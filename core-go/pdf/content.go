package pdf

// Content streams: krilla's ContentBuilder (one stream with its resources, the simulated graphics state
// stack and the bounding box of what was drawn) and Surface (the push/pop interface krilla-svg draws
// through, where opacity, isolation and masks open sub-streams that become form XObjects).

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// ---- paint (krilla::paint) ----

type spreadMethod uint8

const (
	spreadPad spreadMethod = iota
	spreadReflect
	spreadRepeat
)

// gradStop is a gradient stop; krilla-svg gives every stop an RGB color.
type gradStop struct {
	offset  float32
	color   [3]uint8
	opacity float32
}

type linearGradient struct {
	x1, y1, x2, y2 float32
	ts             skia.Transform
	spread         spreadMethod
	stops          []gradStop
}

type radialGradient struct {
	fx, fy, fr, cx, cy, cr float32
	ts                     skia.Transform
	spread                 spreadMethod
	stops                  []gradStop
}

type tilingPaint struct {
	stream *stream
	ts     skia.Transform
	w, h   float32
}

// paint is one of a solid color (gray when luma), a gradient or a tiling pattern.
type paint struct {
	luma    bool
	rgb     [3]uint8
	linear  *linearGradient
	radial  *radialGradient
	pattern *tilingPaint
}

func (p *paint) solid() bool { return p.linear == nil && p.radial == nil && p.pattern == nil }

// translucentStops is check_paint of has_complex_fill_or_stroke: a gradient with a stop opacity below 1.
func (p *paint) translucentStops() bool {
	var stops []gradStop
	switch {
	case p.linear != nil:
		stops = p.linear.stops
	case p.radial != nil:
		stops = p.radial.stops
	}
	for _, s := range stops {
		if s.opacity != 1 {
			return true
		}
	}
	return false
}

type fill struct {
	paint   paint
	opacity float32
	evenOdd bool
}

// defaultFill is krilla's Fill::default(): opaque gray black, nonzero.
func defaultFill() *fill { return &fill{paint: paint{luma: true}, opacity: 1} }

type stroke struct {
	paint      paint
	width      float32
	miterLimit float32
	cap        skia.LineCap
	join       skia.LineJoin // never MiterClip: krilla-svg maps it to miter
	opacity    float32
	dash       []float32 // nil for none
	dashOffset float32
}

func (s *stroke) toSkia() skia.Stroke {
	st := skia.Stroke{Width: s.width, MiterLimit: s.miterLimit, LineCap: s.cap, LineJoin: s.join}
	if s.dash != nil {
		st.Dash = skia.NewStrokeDash(append([]float32(nil), s.dash...), s.dashOffset)
	}
	return st
}

// ---- graphics state (krilla::graphics::graphics_state) ----

type extGState struct {
	hasCA, hasca bool
	CA, ca       float32
	bm           string // a PDF blend mode name, "" for none
	mask         ref    // a soft mask dictionary, 0 for none
}

func (e *extGState) empty() bool { return !e.hasCA && !e.hasca && e.bm == "" && e.mask == 0 }

func (e *extGState) combine(o extGState) {
	if o.hasCA {
		e.hasCA, e.CA = true, o.CA
	}
	if o.hasca {
		e.hasca, e.ca = true, o.ca
	}
	if o.bm != "" {
		e.bm = o.bm
	}
	if o.mask != 0 {
		e.mask = o.mask
	}
}

func (e *extGState) dict() string {
	var b strings.Builder
	b.WriteString("<</Type /ExtGState")
	if e.hasca {
		b.WriteString(" /ca " + num(e.ca))
	}
	if e.hasCA {
		b.WriteString(" /CA " + num(e.CA))
	}
	if e.bm != "" {
		b.WriteString(" /BM /" + e.bm)
	}
	if e.mask != 0 {
		b.WriteString(" /SMask " + e.mask.String())
	}
	b.WriteString(">>")
	return b.String()
}

type gstate struct {
	ts  skia.Transform
	ext extGState
}

// ---- resources ----

const (
	resExtGState = iota
	resPattern
	resShading
	resXObject
	resFont
	resKinds
)

var resKeys = [resKinds]string{"ExtGState", "Pattern", "Shading", "XObject", "Font"}
var resPrefix = [resKinds]string{"g", "p", "s", "x", "f"}

// resources is a content stream's resource dictionary: one name per referenced object and kind.
type resources struct {
	names [resKinds]map[ref]string
}

