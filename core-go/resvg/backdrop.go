package resvg

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// isNativeBackdropGroup reports whether group is a lightweight native `nrbb:` marker (containing only
// the invisible bounding placeholder path emitted when svg.NativeBackdrop is true), rather than a
// standalone SVG export fallback whose children hold an `<feGaussianBlur>` group with `<use>` refs.
func isNativeBackdropGroup(group *usvg.Group) bool {
	if len(group.Filters()) != 0 || group.Mask() == nil {
		return false
	}
	kids := group.Children()
	if len(kids) == 0 {
		return true
	}
	if len(kids) != 1 {
		return false
	}
	p, ok := kids[0].(*usvg.Path)
	return ok && (p.Fill() == nil || p.Fill().Opacity() == 0) && p.Stroke() == nil
}

// renderBackdropBlur paints a native `nrbb:{uid}:{sigma}` background-blur marker directly from
// the already-rasterized pixels in pixmap (and any enclosing clip/isolation layers in bgStack),
// avoiding O(2^N) SVG `<use>` expansion while preserving exact backdrop replacement over translucent ancestors.
func renderBackdropBlur(group *usvg.Group, rest string, transform skia.Transform, pixmap skia.PixmapMut, bgStack []backdropLayer, curState *isolatedState) {
	mask := group.Mask()
	if mask == nil || !mask.Root().HasChildren() {
		return
	}
	colon := strings.LastIndexByte(rest, ':')
	if colon < 0 {
		return
	}
	sigma, err := strconv.ParseFloat(rest[colon+1:], 64)
	if err != nil || sigma <= 0 || math.IsNaN(sigma) || math.IsInf(sigma, 0) {
		return
	}

	bbox, ok := group.LayerBoundingBox().Transform(transform)
	if !ok {
		bbox, ok = mask.Rect().Transform(transform)
		if !ok {
			return
		}
	}

	pw, ph := int32(pixmap.Width()), int32(pixmap.Height())
	x0 := max(int32(math.Floor(float64(bbox.X()))), 0)
	y0 := max(int32(math.Floor(float64(bbox.Y()))), 0)
	x1 := min(int32(math.Ceil(float64(bbox.X()+bbox.Width()))), pw)
	y1 := min(int32(math.Ceil(float64(bbox.Y()+bbox.Height()))), ph)
	if x1 <= x0 || y1 <= y0 {
		return
	}
	rw, rh := uint32(x1-x0), uint32(y1-y0)

	scaleX := math.Hypot(float64(transform.SX), float64(transform.KY))
	scaleY := math.Hypot(float64(transform.KX), float64(transform.SY))
	sigmaX := sigma * scaleX
	sigmaY := sigma * scaleY
	padX := int32(min(math.Ceil(4*sigmaX), 256))
	padY := int32(min(math.Ceil(4*sigmaY), 256))

	blurW := rw + uint32(2*padX)
	blurH := rh + uint32(2*padY)
	blurBuf, ok := skia.NewPixmap(blurW, blurH)
	if !ok {
		return
	}

	dstData := pixmap.DataMut()
	dstStride := int(pw) * 4
	blurData := blurBuf.Data()
	blurStride := int(blurW) * 4

	if len(bgStack) == 0 {
		// Fast path: root pixmap holds the complete opaque backdrop; copy rows with edge clamping.
		for by := int32(0); by < int32(blurH); by++ {
			sy := clampI32Range(y0-padY+by, 0, ph-1)
			srcRowOff := int(sy) * dstStride
			dstRowOff := int(by) * blurStride

			srcStart := x0 - padX
			srcEnd := srcStart + int32(blurW)

			validStart := max(srcStart, 0)
			validEnd := min(srcEnd, pw)
			leftPad := validStart - srcStart
			if leftPad > 0 {
				r0, g0, b0, a0 := dstData[srcRowOff], dstData[srcRowOff+1], dstData[srcRowOff+2], dstData[srcRowOff+3]
				for bx := int32(0); bx < leftPad; bx++ {
					o := dstRowOff + int(bx)*4
					blurData[o+0] = r0
					blurData[o+1] = g0
					blurData[o+2] = b0
					blurData[o+3] = a0
				}
			}
			if validEnd > validStart {
				copyLen := int(validEnd-validStart) * 4
				copy(
					blurData[dstRowOff+int(leftPad)*4:dstRowOff+int(leftPad)*4+copyLen],
					dstData[srcRowOff+int(validStart)*4:srcRowOff+int(validStart)*4+copyLen],
				)
			}
			rightStart := validEnd - srcStart
			if rightStart < int32(blurW) {
				lastOff := srcRowOff + int(pw-1)*4
				r1, g1, b1, a1 := dstData[lastOff], dstData[lastOff+1], dstData[lastOff+2], dstData[lastOff+3]
				for bx := max(rightStart, 0); bx < int32(blurW); bx++ {
					o := dstRowOff + int(bx)*4
					blurData[o+0] = r1
					blurData[o+1] = g1
					blurData[o+2] = b1
					blurData[o+3] = a1
				}
			}
		}
	} else {
		// Isolated group (e.g. clip: true frame or layer_blur group): composite current sub-pixmap over ancestor
		// backdrop layers using remaining ancestor transmittance so translucent backdrops are never double-composited.
		for by := int32(0); by < int32(blurH); by++ {
			rawY := y0 - padY + by
			dstRowOff := int(by) * blurStride
			for bx := int32(0); bx < int32(blurW); bx++ {
				rawX := x0 - padX + bx
				var r, g, b, a uint32
				remTrans := uint32(255)
				if rawX >= 0 && rawX < pw && rawY >= 0 && rawY < ph {
					pxIdx := int(rawY)*int(pw) + int(rawX)
					so := pxIdx * 4
					r = uint32(dstData[so+0])
					g = uint32(dstData[so+1])
					b = uint32(dstData[so+2])
					a = uint32(dstData[so+3])
					remTrans = curState.transmittanceAt(pxIdx, a)
				}
				for li := len(bgStack) - 1; li >= 0 && remTrans > 0; li-- {
					l := bgStack[li]
					if l.w == 0 || l.h == 0 {
						continue
					}
					lx := rawX + l.offX
					ly := rawY + l.offY
					if li == 0 {
						lx = clampI32Range(lx, 0, int32(l.w)-1)
						ly = clampI32Range(ly, 0, int32(l.h)-1)
					} else if lx < 0 || lx >= int32(l.w) || ly < 0 || ly >= int32(l.h) {
						continue
					}
					lIdx := int(ly)*int(l.w) + int(lx)
					lo := lIdx * 4
					lr := uint32(l.data[lo+0])
					lg := uint32(l.data[lo+1])
					lb := uint32(l.data[lo+2])
					la := uint32(l.data[lo+3])
					if remTrans == 255 && r == 0 && g == 0 && b == 0 && a == 0 {
						r, g, b, a = lr, lg, lb, la
					} else if la > 0 {
						r += (lr*remTrans + 127) / 255
						g += (lg*remTrans + 127) / 255
						b += (lb*remTrans + 127) / 255
						a += (la*remTrans + 127) / 255
					}
					lTrans := l.state.transmittanceAt(lIdx, la)
					remTrans = (remTrans*lTrans + 127) / 255
				}
				o := dstRowOff + int(bx)*4
				blurData[o+0] = uint8(min(r, 255))
				blurData[o+1] = uint8(min(g, 255))
				blurData[o+2] = uint8(min(b, 255))
				blurData[o+3] = uint8(min(a, 255))
			}
		}
	}

	if sigmaX >= 0.25 || sigmaY >= 0.25 {
		boxBlur(sigmaX, sigmaY, imageRef{data: blurData, width: blurW, height: blurH})
	}

	// Render alpha mask at screen coordinates of (x0, y0, rw, rh).
	maskPM, ok := skia.NewPixmap(rw, rh)
	if !ok {
		return
	}
	maskTf := skia.TransformFromTranslate(-float32(x0), -float32(y0)).PreConcat(transform)
	alphaMask, ok := skia.NewMask(rw, rh)
	if !ok {
		return
	}
	alphaMask.FillPath(skia.PathFromRect(mask.Rect().ToRect()), skia.FillRuleWinding, true, maskTf)
	renderNodes(mask.Root(), maskTf, maskPM.AsMut())
	maskPM.ApplyMask(alphaMask)
	if sub := mask.Mask(); sub != nil {
		applyMask(sub, maskTf, maskPM)
	}

	maskData := maskPM.Data()
	maskStride := int(rw) * 4
	opacity := float64(group.Opacity())
	if opacity <= 0 {
		return
	}
	useOpacity := opacity < 0.9999

	var rho []uint8
	if len(bgStack) > 0 && curState != nil {
		rho = curState.ensureRho()
	}

	// Exact premultiplied lerp: dst = blur * m + dst * (1 - m)
	for y := uint32(0); y < rh; y++ {
		dstRow := int(uint32(y0)+y)*dstStride + int(x0)*4
		blurRow := int(uint32(padY)+y)*blurStride + int(padX)*4
		maskRow := int(y) * maskStride
		rowPxBase := (int(y0)+int(y))*int(pw) + int(x0)
		for x := uint32(0); x < rw; x++ {
			xo := int(x) * 4
			m := uint32(maskData[maskRow+xo+3])
			if m == 0 {
				continue
			}
			if useOpacity {
				m = uint32(math.Round(float64(m) * opacity))
				if m == 0 {
					continue
				}
			}
			do := dstRow + xo
			bo := blurRow + xo
			pxIdx := rowPxBase + int(x)
			var t0 uint32
			if rho != nil {
				t0 = curState.transmittanceAt(pxIdx, uint32(dstData[do+3]))
			}
			if m == 255 {
				dstData[do+0] = blurData[bo+0]
				dstData[do+1] = blurData[bo+1]
				dstData[do+2] = blurData[bo+2]
				dstData[do+3] = blurData[bo+3]
			} else {
				inv := 255 - m
				dstData[do+0] = uint8((uint32(blurData[bo+0])*m + uint32(dstData[do+0])*inv + 127) / 255)
				dstData[do+1] = uint8((uint32(blurData[bo+1])*m + uint32(dstData[do+1])*inv + 127) / 255)
				dstData[do+2] = uint8((uint32(blurData[bo+2])*m + uint32(dstData[do+2])*inv + 127) / 255)
				dstData[do+3] = uint8((uint32(blurData[bo+3])*m + uint32(dstData[do+3])*inv + 127) / 255)
			}
			if rho != nil {
				t1 := (t0*(255-m) + 127) / 255
				invNewA := 255 - uint32(dstData[do+3])
				if invNewA == 0 || t1 == 0 {
					rho[pxIdx] = 0
				} else if t1 >= invNewA {
					rho[pxIdx] = 255
				} else {
					rho[pxIdx] = uint8((t1*255 + invNewA/2) / invNewA)
				}
			}
		}
	}
}

