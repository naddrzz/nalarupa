package pdf

// The usvg tree walk of krilla-svg 0.8 (lib.rs, group.rs, path.rs, image.rs, text.rs, clip_path.rs,
// mask.rs, filter.rs and util.rs), with its default settings: text embedded, filters rasterised at 4×.

import (
	"errors"
	"fmt"
	"math"

	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// filterScale is SvgSettings::default().filter_scale.
const filterScale = 4

// document is one PDF being written: its objects and the resources shared between pages.
type document struct {
	w          *writer
	pages      []ref
	pagesRef   ref
	extGStates map[string]ref
	images     map[[32]byte]ref
	fonts      map[string]*pdfFont
	fontOrder  []*pdfFont
}

// Encode writes one page per tree, in order, each the tree's size in points (1 px = 1 pt), as render.rs
// `encode_pdf` does with krilla-svg. A tree without a positive finite size is "empty page".
func Encode(trees []*usvg.Tree) ([]byte, error) {
	d := &document{w: newWriter(), extGStates: map[string]ref{}, images: map[[32]byte]ref{}, fonts: map[string]*pdfFont{}}
	catalog := d.w.alloc()
	d.pagesRef = d.w.alloc()
	for _, t := range trees {
		size := t.Size()
		w, h := size.Width(), size.Height()
		if !(w > 0 && h > 0) || math.IsInf(float64(w), 0) || math.IsInf(float64(h), 0) {
			return nil, errors.New("empty page")
		}
		root := newBuilder(d, skia.TransformFromRow(1, 0, 0, -1, 0, h), false)
		s := newSurface(d, root)
		c := &walker{d: d, db: t.FontDB()}
		// SurfaceExt::draw_svg at the tree's own size: its scale transform is the identity.
		s.pushTransform(skia.Identity())
		r, _ := skia.RectFromXYWH(0, 0, w, h)
		s.pushClipPath(rectPath(r), false)
		c.group(t.Root(), s)
		s.pop()
		s.pop()
		st := s.finish()
		content := d.w.alloc()
		d.w.stream(content, "", st.content, true)
		page := d.w.alloc()
		d.w.obj(page, "<</Type /Page /Parent "+d.pagesRef.String()+" /MediaBox [0 0 "+nums(w, h)+"] /Resources "+st.res.dict()+" /Contents "+content.String()+">>")
		d.pages = append(d.pages, page)
	}
	if len(trees) == 0 {
		// krilla's Document::finish writes one empty A4 page when none was started.
		content, page := d.w.alloc(), d.w.alloc()
		d.w.stream(content, "", nil, true)
		d.w.obj(page, "<</Type /Page /Parent "+d.pagesRef.String()+" /MediaBox [0 0 595 842] /Resources "+(&resources{}).dict()+" /Contents "+content.String()+">>")
		d.pages = append(d.pages, page)
	}
	for _, f := range d.fontOrder {
		d.writeFont(f)
	}
	kids := ""
	for i, p := range d.pages {
		if i > 0 {
			kids += " "
		}
		kids += p.String()
	}
	d.w.obj(d.pagesRef, "<</Type /Pages /Kids ["+kids+"] /Count "+itoa(len(d.pages))+">>")
	d.w.obj(catalog, "<</Type /Catalog /Pages "+d.pagesRef.String()+">>")
	return d.w.finish(catalog), nil
}

// walker is krilla-svg's ProcessContext: the fonts of the tree being drawn.
type walker struct {
	d  *document
	db *text.Database
}

// rectPath is RectExt::to_clip_path.
func rectPath(r skia.Rect) *skia.Path {
	b := skia.NewPathBuilder()
	b.MoveTo(r.Left(), r.Top())
	b.LineTo(r.Right(), r.Top())
	b.LineTo(r.Right(), r.Bottom())
	b.LineTo(r.Left(), r.Bottom())
	b.Close()
	return b.Finish()
}

var blendNames = [...]string{
	usvg.BlendModeNormal: "", usvg.BlendModeMultiply: "Multiply", usvg.BlendModeScreen: "Screen",
	usvg.BlendModeOverlay: "Overlay", usvg.BlendModeDarken: "Darken", usvg.BlendModeLighten: "Lighten",
	usvg.BlendModeColorDodge: "ColorDodge", usvg.BlendModeColorBurn: "ColorBurn", usvg.BlendModeHardLight: "HardLight",
	usvg.BlendModeSoftLight: "SoftLight", usvg.BlendModeDifference: "Difference", usvg.BlendModeExclusion: "Exclusion",
	usvg.BlendModeHue: "Hue", usvg.BlendModeSaturation: "Saturation", usvg.BlendModeColor: "Color",
	usvg.BlendModeLuminosity: "Luminosity",
}

// ---- group.rs ----

func (c *walker) group(g *usvg.Group, s *surface) {
	if len(g.Filters()) != 0 {
		c.filter(g, s)
		return
	}
	pops := 1
	s.pushTransform(g.Transform())
	if cp := g.ClipPath(); cp != nil {
		pops += c.clipPath(g, cp, s)
	}
	if m := g.Mask(); m != nil {
		pops += c.mask(m, s)
	}
	s.pushBlendMode(blendNames[g.BlendMode()])
	s.pushOpacity(g.Opacity())
	pops += 2
	// Clip paths alone do not isolate (krilla-svg: no visual difference, and far fewer XObjects).
	if g.Isolate() || g.Opacity() != 1 || g.Mask() != nil || g.BlendMode() != usvg.BlendModeNormal {
		s.pushIsolated()
		pops++
	}
	for _, child := range g.Children() {
		c.node(child, s)
	}
	for ; pops > 0; pops-- {
		s.pop()
	}
}

func (c *walker) node(n usvg.Node, s *surface) {
	switch n := n.(type) {
	case *usvg.Group:
		c.group(n, s)
	case *usvg.Path:
		c.path(n, s)
	case *usvg.Image:
		c.image(n, s)
	case *usvg.Text:
		c.text(n, s)
	}
}

// ---- path.rs ----

func (c *walker) path(p *usvg.Path, s *surface) {
	if !p.IsVisible() {
		return
	}
	if p.PaintOrder() == usvg.PaintOrderFillAndStroke {
		c.drawPath(p, p.Fill(), p.Stroke(), s)
	} else {
		c.drawPath(p, nil, p.Stroke(), s)
		c.drawPath(p, p.Fill(), nil, s)
	}
}

func (c *walker) drawPath(p *usvg.Path, f *usvg.Fill, st *usvg.Stroke, s *surface) {
	var pf *fill
	var ps *stroke
	if f != nil {
		pf = c.fill(f, skia.Identity(), s)
	}
	if st != nil {
		ps = c.stroke(st, skia.Identity(), s)
	}
	// Otherwise krilla fills with black.
	if pf != nil || ps != nil {
		s.fill, s.stroke = pf, ps
		s.drawPath(p.Data())
	}
}

// ---- util.rs ----

func convertStops(stops []usvg.Stop) []gradStop {
	out := make([]gradStop, len(stops))
	for i, st := range stops {
		col := st.Color()
		out[i] = gradStop{offset: st.Offset(), color: [3]uint8{col.Red, col.Green, col.Blue}, opacity: st.Opacity()}
	}
	return out
}

// paint is convert_paint. extra counters the transform of a glyph drawn on its own, so the paint stays
// in the text's space.
func (c *walker) paint(p usvg.Paint, extra skia.Transform, s *surface) paint {
	switch p := p.(type) {
	case usvg.Color:
		if p.Red == p.Green && p.Green == p.Blue {
			return paint{luma: true, rgb: [3]uint8{p.Red, p.Red, p.Red}}
		}
		return paint{rgb: [3]uint8{p.Red, p.Green, p.Blue}}
	case *usvg.LinearGradient:
		return paint{linear: &linearGradient{
			x1: p.X1(), y1: p.Y1(), x2: p.X2(), y2: p.Y2(),
			ts:     extra.PreConcat(p.Transform()),
			spread: spreadMethod(p.SpreadMethod()),
			stops:  convertStops(p.Stops()),
		}}
	case *usvg.RadialGradient:
		// krilla-svg passes no focal radius.
		return paint{radial: &radialGradient{
			fx: p.FX(), fy: p.FY(), fr: 0, cx: p.CX(), cy: p.CY(), cr: p.R(),
			ts:     extra.PreConcat(p.Transform()),
			spread: spreadMethod(p.SpreadMethod()),
			stops:  convertStops(p.Stops()),
		}}
	case *usvg.Pattern:
		sub := s.subSurface()
		c.group(p.Root(), sub)
		rect := p.Rect()
		return paint{pattern: &tilingPaint{
			stream: sub.finish(),
			ts:     extra.PreConcat(p.Transform()).PreConcat(skia.TransformFromTranslate(rect.X(), rect.Y())),
			w:      rect.Width(),
			h:      rect.Height(),
		}}
	}
	return paint{luma: true}
}

func (c *walker) fill(f *usvg.Fill, extra skia.Transform, s *surface) *fill {
	return &fill{paint: c.paint(f.Paint(), extra, s), opacity: f.Opacity(), evenOdd: f.Rule() == usvg.FillRuleEvenOdd}
}

func (c *walker) stroke(st *usvg.Stroke, extra skia.Transform, s *surface) *stroke {
	out := &stroke{
		paint:      c.paint(st.Paint(), extra, s),
		width:      st.Width(),
		miterLimit: st.Miterlimit().Get(),
		opacity:    st.Opacity(),
	}
	switch st.Linecap() {
	case usvg.LineCapRound:
		out.cap = skia.LineCapRound
	case usvg.LineCapSquare:
		out.cap = skia.LineCapSquare
	}
	switch st.Linejoin() {
	case usvg.LineJoinRound:
		out.join = skia.LineJoinRound
	case usvg.LineJoinBevel:
		out.join = skia.LineJoinBevel
	}
	if d := st.Dasharray(); d != nil {
		out.dash = append([]float32{}, d...)
		out.dashOffset = st.Dashoffset()
	}
	return out
}

// ---- image.rs ----

func (c *walker) image(img *usvg.Image, s *surface) {
	if !img.IsVisible() {
		return
	}
	size := img.Size()
	if svg, ok := img.Kind().(usvg.ImageKindSVG); ok {
		r, ok := skia.RectFromXYWH(0, 0, svg.Tree.Size().Width(), svg.Tree.Size().Height())
		if !ok {
			return
		}
		s.pushClipPath(rectPath(r), false)
		c.group(svg.Tree.Root(), s)
		s.pop()
		return
	}
	decoded, ok := decodeImage(img.Kind())
	if !ok {
		return // a damaged image is left out
	}
	s.drawImage(decoded, size.Width(), size.Height())
}

// ---- mask.rs ----

func (c *walker) mask(m *usvg.Mask, s *surface) int {
	sub := s.subSurface()
	pops := 0
	if inner := m.Mask(); inner != nil {
		pops += c.mask(inner, sub)
	}
	sub.pushClipPath(rectPath(m.Rect().ToRect()), false)
	pops++
	c.group(m.Root(), sub)
	for ; pops > 0; pops-- {
		sub.pop()
	}
	s.pushMask(&mask{stream: sub.finish(), luminosity: m.Kind() == usvg.MaskTypeLuminance})
	return 1
}

// ---- clip_path.rs ----

// clipPath pushes a clip path: native PDF clipping when the clip path is simple enough, else an alpha
// soft mask of its content.
func (c *walker) clipPath(g *usvg.Group, cp *usvg.ClipPath, s *surface) int {
	rules := clipRules(cp.Root(), nil)
	allNonZero, allEvenOdd := true, true
	for _, r := range rules {
		allNonZero = allNonZero && r == usvg.FillRuleNonZero
		allEvenOdd = allEvenOdd && r == usvg.FillRuleEvenOdd
	}
	// Overlapping even-odd shapes clip differently in PDF, so at most one.
	if isSimpleClip(cp.Root()) && (allNonZero || (allEvenOdd && len(rules) == 1)) {
		rule := usvg.FillRuleNonZero
		if len(rules) > 0 {
			rule = rules[0]
		}
		pops := 0
		for _, p := range createClipPaths(cp, rule == usvg.FillRuleEvenOdd, nil) {
			s.pushClipPath(p, rule == usvg.FillRuleEvenOdd)
			pops++
		}
		return pops
	}
	return c.complexClip(g, cp, s)
}

// createClipPaths is create_clip_path: one path per clip path in the chain, outermost first.
func createClipPaths(cp *usvg.ClipPath, evenOdd bool, out []*skia.Path) []*skia.Path {
	if inner := cp.ClipPath(); inner != nil {
		out = createClipPaths(inner, evenOdd, out)
	}
	// A dummy move, so a clip path with only hidden children still hides everything.
	b := skia.NewPathBuilder()
	b.MoveTo(0, 0)
	extendClipSegments(cp.Root(), cp.Transform(), b)
	p := b.Finish()
	if p == nil {
		e := skia.NewPathBuilder()
		e.MoveTo(0, 0)
		e.LineTo(0, 0)
		p = e.Finish()
	}
	return append(out, p)
}

func extendClipSegments(g *usvg.Group, ts skia.Transform, b *skia.PathBuilder) {
	for _, child := range g.Children() {
		switch n := child.(type) {
		case *usvg.Path:
			if !n.IsVisible() {
				continue
			}
			it := n.Data().Segments()
			for {
				seg, ok := it.Next()
				if !ok {
					break
				}
				pts := seg.Pts
				switch seg.Verb {
				case skia.VerbMove:
					ts.MapPoints(pts[:1])
					b.MoveTo(pts[0].X, pts[0].Y)
				case skia.VerbLine:
					ts.MapPoints(pts[:1])
					b.LineTo(pts[0].X, pts[0].Y)
				case skia.VerbQuad:
					ts.MapPoints(pts[:2])
					b.QuadTo(pts[0].X, pts[0].Y, pts[1].X, pts[1].Y)
				case skia.VerbCubic:
					ts.MapPoints(pts[:3])
					b.CubicTo(pts[0].X, pts[0].Y, pts[1].X, pts[1].Y, pts[2].X, pts[2].Y)
				case skia.VerbClose:
					b.Close()
				}
			}
		case *usvg.Group:
			extendClipSegments(n, ts.PreConcat(n.Transform()), b)
		case *usvg.Text:
			// Text in a clip path is its flattened outline.
			if fl := n.Flattened(); fl != nil {
				extendClipSegments(fl, ts, b)
			}
		}
	}
}

// isSimpleClip: a nested clip path is only expressible on the clip path itself, not on a child.
func isSimpleClip(g *usvg.Group) bool {
	for _, child := range g.Children() {
		if cg, ok := child.(*usvg.Group); ok && (cg.ClipPath() != nil || !isSimpleClip(cg)) {
			return false
		}
	}
	return true
}

func clipRules(g *usvg.Group, rules []usvg.FillRule) []usvg.FillRule {
	for _, child := range g.Children() {
		switch n := child.(type) {
		case *usvg.Path:
			if f := n.Fill(); f != nil {
				rules = append(rules, f.Rule())
			}
		case *usvg.Text:
			if fl := n.Flattened(); fl != nil {
				rules = clipRules(fl, rules)
			}
		case *usvg.Group:
			rules = clipRules(n, rules)
		}
	}
	return rules
}

// complexClip is render_complex: the clip path drawn into an alpha mask.
func (c *walker) complexClip(parent *usvg.Group, cp *usvg.ClipPath, s *surface) int {
	sub := s.subSurface()
	pops := 0
	if inner := cp.ClipPath(); inner != nil {
		pops += c.clipPath(parent, inner, sub)
	}
	sub.pushTransform(cp.Transform())
	pops++
	c.group(cp.Root(), sub)
	for ; pops > 0; pops-- {
		sub.pop()
	}
	s.pushMask(&mask{stream: sub.finish()})
	return 1
}

// ---- text.rs ----

func (c *walker) text(t *usvg.Text, s *surface) {
	spans := t.Layouted()
	for si := range spans {
		span := &spans[si]
		if !span.IsVisible() {
			continue
		}
		if p := span.Overline(); p != nil {
			c.path(p, s)
		}
		if p := span.Underline(); p != nil {
			c.path(p, s)
		}
		glyphs := span.PositionedGlyphs()
		for gi := range glyphs {
			g := &glyphs[gi]
			f, ok := c.retrieveFont(span, g.Font)
			if !ok {
				continue // a font that cannot be loaded drops its glyphs
			}
			fs := span.FontSize()
			// The glyph transform includes font size / upem; the font size is the text's own.
			ts := g.Transform().PreConcat(skia.TransformFromScale(f.upem/fs, f.upem/fs))
			inv, ok := ts.Invert()
			if !ok {
				continue
			}
			// The inverse keeps each glyph's paint in the text's space.
			var pf *fill
			var ps *stroke
			if sf := span.Fill(); sf != nil {
				pf = c.fill(sf, inv, s)
			}
			if ss := span.Stroke(); ss != nil {
				ps = c.stroke(ss, inv, s)
			}
			draw := func(fl *fill, st *stroke, embed bool) {
				s.fill, s.stroke = fl, st
				s.drawGlyph(f, g.ID, g.Text, fs, !embed)
			}
			s.pushTransform(ts)
			switch {
			case pf == nil && ps == nil:
				// An invisible glyph is drawn in RGB black with zero opacity, so its text remains.
				draw(&fill{opacity: 0}, nil, true)
			case span.PaintOrder() == usvg.PaintOrderFillAndStroke || pf == nil || ps == nil:
				draw(pf, ps, true)
			default:
				// Stroke below the fill: the stroke is outlined so the text is embedded once.
				draw(nil, ps, false)
				draw(pf, nil, true)
			}
			s.pop()
		}
		if p := span.LineThrough(); p != nil {
			c.path(p, s)
		}
	}
}

var tagOpsz = [4]byte{'o', 'p', 's', 'z'}

// retrieveFont is Fonts::retrieve with resolve_variations: the span's variations on axes the face has,
// plus opsz at the font size under font-optical-sizing: auto when the face has it and it is not set.
func (c *walker) retrieveFont(span *usvg.Span, face text.FaceID) (*pdfFont, bool) {
	axes := c.db.Axes(face)
	supported := func(t [4]byte) bool {
		for _, a := range axes {
			if a == t {
				return true
			}
		}
		return false
	}
	var vars []text.Variation
	hasOpsz := false
	for _, v := range span.Variations() {
		if supported(v.Tag) {
			vars = append(vars, text.Variation{Tag: v.Tag, Value: v.Value})
			hasOpsz = hasOpsz || v.Tag == tagOpsz
		}
	}
	if span.FontOpticalSizing() == usvg.FontOpticalSizingAuto && supported(tagOpsz) && !hasOpsz {
		vars = append(vars, text.Variation{Tag: tagOpsz, Value: span.FontSize()})
	}
	return c.d.font(c.db, face, vars)
}

// ---- filter.rs ----

// filter rasterises a filtered group with resvg and draws the image: at 4× the size the group has on
// the page (as far as the current stream's transform tells), at most 5000 pixels a side.
func (c *walker) filter(g *usvg.Group, s *surface) {
	layer := g.LayerBoundingBox()
	layerBBox, ok := layer.Transform(g.Transform())
	if !ok {
		return
	}
	actual, ok := layer.Transform(s.curTransform())
	if !ok {
		return
	}
	if actual, ok = actual.Transform(g.Transform()); !ok {
		return
	}
	scale := max(actual.Width()/layerBBox.Width(), actual.Height()/layerBBox.Height()) * filterScale
	const pixelThreshold = 5000
	maxScale := min(pixelThreshold/layerBBox.Width(), pixelThreshold/layerBBox.Height())
	scale = min(scale, maxScale)
	pw, ph := layerBBox.Width()*scale, layerBBox.Height()*scale
	if !(pw > 0 && ph > 0) || math.IsInf(float64(pw), 0) || math.IsInf(float64(ph), 0) {
		return
	}
	width, height := uint32(math.Round(float64(pw))), uint32(math.Round(float64(ph)))
	pm, ok := skia.NewPixmap(width, height)
	if !ok {
		return
	}
	abs, _ := g.AbsLayerBoundingBox()
	// resvg's render_node moves by the absolute layer box; the last translation undoes that.
	initial := skia.TransformFromScale(scale, scale).
		PreConcat(skia.TransformFromTranslate(-layerBBox.X(), -layerBBox.Y())).
		PreConcat(skia.TransformFromTranslate(abs.X(), abs.Y()))
	resvg.RenderNode(g, initial, pm.AsMut())
	img := fromRGBA8(pm.TakeDemultiplied(), int(width), int(height), true)
	img.key = imageKey(fmt.Sprintf("filter %dx%d", width, height), append(append([]byte(nil), img.data...), img.alpha...))
	s.pushTransform(skia.TransformFromTranslate(layerBBox.X(), layerBBox.Y()))
	s.drawImage(img, layerBBox.Width(), layerBBox.Height())
	s.pop()
}
