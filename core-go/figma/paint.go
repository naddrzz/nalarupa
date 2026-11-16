package figma

import (
	"math"
	"slices"
	"strconv"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/libm"
)

// hex is "#rrggbbaa" from Figma's 0..1 floats, alpha times opacity.
func hex(c jsonv.Value, opacity float64) string {
	b := func(k string, m float64) byte {
		return byte(math.Max(0, math.Min(255, math.Round(numOr(get(c, k), 0)*m*255))))
	}
	out := []byte{'#'}
	for _, v := range []byte{b("r", 1), b("g", 1), b("b", 1), b("a", opacity)} {
		out = append(out, hexDigits[v>>4], hexDigits[v&15])
	}
	return string(out)
}

// hexShort drops an opaque alpha (shadow colors).
func hexShort(c jsonv.Value) string {
	h := hex(c, 1)
	if h[7:] == "ff" {
		return h[:7]
	}
	return h
}

var blends = map[string]string{
	"DARKEN": "darken", "MULTIPLY": "multiply", "LINEAR_BURN": "linearBurn", "COLOR_BURN": "colorBurn", "LIGHTEN": "light",
	"SCREEN": "screen", "LINEAR_DODGE": "linearDodge", "COLOR_DODGE": "colorDodge", "OVERLAY": "overlay", "SOFT_LIGHT": "softLight",
	"HARD_LIGHT": "hardLight", "DIFFERENCE": "difference", "EXCLUSION": "exclusion", "HUE": "hue", "SATURATION": "saturation",
	"COLOR": "color", "LUMINOSITY": "luminosity",
}

func blend(m jsonv.Value) (string, bool) {
	s, ok := jsonv.Str(m)
	if !ok {
		return "", false
	}
	b, ok := blends[s]
	return b, ok
}

// paints turns Figma paints into .pen fills. Image bytes are not on the clipboard, so image fills keep
// their mode but have no url (a placeholder).
func paints(v jsonv.Value) []jsonv.Value {
	var out []jsonv.Value
	for _, r := range jsonv.Items(v) {
		enabled := jsonv.IsTrue(get(r, "visible"))
		opacity := numOr(get(r, "opacity"), 1)
		bm, hasBlend := blend(get(r, "blendMode"))
		var o *jsonv.Object
		kind, _ := jsonv.Str(get(r, "type"))
		switch {
		case kind == "SOLID" && isObj(get(r, "color")):
			c := hex(get(r, "color"), opacity)
			if enabled && !hasBlend {
				out = append(out, c)
				continue
			}
			o = obj("type", "color", "color", c)
		case kind == "IMAGE":
			mode := "fill"
			switch jsonv.StrOr(get(r, "imageScaleMode")) {
			case "FIT":
				mode = "fit"
			case "STRETCH":
				mode = "stretch"
			}
			o = obj("type", "image", "mode", mode)
			if opacity != 1 {
				o.Set("opacity", jsonv.Float(opacity))
			}
		case kind == "GRADIENT_LINEAR" || kind == "GRADIENT_RADIAL" || kind == "GRADIENT_ANGULAR":
			if o = gradient(kind, r); o == nil {
				continue
			}
			if opacity != 1 {
				o.Set("opacity", jsonv.Float(opacity))
			}
		default:
			continue
		}
		if !enabled {
			o.Set("enabled", false)
		}
		if hasBlend {
			o.Set("blendMode", bm)
		}
		out = append(out, o)
	}
	return out
}

type stop struct {
	pos   float64
	color string
}