func (r *resources) name(kind int, x ref) string {
	if r.names[kind] == nil {
		r.names[kind] = map[ref]string{}
	}
	n, ok := r.names[kind][x]
	if !ok {
		n = fmt.Sprintf("%s%d", resPrefix[kind], len(r.names[kind]))
		r.names[kind][x] = n
	}
	return "/" + n
}

// dict is the resource dictionary, written direct (with krilla's ProcSet entry).
func (r *resources) dict() string {
	var b strings.Builder
	b.WriteString("<</ProcSet [/PDF /Text /ImageC /ImageB]")
	for kind := 0; kind < resKinds; kind++ {
		if len(r.names[kind]) == 0 {
			continue
		}
		type entry struct {
			name string
			ref  ref
		}
		var es []entry
		for x, n := range r.names[kind] {
			es = append(es, entry{n, x})
		}
		sort.Slice(es, func(i, j int) bool { return es[i].ref < es[j].ref })
		b.WriteString(" /" + resKeys[kind] + " <<")
		for _, e := range es {
			b.WriteString(" /" + e.name + " " + e.ref.String())
		}
		b.WriteString(" >>")
	}
	b.WriteString(">>")
	return b.String()
}

// stream is a finished content stream: krilla's Stream.
type stream struct {
	content  []byte
	bbox     skia.Rect
	res      *resources
	usesMask bool
}

// ---- ContentBuilder ----

type builder struct {
	d             *document
	c             bytes.Buffer
	res           resources
	root          skia.Transform
	states        []gstate
	bbox          skia.Rect
	hasBBox       bool
	bboxImportant bool
	usesMask      bool
}

func newBuilder(d *document, root skia.Transform, bboxImportant bool) *builder {
	return &builder{d: d, root: root, bboxImportant: bboxImportant, states: []gstate{{ts: skia.Identity()}}}
}

func (b *builder) finish() *stream {
	bbox := b.bbox
	if !b.hasBBox {
		bbox, _ = skia.RectFromXYWH(0, 0, 1, 1)
	}
	return &stream{content: b.c.Bytes(), bbox: bbox, res: &b.res, usesMask: b.usesMask}
}

func (b *builder) op(s string) {
	b.c.WriteString(s)
	b.c.WriteByte('\n')
}

func (b *builder) cur() *gstate                     { return &b.states[len(b.states)-1] }
func (b *builder) curTransform() skia.Transform     { return b.cur().ts }
func (b *builder) curWithRoot() skia.Transform      { return b.root.PreConcat(b.cur().ts) }
func (b *builder) concatTransform(t skia.Transform) { b.cur().ts = b.cur().ts.PreConcat(t) }
func (b *builder) saveState()                       { b.states = append(b.states, *b.cur()) }
func (b *builder) restoreState()                    { b.states = b.states[:len(b.states)-1] }

func (b *builder) setBlendMode(bm string) {
	if bm != "" {
		b.cur().ext.combine(extGState{bm: bm})
	}
}

func (b *builder) setFillOpacity(a float32) {
	if a != 1 {
		b.cur().ext.combine(extGState{hasca: true, ca: a})
	}
}

func (b *builder) setStrokeOpacity(a float32) {
	if a != 1 {
		b.cur().ext.combine(extGState{hasCA: true, CA: a})
	}
}

func (b *builder) expandBBox(r skia.Rect) {
	t, ok := r.Transform(b.cur().ts)
	if !ok {
		return
	}
	if !b.hasBBox {
		b.bbox, b.hasBBox = t, true
		return
	}
	if u, ok := skia.RectFromLTRB(min(b.bbox.Left(), t.Left()), min(b.bbox.Top(), t.Top()),
		max(b.bbox.Right(), t.Right()), max(b.bbox.Bottom(), t.Bottom())); ok {
		b.bbox = u
	}
}

func transformOp(t skia.Transform) string {
	return nums(t.SX, t.KY, t.KX, t.SY, t.TX, t.TY) + " cm"
}

// applyIsolatedOp is apply_isolated_op: the operation inside q/Q with the full transform and the
// graphics state written once.
func (b *builder) applyIsolatedOp(prep, op func()) {
	b.saveState()
	b.op("q")
	if prep != nil {
		prep()
	}
	if t := b.curWithRoot(); !t.IsIdentity() {
		b.op(transformOp(t))
	}
	if st := b.cur().ext; !st.empty() {
		b.op(b.res.name(resExtGState, b.d.extGState(st)) + " gs")
	}
	op()
	b.op("Q")
	b.restoreState()
}

