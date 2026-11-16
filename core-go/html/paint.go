package html

// Fills as CSS background layers (html.rs `fill_layers`, `gradient_css`, `svg_gradient`, `image_layer`,
// `raster_layer`).

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// overlay is the CSS of an overlay box: it sits above the node's own background and below its children.
const overlay = "position:absolute;z-index:-1;border-radius:inherit;pointer-events:none;"

func cssColor(v string) (string, bool) { return cssColorAlpha(v, 1) }

// cssColorAlpha is cssColor with the alpha scaled by k (a paint's `opacity`).
func cssColorAlpha(v string, k float64) (string, bool) {
	c, a, ok := svg.Color(v)
	if !ok {
		return "", false
	}
	a *= k
	if a >= 0.999 {
		return c, true
	}
	p := func(i int) uint64 {
		b, _ := strconv.ParseUint(c[1+i:3+i], 16, 8)
		return b
	}
	return fmt.Sprintf("rgba(%d, %d, %d, %.3f)", p(0), p(2), p(4), a), true
}

// visibleFill reports whether a fill paints anything in HTML: a paint with alpha above zero, of a type
// fillLayers draws (mesh gradients and shaders are not).
func visibleFill(n *resolve.RNode) bool {
	for _, f := range fills(n, "fill") {
		switch x := f.(type) {
		case string:
			if _, a, ok := svg.Color(x); ok && a > 0 {
				return true
			}
		case *jsonv.Object:
			switch fillType(x) {
			case "color":
				if c, ok := jsonv.Str(jsonv.Get(x, "color")); ok {
					if _, a, ok := svg.Color(c); ok && a > 0 {
						return true
					}
				}
			case "gradient", "image":
				if numOr(jsonv.Get(x, "opacity"), 1) > 0 {
					return true
				}
			}
		}
	}
	return false
}

type gradStop struct {
	pos   float64
	color string
}

// totalKey orders floats as Rust's `total_cmp`.
func totalKey(f float64) int64 {
	b := int64(math.Float64bits(f))
	if b < 0 {
		b ^= math.MaxInt64
	}
	return b
}

// gradientCSS is the CSS gradient of a gradient paint. Backgrounds have no per-layer opacity, so `opacity` goes
// into the stop alphas.
func gradientCSS(o *jsonv.Object) (string, bool) {
	opacity := clamp(numOr(jsonv.Get(o, "opacity"), 1), 0, 1)
	// Like the canvas: positions clamped to 0..1 and sorted.
	// An unparseable colour is transparent black, not a dropped stop.
	var stops []gradStop
	for _, c := range jsonv.Items(jsonv.Get(o, "colors")) {
		col := "rgba(0, 0, 0, 0)"
		if s, ok := jsonv.Str(jsonv.Get(c, "color")); ok {
			if v, ok := cssColorAlpha(s, opacity); ok {
				col = v
			}
		}
		stops = append(stops, gradStop{clamp(numOr(jsonv.Get(c, "position"), 0), 0, 1), col})
	}
	if len(stops) == 0 {
		return "", false
	}
	sort.SliceStable(stops, func(i, j int) bool { return totalKey(stops[i].pos) < totalKey(stops[j].pos) })
	rot := numOr(jsonv.Get(o, "rotation"), 0)
	pt := func(k, a string, d float64) float64 { return numOr(jsonv.Get(jsonv.Get(o, k), a), d) }
	at := func(k string) float64 { return pt("center", k, 0.5) * 100 }
	kind, _ := jsonv.Str(jsonv.Get(o, "gradientType"))
	// CSS has no shifted or resized linear gradient, nor a rotated ellipse (a multiple of 90deg or a circle needs none),
	// and its linear gradient line is measured in pixels, so a slanted one differs from the canvas, which rotates the unit frame first.
	quarter := math.Abs(rot/90-math.Round(rot/90)) < 1e-9
	rw, rh := pt("size", "width", 1), pt("size", "height", 1)
	var beyond bool
	switch kind {
	case "radial":
		beyond = !quarter && rw != rh
	case "angular":
		beyond = false
	default:
		beyond = !quarter || at("x") != 50 || at("y") != 50 || rh != 1
	}
	if beyond {
		return svgGradient(kind == "radial", at("x")/100, at("y")/100, rw, rh, rot, stops), true
	}
	list := make([]string, len(stops))
	for i, s := range stops {
		list[i] = s.color + " " + ff(s.pos*100) + "%"
	}
	joined := strings.Join(list, ",")
	switch kind {
	// The canvas ellipse spans `size` of the box; CSS radii are box percentages.
	case "radial":
		rx, ry := rw/2, rh/2
		a := toRad(-rot)
		sn, cs := math.Sin(a), math.Cos(a)
		r := func(a, b float64) string { return ff(fmax(math.Hypot(a, b)*100, 1e-4)) }
		return "radial-gradient(" + r(rx*cs, ry*sn) + "% " + r(rx*sn, ry*cs) + "% at " + ff(at("x")) + "% " + ff(at("y")) + "%," + joined + ")", true
	// Angle 0 points up and runs clockwise, like the canvas; rotation is CCW.
	case "angular":
		return "conic-gradient(from " + ff(-rot) + "deg at " + ff(at("x")) + "% " + ff(at("y")) + "%," + joined + ")", true
	}
	return "linear-gradient(" + ff(-rot) + "deg," + joined + ")", true
}

