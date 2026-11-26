package render

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// The dirty-rect logic grows edits by the background blurs of every layer. Rust reads them off the vello
// display list (raster.rs `Scene::new`), which it builds even when painting with resvg. This is that
// builder's control flow without the display list: a background blur counts when the builder records it,
// i.e. when no enclosing group falls back to resvg before reaching it and its mask content needs no
// fallback.

// backdrop is a background blur's doc-space box and how far around it content feeds its blur.
type backdrop struct {
	box    Rect
	reach  float64
	native bool
}

type sceneScan struct {
	out []backdrop
	// inside a pattern or mask: nothing can fall back there (a fallback fails the enclosing item).
	inside bool
	failed bool
	// alpha: only the alpha of what is built matters (shadow sources, knockouts, blur masks).
	alpha bool
}

// sceneBackdrops is `Scene::new(tree).backdrops()`.
func sceneBackdrops(t *usvg.Tree) []backdrop {
	s := &sceneScan{}
	s.children(t.Root(), t.Root().AbsTransform())
	return s.out
}

func (s *sceneScan) children(g *usvg.Group, abs skia.Transform) {
	for _, n := range g.Children() {
		s.node(n, abs)
	}
}

func (s *sceneScan) node(n usvg.Node, abs skia.Transform) {
	switch v := n.(type) {
	case *usvg.Group:
		s.group(v, abs)
	case *usvg.Text:
		s.group(v.Flattened(), abs)
	case *usvg.Path:
		s.path(v, abs)
	case *usvg.Image:
		s.image(v)
	}
}

// maskShapes visits mask content (alpha only, nothing in it can fall back).
func (s *sceneScan) maskShapes(nodes []usvg.Node, abs skia.Transform) {
	inside, alpha := s.inside, s.alpha
	s.inside, s.alpha = true, true
	for _, n := range nodes {
		s.node(n, abs)
	}
	s.inside, s.alpha = inside, alpha
}

func (s *sceneScan) fallback() {
	if s.inside {
		s.failed = true
	}
}

type fxKind uint8

const (
	fxNone fxKind = iota
	fxBlur
	fxDrop
	fxInner
	fxBlack
)

func (s *sceneScan) group(g *usvg.Group, parent skia.Transform) {
	abs := parent.PreConcat(g.Transform())
	if !g.ShouldIsolate() {
		s.children(g, abs)
		return
	}
	if isShadow(g, abs) {
		return
	}
	if rest, ok := strings.CutPrefix(g.ID(), "nrbb:"); ok {
		if sigma, err := strconv.ParseFloat(rest[strings.LastIndexByte(rest, ':')+1:], 64); err == nil {
			s.backdrop(g, abs, sigma)
			return
		}
	}
	fx := fxNone
	switch fs := g.Filters(); len(fs) {
	case 0:
	case 1:
		fx = effectOf(fs[0])
		if fx == fxBlack && s.alpha {
			fx = fxNone
		} else if fx == fxBlack || fx == fxNone {
			s.fallback()
			return
		}
	default:
		s.fallback()
		return
	}
	var shapes []usvg.Node
	knock := false
	if m := g.Mask(); m != nil {
		var ok bool
		shapes, ok = knockout(m)
		if !ok || g.ClipPath() != nil {
			s.fallback()
			return
		}
		knock = true
	}
	if c := g.ClipPath(); c != nil && !simpleClip(c) {
		s.fallback()
		return
	}
	failed := s.failed
	s.failed = false
	switch fx {
	case fxNone, fxBlack, fxBlur:
		s.children(g, abs)
	case fxDrop:
		alpha := s.alpha
		s.alpha = true
		s.children(g, abs)
		s.alpha = alpha
	case fxInner:
		alpha := s.alpha
		s.alpha = true
		s.children(g, abs) // knocked out
		s.children(g, abs) // kept inside
		s.alpha = alpha
	}
	if knock {
		s.maskShapes(shapes, abs)
	}
	sub := s.failed
	s.failed = failed
	if sub {
		// Mask content needs resvg: the whole group falls back.
		s.fallback()
	}
}

