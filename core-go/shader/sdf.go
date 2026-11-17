package shader

import (
	"math"
	"math/bits"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// SDFMax caps the `@sdf` texture at 1024 px per side.
const SDFMax = 1024.0

// fmax and fmin are Rust's f64::max/min: a NaN operand gives the other one.
func fmax(a, b float64) float64 {
	if a != a || a < b {
		return b
	}
	return a
}

func fmin(a, b float64) float64 {
	if a != a || a > b {
		return b
	}
	return a
}

func sign(v float64) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// signum is Rust's f64::signum: ±1 by the sign bit, NaN for NaN.
func signum(v float64) float64 {
	if v != v {
		return v
	}
	return math.Copysign(1, v)
}

// SDF is the `@sdf` texture of a w×h node at dpi pixels per unit (shader.rs `sdf`): RGBA half floats, rows
// bottom-up (GL orientation), with R the signed distance to the outline in node units (positive inside),
// G/B its gradient per texel with y up and A 1.
func SDF(o svg.Outline, w, h, dpi float64) (sw, sh int, half []uint16) {
	sw, sh, px := sdfTexels(o, w, h, dpi)
	return sw, sh, halfRows(sw, sh, px)
}

// sdfTexels is the `@sdf` texels, top row first, and the texture size.
func sdfTexels(o svg.Outline, w, h, dpi float64) (int, int, [][4]float32) {
	r := fmax(fmax(w, h)*dpi, 1e-9)
	s := dpi * fmin(r, SDFMax) / r
	sw, sh := int(fmax(math.Ceil(w*s), 1)), int(fmax(math.Ceil(h*s), 1))
	fx, fy := w/float64(sw), h/float64(sh)
	px := make([][4]float32, sw*sh)
	switch o.Kind {
	case svg.OutlineRRect:
		rad := o.Radii
		hw, hh := w/2, h/2
		for row := range sh {
			y := float64((float64(row)+0.5)*fy) - hh
			for col := range sw {
				x := float64((float64(col)+0.5)*fx) - hw
				var r float64
				switch {
				case x < 0 && y < 0:
					r = rad[0]
				case x < 0:
					r = rad[3]
				case y < 0:
					r = rad[1]
				default:
					r = rad[2]
				}
				qx, qy := (math.Abs(x)-hw)+r, (math.Abs(y)-hh)+r
				var d, ux, uy float64
				switch {
				case qx > 0 && qy > 0:
					l := math.Hypot(qx, qy)
					d, ux, uy = r-l, qx/l, qy/l
				case qx > qy:
					d, ux, uy = r-qx, 1, 0
				default:
					d, ux, uy = r-qy, 0, 1
				}
				px[row*sw+col] = [4]float32{float32(d), float32(-sign(x) * ux * fx), float32(sign(y) * uy * fy), 1}
			}
		}
	case svg.OutlineEllipse:
		a, b := w/2, h/2
		for row := range sh {
			y := float64((float64(row)+0.5)*fy) - b
			for col := range sw {
				x := float64((float64(col)+0.5)*fx) - a
				d, nx, ny := ellipseDistance(a, b, x, y)
				// The inward normal at the closest point, y flipped to point up.
				px[row*sw+col] = [4]float32{float32(d), float32(nx * fx), float32(-ny * fy), 1}
			}
		}
	case svg.OutlinePath:
		pathSDF(o.D, o.X, o.Y, o.EvenOdd, sw, sh, w, h, s, px)
	}
	return sw, sh, px
}

// halfRows is the texels as half floats, rows bottom-up.
func halfRows(sw, sh int, px [][4]float32) []uint16 {
	out := make([]uint16, 0, sw*sh*4)
	for row := sh - 1; row >= 0; row-- {
		for _, p := range px[row*sw : (row+1)*sw] {
			out = append(out, F16(p[0]), F16(p[1]), F16(p[2]), F16(p[3]))
		}
	}
	return out
}

// ellipseDistance is the signed distance from (x, y) to the ellipse with semi-axes a, b (positive inside) and
// the unit inward normal at the closest point (y down).
func ellipseDistance(a, b, x, y float64) (float64, float64, float64) {
	px, py := math.Abs(x), math.Abs(y)
	ex, ey := px/a, py/b
	inside := float64(ex*ex)+float64(ey*ey) <= 1
	if math.Abs(a-b) < 1e-9 {
		l := math.Hypot(px, py)
		var nx, ny float64
		if l > 0 {
			nx, ny = -x/l, -y/l
		}
		return a - l, nx, ny
	}
	// Closest point (a cos t, b sin t): Newton on the perpendicularity condition, first quadrant.
	t := clamp(math.Atan2(a*py, b*px), 0, math.Pi/2)
	k := float64(a*a) - float64(b*b)
	for range 8 {
		c, s := math.Cos(t), math.Sin(t)
		f := (float64(float64(k*s)*c) - float64(float64(px*a)*s)) + float64(float64(py*b)*c)
		df := (float64(k*(float64(c*c)-float64(s*s))) - float64(float64(px*a)*c)) - float64(float64(py*b)*s)
		if math.Abs(df) < 1e-12 {
			break
		}
		t = clamp(t-f/df, 0, math.Pi/2)
	}
	ex, ey = float64(a*math.Cos(t)), float64(b*math.Sin(t))
	dist := math.Hypot(px-ex, py-ey)
	gx, gy := math.Cos(t)/a, math.Sin(t)/b
	gl := fmax(math.Hypot(gx, gy), 1e-12)
	nx, ny := -gx/gl*signum(x), -gy/gl*signum(y)
	if !inside {
		dist = -dist
	}
	if x == 0 {
		nx = 0
	}
	if y == 0 {
		ny = 0
	}
	return dist, nx, ny
}

// svgPath is the path d as tiny-skia builds it; false when d has an error or no segments.
func svgPath(d string) (*skia.Path, bool) {
	segs, ok := usvg.SimplifyPathChecked(d)
	if !ok {
		return nil, false
	}
	pb := skia.NewPathBuilder()
	for _, s := range segs {
		switch s.Kind {
		case 'M':
			pb.MoveTo(float32(s.X), float32(s.Y))
		case 'L':
			pb.LineTo(float32(s.X), float32(s.Y))
		case 'C':
			pb.CubicTo(float32(s.X1), float32(s.Y1), float32(s.X2), float32(s.Y2), float32(s.X), float32(s.Y))
		case 'Q':
			pb.QuadTo(float32(s.X1), float32(s.Y1), float32(s.X), float32(s.Y))
		case 'Z':
			pb.Close()
		}
	}
	p := pb.Finish()
	return p, p != nil
}

// sq is x² in f32, rounded before any addition.
func sq(x float32) float32 { return float32(x * x) }

// pathSDF is the raster SDF of an arbitrary outline: anti-aliased coverage, sub-pixel edge seeds, jump
// flooding, distance (sign from coverage), a σ = 3 texel blur and a Scharr gradient.
func pathSDF(d string, x0, y0 float64, evenOdd bool, sw, sh int, w, h, scale float64, px [][4]float32) {
	alpha := make([]float32, sw*sh)
	if path, ok := svgPath(d); ok {
		if pm, ok := skia.NewPixmap(uint32(sw), uint32(sh)); ok {
			paint := skia.DefaultPaint()
			paint.SetColorRGBA8(0, 0, 0, 255)
			paint.AntiAlias = true
			rule := skia.FillRuleWinding
			if evenOdd {
				rule = skia.FillRuleEvenOdd
			}
			ts := skia.TransformFromScale(float32(float64(sw)/w), float32(float64(sh)/h)).PreTranslate(float32(-x0), float32(-y0))
			pm.FillPath(path, &paint, rule, ts, nil)
			data := pm.Data()
			for i := range alpha {
				alpha[i] = float32(data[i*4+3]) / 255
			}
		}
	}
	at := func(c, r int) float32 {
		if c >= 0 && r >= 0 && c < sw && r < sh {
			return alpha[r*sw+c]
		}
		return 0
	}
	far := 2 * float32(max(sw, sh))
	seed := make([][2]float32, sw*sh)
	for i := range seed {
		seed[i] = [2]float32{far, far}
	}
	for r := range sh {
		for c := range sw {
			a := at(c, r)
			var off [2]float32
			var count float32
			for _, n := range [4][2]int{{1, 0}, {0, 1}, {-1, 0}, {0, -1}} {
				v := at(c+n[0], r+n[1])
				if float32((a-0.5)*(v-0.5)) < 0 {
					t := (0.5 - a) / (v - a)
					off[0] += float32(t * float32(n[0]))
					off[1] += float32(t * float32(n[1]))
					count++
				}
			}
			if count > 0 {
				seed[r*sw+c] = [2]float32{float32(c) + off[0]/count, float32(r) + off[1]/count}
			}
		}
	}
	for step := 1 << (bits.Len(uint(max(sw, sh))) - 1); step > 0; step >>= 1 {
		for r := range sh {
			for c := range sw {
				best := seed[r*sw+c]
				bd := sq(float32(c)-best[0]) + sq(float32(r)-best[1])
				for _, nr := range [3]int{r - step, r, r + step} {
					if nr < 0 || nr >= sh {
						continue
					}
					for _, nc := range [3]int{c - step, c, c + step} {
						if nc < 0 || nc >= sw {
							continue
						}
						s := seed[nr*sw+nc]
						if d := sq(float32(c)-s[0]) + sq(float32(r)-s[1]); d < bd {
							bd, best = d, s
						}
					}
				}
				seed[r*sw+c] = best
			}
		}
	}
	dist := make([]float32, sw*sh)
	for i := range dist {
		c, r := float32(i%sw), float32(i/sw)
		s := seed[i]
		v := float32(math.Sqrt(float64(sq(c-s[0])+sq(r-s[1])))) / float32(scale)
		if alpha[i] < 0.5 {
			v = -v
		}
		dist[i] = v
	}
	blur(dist, sw, sh, 3)
	g := func(c, r int) float32 { return dist[r*sw+c] }
	for r := range sh {
		up, down := max(r-1, 0), min(r+1, sh-1)
		for c := range sw {
			left, right := max(c-1, 0), min(c+1, sw-1)
			gx := float32(10.0/32*(g(right, r)-g(left, r))) + float32(3.0/32*(((g(right, up)-g(left, up))+g(right, down))-g(left, down)))
			gy := float32(10.0/32*(g(c, up)-g(c, down))) + float32(3.0/32*(((g(left, up)-g(left, down))+g(right, up))-g(right, down)))
			px[r*sw+c] = [4]float32{g(c, r), gx, gy, 1}
		}
	}
}

// negZero is the start of Rust's `Sum` for floats (since 1.81).
var negZero = float32(math.Copysign(0, -1))

// blur is a separable Gaussian blur, edges clamped.
func blur(v []float32, w, h int, sigma float32) {
	radius := int(math.Ceil(float64(sigma * 3)))
	k := make([]float32, radius+1)
	for i := range k {
		k[i] = float32(math.Exp(float64(float32(-(i * i)) / (2 * sigma * sigma))))
	}
	sum := negZero
	for _, x := range k[1:] {
		sum += x
	}
	sum = k[0] + float32(2*sum)
	for i := range k {
		k[i] /= sum
	}
	tmp := make([]float32, max(w, h))
	for _, p := range [2][4]int{{w, 1, h, w}, {h, w, w, 1}} {
		n, stride, lines, step := p[0], p[1], p[2], p[3]
		for line := range lines {
			at := func(i int) float32 { return v[line*step+min(max(i, 0), n-1)*stride] }
			for i := range n {
				s := negZero
				for j := 1; j <= radius; j++ {
					s += float32(k[j] * (at(i-j) + at(i+j)))
				}
				tmp[i] = float32(k[0]*at(i)) + s
			}
			for i := range n {
				v[line*step+i*stride] = tmp[i]
			}
		}
	}
}