// svgGradient is a gradient as an SVG background over the unit box, stretched to the element, with the geometry
// of the SVG export: the frame (rotated CCW, then scaled with the box) is `size` wide and tall around the center.
func svgGradient(radial bool, cx, cy, w, h, rot float64, stops []gradStop) string {
	half := func(v float64) float64 {
		if v == 0 {
			return 1e-6
		}
		return v / 2
	}
	rx, ry := half(w), half(h)
	a := toRad(-rot)
	sn, cs := math.Sin(a), math.Cos(a)
	up := [2]float64{float64(ry * sn), -float64(ry * cs)}
	right := [2]float64{float64(rx * cs), float64(rx * sn)}
	var tags strings.Builder
	for _, s := range stops {
		tags.WriteString(`<stop offset="` + ff(px(s.pos)) + `" stop-color="` + s.color + `"/>`)
	}
	var g string
	if radial {
		g = `<radialGradient id="g" cx="0" cy="0" r="1" gradientTransform="matrix(` + ff(px(right[0])) + " " + ff(px(right[1])) + " " + ff(px(-up[0])) + " " + ff(px(-up[1])) + " " +
			ff(px(cx)) + " " + ff(px(cy)) + `)">` + tags.String() + `</radialGradient>`
	} else {
		g = `<linearGradient id="g" x1="` + ff(px(cx-up[0])) + `" y1="` + ff(px(cy-up[1])) + `" x2="` + ff(px(cx+up[0])) + `" y2="` + ff(px(cy+up[1])) + `">` +
			tags.String() + `</linearGradient>`
	}
	doc := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1" preserveAspectRatio="none"><defs>` + g + `</defs><rect width="1" height="1" fill="url(#g)"/></svg>`
	uri := strings.NewReplacer("%", "%25", `"`, "%22", "<", "%3C", ">", "%3E", "#", "%23").Replace(doc)
	return "url('data:image/svg+xml," + uri + "') 0 0/100% 100% no-repeat"
}

// imageLayer is the CSS layer of an image paint and its opacity (a background has none of its own).
func imageLayer(o *jsonv.Object) (string, float64, bool) {
	url, ok := jsonv.Str(jsonv.Get(o, "url"))
	if !ok || url == "" {
		return "", 0, false
	}
	size := "100% 100%"
	switch m, _ := jsonv.Str(jsonv.Get(o, "mode")); m {
	case "fit":
		size = "contain"
	case "fill":
		size = "cover"
	}
	return "url('" + svg.Esc(cssStr(url)) + "') center/" + size + " no-repeat", clamp(numOr(jsonv.Get(o, "opacity"), 1), 0, 1), true
}

// rasterLayer is a mesh gradient or shader paint of the n.W×n.H box as a PNG background layer: CSS has no such
// paint, so it is rasterised like the SVG export does. False for another paint, an empty box or a shader that
// fails (the SVG export's error hatch is a canvas marker; the page shows no fill).
func (h *writer) rasterLayer(o *jsonv.Object, n *resolve.RNode) (string, bool) {
	w, hh := n.W, n.H
	if w <= 0 || hh <= 0 {
		return "", false
	}
	t, ok := paintType(o)
	if !ok {
		return "", false
	}
	var data string
	switch t {
	case "mesh_gradient":
		// The SVG export's resolution: at most 512 px, 2x for small boxes.
		scale := math.Min(512/fmax(w, hh), 2)
		pm := svg.MeshGradient(o, int(satU32(fmax(math.Ceil(w*scale), 1))), int(satU32(fmax(math.Ceil(hh*scale), 1))))
		if pm == nil {
			return "", false
		}
		png, err := pm.EncodePNG()
		if err != nil {
			return "", false
		}
		data = base64.StdEncoding.EncodeToString(png)
	case "shader":
		url := str(jsonv.Get(o, "url"))
		var err error
		data, err = h.shader(o, n, url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "shader fill %s: %v\n", url, err)
			return "", false
		}
	default:
		return "", false
	}
	return "url('data:image/png;base64," + data + "') 0 0/100% 100% no-repeat", true
}

