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
// the already-rasterized pixels in pixmap, avoiding O(2^N) SVG `<use>` expansion.
func renderBackdropBlur(group *usvg.Group, rest string, transform skia.Transform, pixmap skia.PixmapMut) {
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
	padX := int32(min(math.Ceil(3*sigmaX), 256))
	padY := int32(min(math.Ceil(3*sigmaY), 256))

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

	// Copy backdrop pixels into padded blur buffer with edge clamping so borders never darken.
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

	// Exact premultiplied lerp: dst = blur * m + dst * (1 - m)
	for y := uint32(0); y < rh; y++ {
		dstRow := int(uint32(y0)+y)*dstStride + int(x0)*4
		blurRow := int(uint32(padY)+y)*blurStride + int(padX)*4
		maskRow := int(y) * maskStride
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
			if m == 255 {
				dstData[do+0] = blurData[bo+0]
				dstData[do+1] = blurData[bo+1]
				dstData[do+2] = blurData[bo+2]
				dstData[do+3] = blurData[bo+3]
				continue
			}
			inv := 255 - m
			dstData[do+0] = uint8((uint32(blurData[bo+0])*m + uint32(dstData[do+0])*inv + 127) / 255)
			dstData[do+1] = uint8((uint32(blurData[bo+1])*m + uint32(dstData[do+1])*inv + 127) / 255)
			dstData[do+2] = uint8((uint32(blurData[bo+2])*m + uint32(dstData[do+2])*inv + 127) / 255)
			dstData[do+3] = uint8((uint32(blurData[bo+3])*m + uint32(dstData[do+3])*inv + 127) / 255)
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