// pathData is content_draw_path: quads become cubics as resvg computes them.
func (b *builder) pathData(p *skia.Path) {
	calc := func(n1, n2 float32) float32 { return (n1 + float32(n2*2)) / 3 }
	var prev skia.Point
	it := p.Segments()
	for {
		seg, ok := it.Next()
		if !ok {
			break
		}
		switch seg.Verb {
		case skia.VerbMove:
			b.op(nums(seg.Pts[0].X, seg.Pts[0].Y) + " m")
			prev = seg.Pts[0]
		case skia.VerbLine:
			b.op(nums(seg.Pts[0].X, seg.Pts[0].Y) + " l")
			prev = seg.Pts[0]
		case skia.VerbQuad:
			p1, p2 := seg.Pts[0], seg.Pts[1]
			b.op(nums(calc(prev.X, p1.X), calc(prev.Y, p1.Y), calc(p2.X, p1.X), calc(p2.Y, p1.Y), p2.X, p2.Y) + " c")
			prev = p2
		case skia.VerbCubic:
			b.op(nums(seg.Pts[0].X, seg.Pts[0].Y, seg.Pts[1].X, seg.Pts[1].Y, seg.Pts[2].X, seg.Pts[2].Y) + " c")
			prev = seg.Pts[2]
		case skia.VerbClose:
			b.op("h")
		}
	}
}

// drawPath is ContentBuilder::draw_path.
func (b *builder) drawPath(p *skia.Path, f *fill, s *stroke) {
	if f == nil && s == nil {
		return
	}
	bounds := p.Bounds()
	if bounds.Width() == 0 && bounds.Height() == 0 {
		return // zero-size geometry
	}
	verbs := p.Verbs()
	isLine := len(verbs) == 2 && verbs[0] == skia.VerbMove && verbs[1] == skia.VerbLine
	if bounds.Width() == 0 || bounds.Height() == 0 || isLine {
		// Some viewers slightly fill zero-sized lines.
		if s == nil {
			return
		}
		f = nil
	}
	strokeBBox := func() skia.Rect {
		if b.bboxImportant || !s.paint.solid() {
			st := s.toSkia()
			if sp := p.Stroke(&st, 1); sp != nil {
				if r, ok := sp.ComputeTightBounds(); ok {
					return r
				}
			}
		}
		return bounds
	}
	fillPrep := func() {
		b.expandBBox(bounds)
		// Viewers apply fill/stroke opacity to patterns inconsistently: the pattern carries it.
		if f.paint.pattern == nil {
			b.setFillOpacity(f.opacity)
		}
	}
	strokePrep := func(sb skia.Rect) {
		b.expandBBox(sb)
		if s.paint.pattern == nil {
			b.setStrokeOpacity(s.opacity)
		}
	}
	fillOp := "f"
	if f != nil && f.evenOdd {
		fillOp = "f*"
	}
	switch {
	case f != nil && s == nil:
		b.applyIsolatedOp(fillPrep, func() {
			b.setFillProps(bounds, f)
			b.pathData(p)
			b.op(fillOp)
		})
	case f == nil:
		sb := strokeBBox()
		b.applyIsolatedOp(func() { strokePrep(sb) }, func() {
			b.setStrokeProps(sb, s)
			b.pathData(p)
			b.op("S")
		})
	default:
		sb := strokeBBox()
		b.applyIsolatedOp(func() { fillPrep(); strokePrep(sb) }, func() {
			b.setFillProps(bounds, f)
			b.setStrokeProps(sb, s)
			b.pathData(p)
			if f.evenOdd {
				b.op("B*")
			} else {
				b.op("B")
			}
		})
	}
}

func (b *builder) pushClipPath(p *skia.Path, evenOdd bool) {
	b.op("q")
	if tp := p.Transform(b.curWithRoot()); tp != nil {
		b.pathData(tp)
	}
	if evenOdd {
		b.op("W* n")
	} else {
		b.op("W n")
	}
}

func (b *builder) popClipPath() { b.op("Q") }