// shader runs the shader fill o over n's box. `@backdrop` reads nothing here (a transparent pixel): the page has
// no canvas to sample.
func (h *writer) shader(o *jsonv.Object, n *resolve.RNode, url string) (string, error) {
	p, ok := h.assets.Image(url, h.base)
	if !ok {
		return "", errors.New("Shader file not found: \"" + url + "\"")
	}
	src, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if svg.DefaultShader == nil {
		return "", errors.New("no shader painter")
	}
	outline := svg.Outline{Kind: svg.OutlineRRect, Radii: svg.Radii(n)}
	if n.Type == "ellipse" {
		outline = svg.Outline{Kind: svg.OutlineEllipse}
	}
	load := func(u string) (*svg.Pixmap, bool) {
		p, ok := h.assets.Image(u, h.base)
		if !ok {
			return nil, false
		}
		return svg.DecodeImage(p)
	}
	return svg.DefaultShader.PNG(string(src), jsonv.Obj(jsonv.Get(o, "uniforms")), n.W, n.H, outline, load, nil)
}

// satU32 is Rust's saturating `as u32` of a non-negative float.
func satU32(v float64) float64 {
	return math.Min(v, math.MaxUint32)
}

// layer is one fill as a CSS background layer: its image, the paint's opacity where CSS cannot fold it into the
// layer, and its blend mode ("" for none).
type layer struct {
	css   string
	alpha float64
	blend string
}

// fillLayers is the fills bottom to top as CSS layers. raster draws the paints CSS cannot express (mesh
// gradients, shaders) into a layer.
func fillLayers(n *resolve.RNode, raster func(o *jsonv.Object) (string, bool)) []layer {
	var layers []layer
	for _, f := range fills(n, "fill") {
		first := len(layers)
		switch x := f.(type) {
		case string:
			if c, ok := cssColor(x); ok {
				layers = append(layers, layer{"linear-gradient(" + c + "," + c + ")", 1, ""})
			}
		case *jsonv.Object:
			switch fillType(x) {
			case "color":
				if s, ok := jsonv.Str(jsonv.Get(x, "color")); ok {
					if c, ok := cssColor(s); ok {
						layers = append(layers, layer{"linear-gradient(" + c + "," + c + ")", 1, ""})
					}
				}
			case "gradient":
				if g, ok := gradientCSS(x); ok {
					layers = append(layers, layer{g, 1, ""})
				}
			case "image":
				if l, a, ok := imageLayer(x); ok {
					layers = append(layers, layer{l, a, ""})
				}
			case "mesh_gradient", "shader":
				opacity := clamp(numOr(jsonv.Get(x, "opacity"), 1), 0, 1)
				if l, ok := raster(x); ok {
					layers = append(layers, layer{l, opacity, ""})
				}
			}
		}
		// The paint just pushed (if any) blends with what is under it.
		if first < len(layers) {
			layers[first].blend = blendOf(f)
		}
	}
	return layers
}

// blendsBackdrop reports a bottom fill or a stroke with a blend mode: it blends with what lies beneath the
// element, not just its own layers.
func blendsBackdrop(n *resolve.RNode) bool {
	if n.Type == "text" || n.Type == "icon" {
		return false
	}
	if l := fillLayers(n, func(*jsonv.Object) (string, bool) { return "", true }); len(l) > 0 && l[0].blend != "" {
		return true
	}
	for _, s := range fills(n, "stroke") {
		if blendOf(s) != "" {
			return true
		}
	}
	return false
}