func compositeBackdropReplacedLayer(
	offX, offY int32,
	sub, koPM *skia.Pixmap,
	opacity float32,
	dst skia.PixmapMut,
	parentState *isolatedState,
) {
	op := float64(opacity)
	if op <= 0 {
		return
	}
	useOp := op < 0.9999
	sw, sh := int32(sub.Width()), int32(sub.Height())
	dw, dh := int32(dst.Width()), int32(dst.Height())
	subData := sub.Data()
	koData := koPM.Data()
	dstData := dst.DataMut()
	var parentRho []uint8
	if parentState != nil {
		parentRho = parentState.ensureRho()
	}

	sy0 := max(-offY, 0)
	sy1 := min(dh-offY, sh)
	sx0 := max(-offX, 0)
	sx1 := min(dw-offX, sw)
	if sx1 <= sx0 || sy1 <= sy0 {
		return
	}

	for sy := sy0; sy < sy1; sy++ {
		dy := sy + offY
		sRow := int(sy) * int(sw)
		dRow := int(dy) * int(dw)
		for sx := sx0; sx < sx1; sx++ {
			dx := sx + offX
			sIdx := sRow + int(sx)
			so := sIdx * 4
			sr := uint32(subData[so+0])
			sg := uint32(subData[so+1])
			sb := uint32(subData[so+2])
			sa := uint32(subData[so+3])
			ka := uint32(koData[so+3])
			if useOp {
				sr = uint32(math.Round(float64(sr) * op))
				sg = uint32(math.Round(float64(sg) * op))
				sb = uint32(math.Round(float64(sb) * op))
				sa = uint32(math.Round(float64(sa) * op))
				ka = uint32(math.Round(float64(ka) * op))
			}
			if ka < sa {
				ka = sa
			}
			if ka == 0 && sa == 0 {
				continue
			}
			dIdx := dRow + int(dx)
			do := dIdx * 4
			var pt0 uint32
			if parentRho != nil {
				pt0 = parentState.transmittanceAt(dIdx, uint32(dstData[do+3]))
			}
			if ka >= 255 {
				dstData[do+0] = uint8(min(sr, 255))
				dstData[do+1] = uint8(min(sg, 255))
				dstData[do+2] = uint8(min(sb, 255))
				dstData[do+3] = uint8(min(sa, 255))
			} else {
				invK := 255 - ka
				dstData[do+0] = uint8(min(sr+(uint32(dstData[do+0])*invK+127)/255, 255))
				dstData[do+1] = uint8(min(sg+(uint32(dstData[do+1])*invK+127)/255, 255))
				dstData[do+2] = uint8(min(sb+(uint32(dstData[do+2])*invK+127)/255, 255))
				dstData[do+3] = uint8(min(sa+(uint32(dstData[do+3])*invK+127)/255, 255))
			}
			if parentRho != nil {
				pt1 := (pt0*(255-min(ka, 255)) + 127) / 255
				invDstA := 255 - uint32(dstData[do+3])
				if invDstA == 0 || pt1 == 0 {
					parentRho[dIdx] = 0
				} else if pt1 >= invDstA {
					parentRho[dIdx] = 255
				} else {
					parentRho[dIdx] = uint8((pt1*255 + invDstA/2) / invDstA)
				}
			}
		}
	}
}

func clampI32Range(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