// setPaintProps is content_set_fill_stroke_properties for a fill (stroking false) or a stroke.
func (b *builder) setPaintProps(bounds skia.Rect, p *paint, opacity float32, stroking bool) {
	solidOp, grayOp, csOp, scnOp := "rg", "g", "cs", "scn"
	if stroking {
		solidOp, grayOp, csOp, scnOp = "RG", "G", "CS", "SCN"
	}
	setRGB := func(c [3]uint8) {
		b.op(nums(float32(c[0])/255, float32(c[1])/255, float32(c[2])/255) + " " + solidOp)
	}
	setPattern := func(r ref) {
		b.op("/Pattern " + csOp + " " + b.res.name(resPattern, r) + " " + scnOp)
	}
	writeGradient := func(g gradientProps, ts skia.Transform) {
		if s, ok := g.singleStop(); ok {
			// One stop is a solid fill.
			b.setFillOpacity(s.opacity)
			setRGB(s.color)
			return
		}
		mask, hasMask := b.d.shadingMask(g, ts, bounds)
		pat := b.d.shadingPattern(g, b.curWithRoot().PreConcat(ts))
		if hasMask {
			b.usesMask = true
			b.op(b.res.name(resExtGState, b.d.extGState(extGState{mask: mask})) + " gs")
		}
		setPattern(pat)
	}
	switch {
	case p.linear != nil:
		writeGradient(linearProps(p.linear, bounds))
	case p.radial != nil:
		writeGradient(radialProps(p.radial))
	case p.pattern != nil:
		ts := b.curWithRoot().PreConcat(p.pattern.ts)
		setPattern(b.d.tilingPattern(p.pattern.stream, ts, opacity, p.pattern.w, p.pattern.h))
	case p.luma:
		b.op(num(float32(p.rgb[0])/255) + " " + grayOp)
	default:
		setRGB(p.rgb)
	}
}

func (b *builder) setFillProps(bounds skia.Rect, f *fill) {
	b.setPaintProps(bounds, &f.paint, f.opacity, false)
}

func (b *builder) setStrokeProps(bounds skia.Rect, s *stroke) {
	b.setPaintProps(bounds, &s.paint, s.opacity, true)
	// Only values that differ from PDF's defaults.
	if s.width != 1 {
		b.op(num(s.width) + " w")
	}
	if s.miterLimit != 10 {
		b.op(num(s.miterLimit) + " M")
	}
	switch s.cap {
	case skia.LineCapRound:
		b.op("1 J")
	case skia.LineCapSquare:
		b.op("2 J")
	}
	switch s.join {
	case skia.LineJoinRound:
		b.op("1 j")
	case skia.LineJoinBevel:
		b.op("2 j")
	}
	if s.dash != nil {
		b.op("[" + nums(s.dash...) + "] " + num(s.dashOffset) + " d")
	}
}

// drawXObject is draw_xobject: the form drawn with state combined into the graphics state.
func (b *builder) drawXObject(x *xobject, state extGState) {
	bbox := x.bbox()
	b.applyIsolatedOp(func() {
		b.cur().ext.combine(state)
		b.expandBBox(bbox)
	}, func() {
		b.op(b.res.name(resXObject, b.d.xobject(x)) + " Do")
	})
}

func (b *builder) drawMasked(m *mask, s *stream) {
	state := extGState{mask: b.d.mask(m)}
	b.usesMask = true
	b.drawXObject(newXObject(s, false, true, nil), state)
}

func (b *builder) drawOpacified(opacity float32, s *stream) {
	state := extGState{hasCA: true, CA: opacity, hasca: true, ca: opacity}
	b.drawXObject(newXObject(s, true, false, nil), state)
}

func (b *builder) drawIsolated(s *stream) {
	b.drawXObject(newXObject(s, true, false, nil), extGState{})
}

// drawImage draws an image scaled from the unit square to size.
func (b *builder) drawImage(img *image, w, h float32) {
	b.applyIsolatedOp(func() {
		b.concatTransform(skia.TransformFromRow(w, 0, 0, -h, 0, h))
		unit, _ := skia.RectFromXYWH(0, 0, 1, 1)
		b.expandBBox(unit)
	}, func() {
		b.op(b.res.name(resXObject, b.d.image(img)) + " Do")
	})
}

func (b *builder) drawShading(sh ref) {
	b.applyIsolatedOp(nil, func() {
		b.op(b.res.name(resShading, sh) + " sh")
	})
}

// ---- Surface ----

type pushKind uint8

const (
	pushTransform pushKind = iota
	pushOpacity
	pushClip
	pushBlend
	pushMask
	pushIsolated
)