// gradient derives the .pen center, rotation and size from the handles: Figma's gradient transform maps
// the node's unit box to gradient space, and its inverse gives them. nil when the paint has none.
func gradient(kind string, r jsonv.Value) *jsonv.Object {
	t := get(r, "transform")
	var mv [6]float64
	for i, k := range []string{"m00", "m10", "m01", "m11", "m02", "m12"} {
		v, ok := num(get(t, k))
		if !ok {
			return nil
		}
		mv[i] = v
	}
	a, b, c, d, e, g := mv[0], mv[1], mv[2], mv[3], mv[4], mv[5]
	det := float64(a*d) - float64(b*c)
	if det == 0 {
		return nil
	}
	inv := func(x, y float64) [2]float64 {
		return [2]float64{(float64(d*(x-e)) - float64(c*(y-g))) / det, (float64(a*(y-g)) - float64(b*(x-e))) / det}
	}
	var stops []stop
	for _, s := range jsonv.Items(get(r, "stops")) {
		stops = append(stops, stop{numOr(get(s, "position"), 0), hex(get(s, "color"), 1)})
	}
	if len(stops) == 0 {
		return nil
	}
	dist := func(p, q [2]float64) float64 {
		dx, dy := q[0]-p[0], q[1]-p[1]
		return math.Sqrt(float64(dx*dx) + float64(dy*dy))
	}
	var (
		ty     string
		center [2]float64
		rot    float64
		width  float64
		hasW   bool
		height float64
	)
	switch kind {
	case "GRADIENT_LINEAR":
		h, fp, gp := inv(0, 0.5), inv(1, 0.5), inv(0, 1)
		nx, ny := gp[0]-h[0], gp[1]-h[1]
		l := math.Sqrt(float64(nx*nx) + float64(ny*ny))
		if l != 0 {
			nx, ny = nx/l, ny/l
		}
		px, py := -ny, nx
		ln := float64(px*(fp[0]-h[0])) + float64(py*(fp[1]-h[1]))
		ang := libm.Atan2(py, px) + math.Pi/2
		if ln < 0 {
			ln = -ln
			ang += math.Pi
		}
		ty, center, rot, height = "linear", [2]float64{(h[0] + fp[0]) / 2, (h[1] + fp[1]) / 2}, ang, ln
	case "GRADIENT_RADIAL":
		h, fp, gp := inv(0.5, 0.5), inv(1, 0.5), inv(0.5, 1)
		v := [4]float64{fp[0] - h[0], gp[0] - h[0], fp[1] - h[1], gp[1] - h[1]}
		y, x, z := float64(v[0]*v[0])+float64(v[1]*v[1]), float64(v[0]*v[2])+float64(v[1]*v[3]), float64(v[2]*v[2])+float64(v[3]*v[3])
		sum, dt := y+z, float64(y*z)-float64(x*x)
		disc := float64(sum*sum) - float64(4*dt)
		if !(disc >= 0) { // Rust's f64::max(0.0): NaN gives 0
			disc = 0
		}
		disc = math.Sqrt(disc)
		big, small := (sum+disc)/2, (sum-disc)/2
		ty, center, rot = "radial", h, libm.Atan2(big-y, x)+math.Pi/2
		width, hasW, height = math.Sqrt(small)*2, true, math.Sqrt(big)*2
	default:
		h, fp, gp := inv(0.5, 0.5), inv(1, 0.5), inv(0.5, 1)
		ang := libm.Atan2(fp[1]-h[1], fp[0]-h[0]) + math.Pi/2
		cross := float64((gp[0]-h[0])*(fp[1]-h[1])) - float64((gp[1]-h[1])*(fp[0]-h[0]))
		sign := 1.0
		if cross > 0 {
			sign = -1
		}
		// A conic gradient needs stops at 0 and 1: both get the color where the seam is crossed.
		if len(stops) >= 2 {
			slices.SortStableFunc(stops, func(a, b stop) int { return libm.TotalCmp(a.pos, b.pos) })
			first, last := stops[0], stops[len(stops)-1]
			if first.pos != 0 || last.pos != 1 {
				span := first.pos + (1 - last.pos)
				t := 0.0
				if span != 0 {
					t = first.pos / span
				}
				seam := mix(first.color, last.color, t)
				if first.pos != 0 {
					stops = slices.Insert(stops, 0, stop{0, seam})
				}
				if last.pos != 1 {
					stops = append(stops, stop{1, seam})
				}
			}
		}
		ty, center, rot = "angular", h, ang
		width, hasW, height = sign*dist(h, gp)*2, true, dist(h, fp)*2
	}
	o := obj("type", "gradient", "gradientType", ty, "rotation", jsonv.Float(-(rot * deg)))
	s := jsonv.NewObject()
	if hasW {
		s.Set("width", jsonv.Float(width))
	}
	s.Set("height", jsonv.Float(height))
	o.Set("size", s)
	colors := make([]jsonv.Value, len(stops))
	for i, st := range stops {
		colors[i] = obj("color", st.color, "position", jsonv.Float(st.pos))
	}
	o.Set("colors", jsonv.NewArray(colors...))
	if center != [2]float64{0.5, 0.5} {
		o.Set("center", obj("x", jsonv.Float(center[0]), "y", jsonv.Float(center[1])))
	}
	return o
}

// mix is the linear mix of two "#rrggbbaa" colors (the seam color of an angular gradient), from first to
// last.
func mix(first, last string, t float64) string {
	ch := func(s string, i int) float64 {
		v, _ := strconv.ParseUint(s[1+i*2:3+i*2], 16, 8)
		return float64(v)
	}
	out := []byte{'#'}
	for i := range 4 {
		v := math.Round(ch(first, i) + float64(t*(ch(last, i)-ch(first, i))))
		var c byte // Rust's saturating `as u8`
		switch {
		case v >= 255:
			c = 255
		case v > 0:
			c = byte(v)
		}
		out = append(out, hexDigits[c>>4], hexDigits[c&15])
	}
	return string(out)
}

// effects turns Figma effects into .pen effects; blur radii scale by 0.875.
func effects(v jsonv.Value) []jsonv.Value {
	var out []jsonv.Value
	for _, r := range jsonv.Items(v) {
		radius := numOr(get(r, "radius"), 0) * 0.875
		var o *jsonv.Object
		switch kind := jsonv.StrOr(get(r, "type")); kind {
		case "FOREGROUND_BLUR":
			o = obj("type", "blur", "radius", jsonv.Float(radius))
		case "BACKGROUND_BLUR":
			o = obj("type", "background_blur", "radius", jsonv.Float(radius))
		case "DROP_SHADOW", "INNER_SHADOW":
			st := "inner"
			if kind == "DROP_SHADOW" {
				st = "outer"
			}
			o = obj("type", "shadow", "shadowType", st, "color", hexShort(get(r, "color")))
			x, y := numOr(get(r, "offset", "x"), 0), numOr(get(r, "offset", "y"), 0)
			if x != 0 || y != 0 {
				o.Set("offset", obj("x", jsonv.Float(x), "y", jsonv.Float(y)))
			}
			if radius != 0 {
				o.Set("blur", jsonv.Float(radius))
			}
			if b, ok := blend(get(r, "blendMode")); ok {
				o.Set("blendMode", b)
			}
		default:
			continue
		}
		if !jsonv.IsTrue(get(r, "visible")) {
			o.Set("enabled", false)
		}
		out = append(out, o)
	}
	return out
}
