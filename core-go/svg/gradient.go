package svg

// Conic and mesh gradients, which SVG cannot express: rasterised into image patterns (svg.rs `conic`,
// `raster_pattern`, raster.rs `mesh_gradient`).

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"math"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Pixmap is premultiplied RGBA8 pixels, top row first (tiny-skia's Pixmap).
type Pixmap struct {
	W, H int
	Pix  []uint8
}

func newPixmap(w, h int) *Pixmap { return &Pixmap{w, h, make([]uint8, 4*w*h)} }

// premultiplyU8 is tiny-skia's `premultiply_u8`.
func premultiplyU8(c, a uint8) uint8 {
	prod := uint32(c)*uint32(a) + 128
	return uint8((prod + prod>>8) >> 8)
}

func premultiply(c [4]uint8) [4]uint8 {
	return [4]uint8{premultiplyU8(c[0], c[3]), premultiplyU8(c[1], c[3]), premultiplyU8(c[2], c[3]), c[3]}
}

// satU8 is Rust's `f as u8`: saturating, NaN to 0.
func satU8(v float64) uint8 {
	switch {
	case !(v > 0):
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v)
}

// encodePNG demultiplies as tiny-skia's `encode_png` does and writes a PNG.
func (p *Pixmap) encodePNG() ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, p.W, p.H))
	for i := 0; i < len(p.Pix); i += 4 {
		px := p.Pix[i : i+4]
		if px[3] == 255 {
			copy(img.Pix[i:i+4], px)
			continue
		}
		a := float64(px[3]) / 255
		for c := range 3 {
			img.Pix[i+c] = satU8(float64(px[c])/a + 0.5)
		}
		img.Pix[i+3] = px[3]
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// conic is an angular gradient rasterised over the node box grown by pad (angle 0 points "up" through
// the gradient frame, clockwise on screen).
func (s *Svg) conic(stops []stop, c, up, right [2]float64, b Rect) (string, bool) {
	det := up[0]*right[1] - up[1]*right[0]
	if math.Abs(det) < 1e-12 || b.W <= 0 || b.H <= 0 {
		return "", false
	}
	g := inflate(b, s.pad)
	tau := 2 * pi
	return s.rasterPattern(g, func(pw, ph int) *Pixmap {
		pm := newPixmap(pw, ph)
		for i := range pw * ph {
			x := g.X + (float64(i%pw)+0.5)/float64(pw)*g.W
			y := g.Y + (float64(i/pw)+0.5)/float64(ph)*g.H
			dx, dy := (x-b.X)/b.W-c[0], (y-b.Y)/b.H-c[1]
			u, v := (dx*right[1]-dy*right[0])/det, (up[0]*dy-up[1]*dx)/det
			r := math.Mod(math.Atan2(v, u), tau)
			if r < 0 {
				r += tau
			}
			col := sample(stops, r/tau)
			var k [4]uint8
			for n := range k {
				k[n] = satU8(math.Round(clamp(col[n], 0, 1) * 255))
			}
			p := premultiply(k)
			copy(pm.Pix[4*i:], p[:])
		}
		return pm
	})
}

// rasterPattern is an image pattern over the box b of the pixels draw(width, height) makes; false when
// b is empty or draw gives nil.
func (s *Svg) rasterPattern(b Rect, draw func(w, h int) *Pixmap) (string, bool) {
	if b.W <= 0 || b.H <= 0 {
		return "", false
	}
	// A fixed raster (≤512 px, 2× small boxes).
	scale := fmin(512/fmax(b.W, b.H), 2)
	pm := draw(int(fmax(math.Ceil(b.W*scale), 1)), int(fmax(math.Ceil(b.H*scale), 1)))
	if pm == nil {
		return "", false
	}
	data, err := pm.encodePNG()
	if err != nil {
		return "", false
	}
	id := s.id("p")
	s.defs.WriteString(`<pattern id="` + id + `" patternUnits="userSpaceOnUse" x="` + f(b.X) + `" y="` + f(b.Y) + `" width="` + f(b.W) + `" height="` + f(b.H) +
		`"><image width="` + f(b.W) + `" height="` + f(b.H) + `" preserveAspectRatio="none" xlink:href="data:image/png;base64,` + base64.StdEncoding.EncodeToString(data) + `"/></pattern>`)
	return id, true
}

type meshVertex struct {
	p [2]int64
	c [4]float64
}

// meshGradient is a mesh gradient fill over a w×h pixmap of the node box, or nil when the fill is invalid.
// `columns`×`rows` points (row-major, one color each) are `[x, y]` or `{position, leftHandle, rightHandle,
// topHandle, bottomHandle}` in 0..1 of the box. Each cell is a bicubic patch tessellated 32×32 with
// smoothstep vertex colors (truncated), drawn as Gouraud triangles without antialiasing, src-over.
func meshGradient(o *jsonv.Object, w, h int) *Pixmap {
	const T = 32
	points, ok1 := jsonv.Get(o, "points").(*jsonv.Array)
	colorsV, ok2 := jsonv.Get(o, "colors").(*jsonv.Array)
	dim := func(k string) (int, bool) {
		n, ok := num(jsonv.Get(o, k))
		if !ok || !(n >= 1) || n != math.Trunc(n) || math.IsInf(n, 0) || !ok1 || n > float64(len(points.Items)) {
			return 0, false
		}
		return int(n), true
	}
	cols, okc := dim("columns")
	rows, okr := dim("rows")
	if !okc || !okr || !ok2 || cols*rows != len(points.Items) || len(colorsV.Items) != len(points.Items) {
		return nil
	}
	colors := make([][4]float64, len(colorsV.Items))
	for i, c := range colorsV.Items {
		s, _ := jsonv.Str(c)
		k, _ := RGBA8(s)
		colors[i] = [4]float64{float64(k[0]), float64(k[1]), float64(k[2]), float64(k[3])}
	}
	cw, ch := 1/float64(max(cols-1, 1)), 1/float64(max(rows-1, 1))
	xy := func(v jsonv.Value) ([2]float64, bool) {
		it := jsonv.Items(v)
		if len(it) < 2 {
			return [2]float64{}, false
		}
		x, okx := num(it[0])
		y, oky := num(it[1])
		return [2]float64{x, y}, okx && oky
	}
	pts := make([][5][2]float64, len(points.Items))
	for i, p := range points.Items {
		at := func(k string, d [2]float64) [2]float64 {
			if v, ok := get(p, k); ok {
				if q, ok := xy(v); ok {
					return q
				}
			}
			return d
		}
		pos := p
		if v, ok := get(p, "position"); ok {
			pos = v
		}
		home, ok := xy(pos)
		if !ok {
			home = [2]float64{float64(i%cols) * cw, float64(i/cols) * ch}
		}
		pts[i] = [5][2]float64{home, at("leftHandle", [2]float64{-cw / 4, 0}), at("rightHandle", [2]float64{cw / 4, 0}),
			at("topHandle", [2]float64{0, -ch / 4}), at("bottomHandle", [2]float64{0, ch / 4})}
	}
	bern := func(t float64) [4]float64 {
		u := 1 - t
		return [4]float64{u * u * u, 3 * t * (u * u), 3 * t * t * (1 - t), t * t * t}
	}
	smooth := func(t float64) float64 { return t * t * (3 - 2*t) }
	add := func(a, b [2]float64) [2]float64 { return [2]float64{a[0] + b[0], a[1] + b[1]} }
	// 1/256 px fixed point, so triangles sharing an edge agree exactly on which pixels it owns.
	fixed := func(v float64, n int) int64 { return int64(math.Round(clamp(v*float64(n), -1e6, 1e6) * 256)) }
	pm := newPixmap(w, h)
	if cols < 2 || rows < 2 {
		return pm
	}
	gw := (cols-1)*T + 1
	type patch struct {
		k [4]int
		g [4][4][2]float64
	}
	var prev []meshVertex
	for y := range rows - 1 {
		patches := make([]patch, cols-1)
		for x := range patches {
			k := [4]int{y*cols + x, y*cols + x + 1, (y+1)*cols + x, (y+1)*cols + x + 1}
			var g [4][4][2]float64
			for q, rc := range [4][4]int{{0, 0, 2, 4}, {0, 3, 1, 4}, {3, 0, 2, 3}, {3, 3, 1, 3}} {
				p := pts[k[q]]
				r, c, hu, hv := rc[0], rc[1], rc[2], rc[3]
				r1, c1 := 2, 2
				if r == 0 {
					r1 = 1
				}
				if c == 0 {
					c1 = 1
				}
				g[r][c], g[r][c1], g[r1][c], g[r1][c1] = p[0], add(p[0], p[hu]), add(p[0], p[hv]), add(add(p[0], p[hu]), p[hv])
			}
			patches[x] = patch{k, g}
		}
		j0 := 0
		if y > 0 {
			j0 = 1
		}
		for j := j0; j <= T; j++ {
			v := float64(j) / T
			bv, sv := bern(v), smooth(v)
			row := make([]meshVertex, gw)
			for gx := range row {
				x := min(gx/T, cols-2)
				u := float64(gx-x*T) / T
				pa, bu, su := patches[x], bern(u), smooth(u)
				var pt [2]float64
				for r := range 4 {
					for c := range 4 {
						q := pa.g[r][c]
						pt = add(pt, [2]float64{q[0] * bv[r] * bu[c], q[1] * bv[r] * bu[c]})
					}
				}
				wt := [4]float64{(1 - sv) * (1 - su), (1 - sv) * su, sv * (1 - su), sv * su}
				var color [4]float64
				for n := range color {
					sum := 0.0
					for q := range 4 {
						sum += colors[pa.k[q]][n] * wt[q]
					}
					color[n] = math.Trunc(sum)
				}
				row[gx] = meshVertex{[2]int64{fixed(pt[0], w), fixed(pt[1], h)}, color}
			}
			for i := 0; i+1 < len(prev); i++ {
				triangle(pm, [3]meshVertex{prev[i], row[i], prev[i+1]})
				triangle(pm, [3]meshVertex{row[i], prev[i+1], row[i+1]})
			}
			prev = row
		}
	}
	return pm
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// triangle draws one Gouraud triangle (1/256 px fixed-point corners, straight 0–255 colors) without
// antialiasing: each pixel centre inside gets the barycentric mix, premultiplied, src-over.
func triangle(pm *Pixmap, v [3]meshVertex) {
	cross := func(a, b, p [2]int64) int64 { return (b[0]-a[0])*(p[1]-a[1]) - (b[1]-a[1])*(p[0]-a[0]) }
	area := cross(v[0].p, v[1].p, v[2].p)
	if area < 0 {
		v[1], v[2] = v[2], v[1]
		area = -area
	}
	if area == 0 {
		return
	}
	w, h := int64(pm.W), int64(pm.H)
	span := func(k int, n int64) (int64, int64) {
		lo, hi := min(v[0].p[k], v[1].p[k], v[2].p[k]), max(v[0].p[k], v[1].p[k], v[2].p[k])
		return max(floorDiv(lo+127, 256), 0), min(floorDiv(hi-128, 256), n-1)
	}
	// Centres exactly on an edge belong to the triangle it runs down (or left) in.
	owns := func(a, b [2]int64) bool { return b[1] > a[1] || (b[1] == a[1] && b[0] < a[0]) }
	edges := [3][2]int{{1, 2}, {2, 0}, {0, 1}}
	y0, y1 := span(1, h)
	x0, x1 := span(0, w)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			p := [2]int64{x*256 + 128, y*256 + 128}
			var e [3]int64
			inside := true
			for i, ed := range edges {
				e[i] = cross(v[ed[0]].p, v[ed[1]].p, p)
				if !(e[i] > 0 || (e[i] == 0 && owns(v[ed[0]].p, v[ed[1]].p))) {
					inside = false
				}
			}
			if !inside {
				continue
			}
			var c [4]uint8
			for n := range c {
				sum := 0.0
				for i := range 3 {
					sum += float64(e[i]) * v[i].c[n]
				}
				c[n] = satU8(clamp(math.Round(sum/float64(area)), 0, 255))
			}
			s := premultiply(c)
			d := pm.Pix[4*(y*w+x) : 4*(y*w+x)+4]
			ia := 255 - uint32(s[3])
			over := func(s, d uint8) uint8 { return s + uint8((uint32(d)*ia+127)/255) }
			out := [4]uint8{over(s[0], d[0]), over(s[1], d[1]), over(s[2], d[2]), over(s[3], d[3])}
			if out[0] > out[3] || out[1] > out[3] || out[2] > out[3] {
				out = s
			}
			copy(d, out[:])
		}
	}
}