// backdrop is svg.rs's background blur (`<g id="nrbb:{uid}:{sigma}" mask=…>`).
func (s *sceneScan) backdrop(g *usvg.Group, abs skia.Transform, sigma float64) {
	if s.alpha {
		// Adds nothing to an outline.
		return
	}
	m := g.Mask()
	if m == nil || m.Kind() != usvg.MaskTypeAlpha || m.Mask() != nil || !m.Root().Transform().IsIdentity() {
		s.fallback()
		return
	}
	b, ok := m.Rect().Transform(abs)
	if !ok || len(m.Root().Children()) == 0 {
		return
	}
	failed := s.failed
	s.failed = false
	s.maskShapes(m.Root().Children(), abs) // erased under the mask
	s.maskShapes(m.Root().Children(), abs) // the blurred backdrop kept inside it
	sub := s.failed
	s.failed = failed
	if sub {
		s.fallback()
		return
	}
	box := Rect{X: float64(b.X()), Y: float64(b.Y()), W: float64(b.Width()), H: float64(b.Height())}
	native := len(g.Filters()) == 0
	if native {
		for _, k := range g.Children() {
			if _, isGrp := k.(*usvg.Group); isGrp {
				native = false
				break
			}
		}
	}
	s.out = append(s.out, backdrop{box, float64(3 * float64(sigma*absScale(abs))), native})
}

func (s *sceneScan) path(p *usvg.Path, abs skia.Transform) {
	if !p.IsVisible() {
		return
	}
	_, patternStroke := strokePaint(p).(*usvg.Pattern)
	if !p.RenderingMode().UseShapeAntialiasing() || patternStroke {
		s.fallback()
		return
	}
	ok := true
	if f := p.Fill(); f != nil {
		pb := p.Data().Bounds()
		if pb.Width() != 0 && pb.Height() != 0 {
			if pat, isPat := f.Paint().(*usvg.Pattern); isPat {
				ok = s.pattern(pat, abs, pb)
			}
		}
	}
	if !ok {
		s.fallback()
	}
}

func strokePaint(p *usvg.Path) usvg.Paint {
	if st := p.Stroke(); st != nil {
		return st.Paint()
	}
	return nil
}

// pattern: a pattern whose tile contains the whole path is its content clipped to the path; false when
// that does not hold or the content needs resvg.
func (s *sceneScan) pattern(pat *usvg.Pattern, abs skia.Transform, pb skia.Rect) bool {
	r := pat.Rect()
	inv, ok := pat.Transform().Invert()
	if !ok {
		return false
	}
	b, ok := pb.Transform(inv)
	if !ok {
		return false
	}
	const eps = 0.01
	if b.Left() < r.Left()-eps || b.Top() < r.Top()-eps || b.Right() > r.Right()+eps || b.Bottom() > r.Bottom()+eps {
		return false
	}
	inside, failed := s.inside, s.failed
	s.inside, s.failed = true, false
	s.children(pat.Root(), abs.PreConcat(pat.Transform()).PreTranslate(r.X(), r.Y()))
	ok = !s.failed
	s.inside, s.failed = inside, failed
	return ok
}

func (s *sceneScan) image(img *usvg.Image) {
	if !img.IsVisible() {
		return
	}
	if _, isSVG := img.Kind().(usvg.ImageKindSVG); isSVG {
		s.fallback()
		return
	}
	// decode: a pixmap of the rounded size, drawn by render_node over the image's layer box.
	sz := img.Size()
	w, h := u32(math.Round(float64(sz.Width()))), u32(math.Round(float64(sz.Height())))
	if w > math.MaxUint16 || h > math.MaxUint16 || w == 0 || h == 0 {
		s.fallback()
		return
	}
	if _, ok := img.AbsLayerBoundingBox(); !ok {
		s.fallback()
	}
}