type pushed struct {
	kind    pushKind
	opacity float32
	mask    *mask
}

// surface is krilla's Surface: the current fill and stroke, and a root builder with the sub-builders
// that pushed opacities, isolations and masks opened.
type surface struct {
	d      *document
	root   *builder
	subs   []*builder
	pushes []pushed
	fill   *fill
	stroke *stroke
}

func newSurface(d *document, root *builder) *surface { return &surface{d: d, root: root} }

// subSurface is StreamBuilder::surface: a surface on a fresh stream whose box matters.
func (s *surface) subSurface() *surface {
	return newSurface(s.d, newBuilder(s.d, skia.Identity(), true))
}

func (s *surface) bd() *builder {
	if n := len(s.subs); n > 0 {
		return s.subs[n-1]
	}
	return s.root
}

// finish ends the surface; every push must have been popped.
func (s *surface) finish() *stream {
	if len(s.pushes) != 0 || len(s.subs) != 0 {
		panic("pdf: surface finished with pushes left")
	}
	return s.root.finish()
}

func (s *surface) curTransform() skia.Transform { return s.bd().curTransform() }

func (s *surface) pushTransform(t skia.Transform) {
	s.pushes = append(s.pushes, pushed{kind: pushTransform})
	s.bd().saveState()
	s.bd().concatTransform(t)
}

func (s *surface) pushBlendMode(bm string) {
	s.pushes = append(s.pushes, pushed{kind: pushBlend})
	s.bd().saveState()
	s.bd().setBlendMode(bm)
}

func (s *surface) pushClipPath(p *skia.Path, evenOdd bool) {
	s.pushes = append(s.pushes, pushed{kind: pushClip})
	s.bd().pushClipPath(p, evenOdd)
}

func (s *surface) pushMask(m *mask) {
	s.pushes = append(s.pushes, pushed{kind: pushMask, mask: m})
	s.subs = append(s.subs, newBuilder(s.d, skia.Identity(), true))
}

func (s *surface) pushOpacity(o float32) {
	s.pushes = append(s.pushes, pushed{kind: pushOpacity, opacity: o})
	if o != 1 {
		s.subs = append(s.subs, newBuilder(s.d, skia.Identity(), true))
	}
}

func (s *surface) pushIsolated() {
	s.pushes = append(s.pushes, pushed{kind: pushIsolated})
	s.subs = append(s.subs, newBuilder(s.d, skia.Identity(), true))
}

func (s *surface) popSub() *stream {
	sub := s.subs[len(s.subs)-1]
	s.subs = s.subs[:len(s.subs)-1]
	return sub.finish()
}

func (s *surface) pop() {
	p := s.pushes[len(s.pushes)-1]
	s.pushes = s.pushes[:len(s.pushes)-1]
	switch p.kind {
	case pushTransform, pushBlend:
		s.bd().restoreState()
	case pushOpacity:
		if p.opacity != 1 {
			st := s.popSub()
			s.bd().drawOpacified(p.opacity, st)
		}
	case pushClip:
		s.bd().popClipPath()
	case pushMask:
		st := s.popSub()
		s.bd().drawMasked(p.mask, st)
	case pushIsolated:
		st := s.popSub()
		s.bd().drawIsolated(st)
	}
}

// complexFillOrStroke is has_complex_fill_or_stroke: fill and stroke must be drawn apart when the stroke
// is translucent or a gradient has translucent stops.
func (s *surface) complexFillOrStroke() bool {
	complexStroke := s.stroke != nil && (s.stroke.opacity != 1 || s.stroke.paint.translucentStops())
	complexFill := s.fill != nil && s.fill.paint.translucentStops()
	return complexFill || complexStroke
}

func (s *surface) drawPath(p *skia.Path) {
	switch {
	case s.fill == nil && s.stroke == nil:
		s.bd().drawPath(p, defaultFill(), nil) // black by default
	case s.complexFillOrStroke():
		s.bd().drawPath(p, s.fill, nil)
		s.bd().drawPath(p, nil, s.stroke)
	default:
		s.bd().drawPath(p, s.fill, s.stroke)
	}
}

func (s *surface) drawImage(img *image, w, h float32) { s.bd().drawImage(img, w, h) }

func (s *surface) drawOpacifiedStream(o float32, st *stream) { s.bd().drawOpacified(o, st) }
