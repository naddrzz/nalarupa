package pdf

// Gradients as krilla writes them (graphics/shading_function.rs, shading_pattern.rs and the shading
// masks of mask.rs): padded linear gradients as axial shadings, radial gradients as radial shadings
// (always extended: krilla ignores their spread method), and repeating or reflecting linear gradients
// as function shadings with a PostScript calculator function. Stop opacities go into a luminosity soft
// mask drawn from the same shading with the opacities as gray.

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

const (
	shadingFunction = 1
	shadingAxial    = 2
	shadingRadial   = 3
)

// gradientProps is GradientProperties: an axial or radial shading, or a PostScript one.
type gradientProps struct {
	kind   int
	coords []float32 // axial and radial
	stops  []gradStop
	// PostScript (repeating or reflecting linear) gradients:
	min, max float32
	domain   skia.Rect
	spread   spreadMethod
}

func (g *gradientProps) singleStop() (gradStop, bool) {
	if len(g.stops) == 1 {
		return g.stops[0], true
	}
	return gradStop{}, false
}

func (g *gradientProps) opaque() bool {
	for _, s := range g.stops {
		if s.opacity != 1 {
			return false
		}
	}
	return true
}

// expandedBBox is get_expanded_bbox: bbox joined with itself mapped back through the shading transform,
// so the shading covers the shape.
func expandedBBox(bbox skia.Rect, ts skia.Transform) skia.Rect {
	inv, ok := ts.Invert()
	if !ok {
		return bbox
	}
	t, ok := bbox.Transform(inv)
	if !ok {
		return bbox
	}
	r, ok := skia.RectFromLTRB(min(bbox.Left(), t.Left()), min(bbox.Top(), t.Top()), max(bbox.Right(), t.Right()), max(bbox.Bottom(), t.Bottom()))
	if !ok {
		return bbox
	}
	return r
}

// linearProps is LinearGradient::gradient_properties.
func linearProps(lg *linearGradient, bbox skia.Rect) (gradientProps, skia.Transform) {
	if lg.spread == spreadPad {
		return gradientProps{kind: shadingAxial, coords: []float32{lg.x1, lg.y1, lg.x2, lg.y2}, stops: lg.stops}, lg.ts
	}
	// get_point_ts: rotate the gradient onto the x axis.
	p1, p2 := skia.Pt(lg.x1, lg.y1), skia.Pt(lg.x2, lg.y2)
	dist := p1.Distance(p2)
	angle := float32(float32(math.Atan2(float64(p2.Y-p1.Y), float64(p2.X-p1.X))) * float32(180/math.Pi))
	ts := lg.ts.PreConcat(skia.TransformFromRotateAt(angle, p1.X, p1.Y))
	return gradientProps{
		kind:   shadingFunction,
		min:    p1.X,
		max:    p1.X + dist,
		stops:  lg.stops,
		domain: expandedBBox(bbox, ts),
		spread: lg.spread,
	}, ts
}

// radialProps is RadialGradient::gradient_properties.
func radialProps(rg *radialGradient) (gradientProps, skia.Transform) {
	return gradientProps{kind: shadingRadial, coords: []float32{rg.fx, rg.fy, rg.fr, rg.cx, rg.cy, rg.cr}, stops: rg.stops}, rg.ts
}

func stopComponents(s gradStop, opacities bool) []float32 {
	if opacities {
		return []float32{s.opacity}
	}
	return []float32{float32(s.color[0]) / 255, float32(s.color[1]) / 255, float32(s.color[2]) / 255}
}

func numsArray(fs []float32) string { return "[" + nums(fs...) + "]" }