// u32 is `f as u32` for a rounded f32: saturating, NaN gives 0.
func u32(f float64) uint32 {
	switch {
	case !(f > 0):
		return 0
	case f >= math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(f)
}

// absScale is the uniform scale of a transform.
func absScale(t skia.Transform) float64 {
	return math.Sqrt(float64(abs32(float32(t.SX*t.SY) - float32(t.KX*t.KY))))
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// simpleClip: a clip path that is one path (through plain transformed groups) becomes a clip layer.
func simpleClip(c *usvg.ClipPath) bool {
	if c.ClipPath() != nil {
		return false
	}
	g := c.Root()
	for {
		kids := g.Children()
		if len(kids) != 1 {
			return false
		}
		switch k := kids[0].(type) {
		case *usvg.Path:
			return true
		case *usvg.Group:
			if k.ClipPath() != nil {
				return false
			}
			g = k
		default:
			return false
		}
	}
}

// isShadow is `shadow_of(g, abs).is_some()`: a shadow group svg.rs tags
// `nrsh:{d|i}:{uid}:{x},{y},{w},{h},{r}:{dx},{dy},{sigma},{#rrggbb},{alpha};…`, drawn analytically only
// under a uniformly scaled transform rotated by less than ~0.5°.
func isShadow(g *usvg.Group, abs skia.Transform) bool {
	rest, ok := strings.CutPrefix(g.ID(), "nrsh:")
	if !ok {
		return false
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 4 {
		return false
	}
	inner := parts[0] == "i"
	geo := strings.Split(parts[2], ",")
	if len(geo) != 5 {
		return false
	}
	var v [5]float64
	for i, s := range geo {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return false
		}
		v[i] = f
	}
	for _, sh := range strings.Split(parts[3], ";") {
		p := strings.Split(sh, ",")
		if len(p) != 5 {
			return false
		}
		for _, i := range []int{0, 1, 2, 4} {
			if _, err := strconv.ParseFloat(p[i], 64); err != nil {
				return false
			}
		}
		c, ok := strings.CutPrefix(p[3], "#")
		if !ok {
			return false
		}
		if _, err := strconv.ParseUint(strings.TrimPrefix(c, "+"), 16, 32); err != nil || c == "" || strings.HasPrefix(c, "+") && len(c) == 1 {
			return false
		}
	}
	aligned := abs32(abs.KX) < 0.01 && abs32(abs.KY) < 0.01 && abs.SX > 0 && abs32(abs.SX-abs.SY) < 1e-4
	plain := g.Opacity() == 1 && g.BlendMode() == usvg.BlendModeNormal && g.ClipPath() == nil
	m := g.Mask()
	switch {
	case !inner && m != nil && len(g.Filters()) == 0 && m.Mask() == nil:
	case inner && m == nil && len(g.Filters()) != 0:
		if _, ok := skia.RectFromXYWH(float32(v[0]), float32(v[1]), float32(v[2]), float32(v[3])); !ok {
			return false
		}
	default:
		return false
	}
	return aligned && plain
}

// effectOf classifies the filters svg.rs writes by their primitive chains (each primitive reading the
// previous one): blur (layer blur), blur? → offset → flood → in (drop shadow), flood → out → offset →
// blur? → in (inner shadow) and flood black → in (group outline). Blurs must be uniform and sRGB.
func effectOf(f *usvg.Filter) fxKind {
	p := f.Primitives()
	prev := func(in usvg.FilterInput, k int) bool {
		return in.Kind == usvg.FilterInputReference && k > 0 && in.Name == p[k-1].Result()
	}
	ref := func(k int) usvg.FilterInput {
		return usvg.FilterInput{Kind: usvg.FilterInputReference, Name: p[k].Result()}
	}
	srgb := true
	for i := range p {
		srgb = srgb && p[i].ColorInterpolation() == usvg.ColorInterpolationSRGB
	}
	uniform := func(b *usvg.FeGaussianBlur) bool { return b.StdDevX() == b.StdDevY() }
	src := usvg.FilterInput{Kind: usvg.FilterInputSourceGraphic}
	srcAlpha := usvg.FilterInput{Kind: usvg.FilterInputSourceAlpha}
	kind := func(i int) usvg.FilterKind { return p[i].Kind() }
	switch len(p) {
	case 1:
		if b, ok := kind(0).(*usvg.FeGaussianBlur); ok && srgb && b.Input() == src {
			return pick(uniform(b), fxBlur)
		}
	case 2:
		fl, ok1 := kind(0).(*usvg.FeFlood)
		c, ok2 := kind(1).(*usvg.FeComposite)
		if ok1 && ok2 {
			black := fl.Color() == usvg.ColorBlack() && fl.Opacity() == 1
			return pick(black && c.Operator().Op == usvg.CompositeIn && prev(c.Input1(), 1) && c.Input2() == srcAlpha, fxBlack)
		}
	case 3:
		o, ok1 := kind(0).(*usvg.FeOffset)
		_, ok2 := kind(1).(*usvg.FeFlood)
		c, ok3 := kind(2).(*usvg.FeComposite)
		if ok1 && ok2 && ok3 && srgb && o.Input() == srcAlpha && c.Operator().Op == usvg.CompositeIn && prev(c.Input1(), 2) && c.Input2() == ref(0) {
			return fxDrop
		}
	case 4:
		b, ok1 := kind(0).(*usvg.FeGaussianBlur)
		o, ok2 := kind(1).(*usvg.FeOffset)
		_, ok3 := kind(2).(*usvg.FeFlood)
		c, ok4 := kind(3).(*usvg.FeComposite)
		if ok1 && ok2 && ok3 && ok4 && srgb && b.Input() == srcAlpha && prev(o.Input(), 1) && c.Operator().Op == usvg.CompositeIn && prev(c.Input1(), 3) && c.Input2() == ref(1) {
			return pick(uniform(b), fxDrop)
		}
	}
	// flood → out → offset → (blur →) in
	if len(p) >= 3 {
		_, ok1 := kind(0).(*usvg.FeFlood)
		out, ok2 := kind(1).(*usvg.FeComposite)
		o, ok3 := kind(2).(*usvg.FeOffset)
		if ok1 && ok2 && ok3 && srgb && out.Operator().Op == usvg.CompositeOut && prev(out.Input1(), 1) && out.Input2() == srcAlpha && prev(o.Input(), 2) {
			var last *usvg.FeComposite
			switch len(p) {
			case 5:
				b, okb := kind(3).(*usvg.FeGaussianBlur)
				c, okc := kind(4).(*usvg.FeComposite)
				if !okb || !okc || !prev(b.Input(), 3) {
					return fxNone
				}
				if !uniform(b) {
					return fxNone
				}
				last = c
			case 4:
				c, okc := kind(3).(*usvg.FeComposite)
				if !okc {
					return fxNone
				}
				last = c
			default:
				return fxNone
			}
			return pick(last.Operator().Op == usvg.CompositeIn && prev(last.Input1(), len(p)-1) && last.Input2() == srcAlpha, fxInner)
		}
	}
	return fxNone
}

func pick(ok bool, k fxKind) fxKind {
	if ok {
		return k
	}
	return fxNone
}

// knockout recognises svg.rs knockout masks (outer strokes, shadows): a luminance mask that is an opaque
// white rect over its region under opaque black shapes, i.e. "everything but the shapes".
func knockout(m *usvg.Mask) ([]usvg.Node, bool) {
	kids := m.Root().Children()
	if len(kids) == 0 {
		return nil, false
	}
	bg, ok := kids[0].(*usvg.Path)
	if !ok {
		return nil, false
	}
	shapes := kids[1:]
	r := m.Rect().ToRect()
	b := bg.Data().Bounds()
	covers := b.Left() <= r.Left()+0.01 && b.Top() <= r.Top()+0.01 && b.Right() >= r.Right()-0.01 && b.Bottom() >= r.Bottom()-0.01
	white := bg.IsVisible() && bg.Stroke() == nil && bg.Fill() != nil && solid(bg.Fill().Paint(), bg.Fill().Opacity(), usvg.ColorWhite())
	plain := m.Kind() == usvg.MaskTypeLuminance && m.Mask() == nil && m.Root().Transform().IsIdentity()
	if !plain || !covers || !white {
		return nil, false
	}
	for _, n := range shapes {
		if !black(n) {
			return nil, false
		}
	}
	return shapes, true
}

func solid(p usvg.Paint, o float32, c usvg.Color) bool {
	x, ok := p.(usvg.Color)
	return ok && x == c && o == 1
}

func black(n usvg.Node) bool {
	switch v := n.(type) {
	case *usvg.Path:
		return v.RenderingMode().UseShapeAntialiasing() &&
			(v.Fill() == nil || solid(v.Fill().Paint(), v.Fill().Opacity(), usvg.ColorBlack())) &&
			(v.Stroke() == nil || solid(v.Stroke().Paint(), v.Stroke().Opacity(), usvg.ColorBlack()))
	case *usvg.Group:
		switch fs := v.Filters(); len(fs) {
		case 0:
			if v.ShouldIsolate() {
				return false
			}
			for _, k := range v.Children() {
				if !black(k) {
					return false
				}
			}
			return true
		case 1:
			plain := v.Opacity() == 1 && v.ClipPath() == nil && v.Mask() == nil && v.BlendMode() == usvg.BlendModeNormal
			return plain && effectOf(fs[0]) == fxBlack
		}
		return false
	case *usvg.Text:
		for _, k := range v.Flattened().Children() {
			if !black(k) {
				return false
			}
		}
		return true
	}
	return false
}