func repeatRange(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "0 1"
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// exponential writes a type 2 function from c0 to c1.
func (d *document) exponential(c0, c1 []float32) ref {
	r := d.w.alloc()
	d.w.obj(r, "<</FunctionType 2 /Range "+repeatRange(len(c0))+" /C0 "+numsArray(c0)+" /C1 "+numsArray(c1)+" /Domain [0 1] /N 1>>")
	return r
}

// axialRadialFunction is select_axial_radial_function: stops padded to 0 and 1, then one exponential
// function or a stitching function of them.
func (d *document) axialRadialFunction(g *gradientProps, opacities bool) ref {
	stops := append([]gradStop(nil), g.stops...)
	if stops[0].offset != 0 {
		s := stops[0]
		s.offset = 0
		stops = append([]gradStop{s}, stops...)
	}
	if last := stops[len(stops)-1]; last.offset != 1 {
		last.offset = 1
		stops = append(stops, last)
	}
	if len(stops) == 2 {
		return d.exponential(stopComponents(stops[0], opacities), stopComponents(stops[1], opacities))
	}
	var funcs []string
	var bounds, encode []float32
	count := 0
	for i := 0; i+1 < len(stops); i++ {
		bounds = append(bounds, stops[i+1].offset)
		c0, c1 := stopComponents(stops[i], opacities), stopComponents(stops[i+1], opacities)
		count = len(c0)
		funcs = append(funcs, d.exponential(c0, c1).String())
		encode = append(encode, 0, 1)
	}
	bounds = bounds[:len(bounds)-1]
	r := d.w.alloc()
	d.w.obj(r, "<</FunctionType 3 /Domain [0 1] /Range "+repeatRange(count)+" /Functions ["+strings.Join(funcs, " ")+"] /Bounds "+numsArray(bounds)+" /Encode "+numsArray(encode)+">>")
	return r
}

// psCode collects PostScript calculator operators.
type psCode []string

func (c *psCode) real(f float32) { *c = append(*c, decimal(f)) }
func (c *psCode) int(i int)      { *c = append(*c, strconv.Itoa(i)) }
func (c *psCode) ops(ops ...string) {
	*c = append(*c, ops...)
}

// block is PostScriptOp's write_slice: "{ a b }", or "{a}" for one operator.
func (c psCode) block() string {
	if len(c) == 1 {
		return "{" + c[0] + "}"
	}
	return "{ " + strings.Join(c, " ") + " }"
}

// spreadCode is encode_spread_method: x folded into [min, max] for repeat and reflect.
func spreadCode(min, max float32, c *psCode, spread spreadMethod) {
	if spread == spreadPad {
		return
	}
	length := max - min
	c.real(length)
	c.real(min)
	c.int(2)
	c.ops("index")
	c.int(1)
	c.ops("index", "sub", "dup")
	c.int(3)
	c.ops("index", "div", "floor", "exch")
	c.int(1)
	c.ops("index")
	c.int(4)
	c.ops("index", "mul", "sub", "exch", "cvi", "abs")
	c.int(2)
	c.ops("mod")
	c.int(0)
	c.ops("gt")
	if spread == spreadReflect {
		var inner psCode
		inner.int(2)
		inner.ops("index", "exch", "sub")
		c.ops(inner.block(), "if")
	} else {
		c.ops("pop")
	}
	c.ops("add")
	c.int(3)
	c.int(1)
	c.ops("roll", "pop", "pop")
}

const maxPostScriptStops = 97

// trimStops is trim_stops: at most 97 stops, sampled evenly.
func trimStops(stops []gradStop) []gradStop {
	factor := float32(len(stops)) / maxPostScriptStops
	var out []gradStop
	var cur float32
	for {
		i := int(math.Round(float64(cur)))
		if i >= len(stops) {
			break
		}
		out = append(out, stops[i])
		cur += factor
	}
	return out
}

// stopsCode is encode_postscript_stops.
func stopsCode(stops []gradStop, min, max float32, c *psCode, opacities bool) {
	if len(stops) > maxPostScriptStops {
		stops = trimStops(stops)
	}
	first, last := stops[0], stops[len(stops)-1]
	first.offset, last.offset = 0, 1
	padded := append([]gradStop{first}, stops...)
	padded = append(padded, last)
	stopsImpl(padded, min, max, c, opacities)
}

func twoStopsCode(c0, c1 []float32, min, max float32, c *psCode) {
	if min == max {
		c.ops("pop")
		for _, v := range c0 {
			c.real(v)
		}
		return
	}
	c.real(min)
	c.ops("sub")
	c.real(max)
	c.real(min)
	c.ops("sub", "div")
	for i := range c0 {
		c.int(i)
		c.ops("index")
		c.real(c0[i])
		c.ops("exch")
		c.real(c1[i])
		c.real(c0[i])
		c.ops("sub", "mul", "add")
	}
	c.int(len(c0) + 1)
	c.int(-1)
	c.ops("roll", "pop")
}

// stopsImpl is encode_stops_impl: nested ifelse over the stop pairs.
func stopsImpl(stops []gradStop, min, max float32, c *psCode, opacities bool) {
	if len(stops) == 1 {
		for _, v := range stopComponents(stops[0], opacities) {
			c.real(v)
		}
		return
	}
	length := max - min
	smin := min + float32(length*stops[0].offset)
	smax := min + float32(length*stops[1].offset)
	var ifStops, elseStops psCode
	twoStopsCode(stopComponents(stops[0], opacities), stopComponents(stops[1], opacities), smin, smax, &ifStops)
	stopsImpl(stops[1:], min, max, &elseStops, opacities)
	c.ops("dup")
	c.real(smax)
	c.ops("le", ifStops.block(), elseStops.block(), "ifelse")
}

// postScriptFunction is serialize_linear_postscript: x (y ignored) through the spread method and stops.
func (d *document) postScriptFunction(g *gradientProps, opacities bool) ref {
	var c psCode
	c.ops("pop")
	spreadCode(g.min, g.max, &c, g.spread)
	stopsCode(g.stops, g.min, g.max, &c, opacities)
	r := d.w.alloc()
	rng := "[0 1 0 1 0 1]"
	if opacities {
		rng = "[0 1]"
	}
	dom := g.domain
	d.w.stream(r, "/FunctionType 4 /Domain "+numsArray([]float32{dom.Left(), dom.Right(), dom.Top(), dom.Bottom()})+" /Range "+rng, []byte(c.block()), true)
	return r
}

// shading writes a ShadingFunction: colors in DeviceRGB, or the stop opacities in DeviceGray.
func (d *document) shading(g *gradientProps, opacities bool) ref {
	cs := "/DeviceRGB"
	if opacities {
		cs = "/DeviceGray"
	}
	r := d.w.alloc()
	if g.kind == shadingFunction {
		fn := d.postScriptFunction(g, opacities)
		dom := g.domain
		d.w.obj(r, "<</ShadingType 1 /ColorSpace "+cs+" /Matrix [1 0 0 1 0 0] /AntiAlias false /Function "+fn.String()+
			" /Domain "+numsArray([]float32{dom.Left(), dom.Right(), dom.Top(), dom.Bottom()})+">>")
		return r
	}
	fn := d.axialRadialFunction(g, opacities)
	d.w.obj(r, "<</ShadingType "+strconv.Itoa(g.kind)+" /ColorSpace "+cs+" /AntiAlias false /Function "+fn.String()+
		" /Coords "+numsArray(g.coords)+" /Extend [true true]>>")
	return r
}

// shadingPattern writes a shading pattern with the given matrix.
func (d *document) shadingPattern(g gradientProps, ts skia.Transform) ref {
	sh := d.shading(&g, false)
	r := d.w.alloc()
	d.w.obj(r, "<</Type /Pattern /PatternType 2 /Shading "+sh.String()+" /Matrix ["+nums(ts.SX, ts.KY, ts.KX, ts.SY, ts.TX, ts.TY)+"]>>")
	return r
}

// shadingMask is Mask::new_from_shading: a luminosity mask of the stop opacities, or false when every
// stop is opaque.
func (d *document) shadingMask(g gradientProps, ts skia.Transform, bbox skia.Rect) (ref, bool) {
	if g.opaque() {
		return 0, false
	}
	sh := d.shading(&g, true)
	sub := newSurface(d, newBuilder(d, skia.Identity(), true))
	sub.pushTransform(ts)
	sub.bd().drawShading(sh)
	sub.pop()
	st := sub.finish()
	return d.mask(&mask{stream: st, luminosity: true, bbox: &bbox}), true
}
