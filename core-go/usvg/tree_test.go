package usvg

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

func rect(t *testing.T, l, tp, r, b float32) skia.Rect {
	t.Helper()
	v, ok := skia.RectFromLTRB(l, tp, r, b)
	if !ok {
		t.Fatalf("bad rect %v %v %v %v", l, tp, r, b)
	}
	return v
}

func nzRect(t *testing.T, l, tp, r, b float32) skia.NonZeroRect {
	t.Helper()
	v, ok := skia.NonZeroRectFromLTRB(l, tp, r, b)
	if !ok {
		t.Fatalf("bad non-zero rect %v %v %v %v", l, tp, r, b)
	}
	return v
}

func ltrb(r interface {
	Left() float32
	Top() float32
	Right() float32
	Bottom() float32
}) [4]float32 {
	return [4]float32{r.Left(), r.Top(), r.Right(), r.Bottom()}
}

func rectPath(t *testing.T, l, tp, r, b float32) *skia.Path {
	t.Helper()
	return skia.PathFromRect(rect(t, l, tp, r, b))
}

func TestBBox(t *testing.T) {
	d := defaultBBox()
	if !d.isDefault() {
		t.Fatal("default bbox is not default")
	}
	if _, ok := d.toRect(); ok {
		t.Error("default bbox gave a rect")
	}
	if _, ok := d.toNonZeroRect(); ok {
		t.Error("default bbox gave a non-zero rect")
	}

	got := d.expand(bboxFromRect(rect(t, 1, 2, 3, 4)))
	if r, ok := got.toRect(); !ok || ltrb(r) != [4]float32{1, 2, 3, 4} {
		t.Errorf("expand from default = %v %v", r, ok)
	}

	// f32::min/max keep the first operand for +0 and -0.
	neg := float32(math.Copysign(0, -1))
	pos := bbox{0, 0, 0, 0}.expand(bbox{neg, neg, neg, neg})
	if math.Signbit(float64(pos.left)) || math.Signbit(float64(pos.right)) {
		t.Errorf("+0 expanded with -0 lost its sign: %v", pos)
	}
	negB := bbox{neg, neg, neg, neg}.expand(bbox{0, 0, 0, 0})
	if !math.Signbit(float64(negB.left)) || !math.Signbit(float64(negB.bottom)) {
		t.Errorf("-0 expanded with +0 lost its sign: %v", negB)
	}

	// A zero-sized box is a Rect but not a NonZeroRect.
	line := d.expand(bboxFromRect(rect(t, 0, 5, 10, 5)))
	if _, ok := line.toRect(); !ok {
		t.Error("zero-height bbox gave no rect")
	}
	if _, ok := line.toNonZeroRect(); ok {
		t.Error("zero-height bbox gave a non-zero rect")
	}
}

func TestViewBoxToTransform(t *testing.T) {
	img, _ := skia.SizeFromWH(200, 200)
	vb := viewBox{rect: nzRect(t, 10, 20, 110, 70)} // 100x50: sx 2, sy 4

	vb.aspect = stAspectRatio{Align: stAlignNone}
	if got, want := vb.toTransform(img), skia.TransformFromRow(2, 0, 0, 4, -20, -80); got != want {
		t.Errorf("none = %+v, want %+v", got, want)
	}

	// Meet: scale 2, x -20, y -40, free space w 0, h 100.
	// Slice: scale 4, x -40, y -80, free space w -200, h 0.
	aligns := []stAlign{
		stAlignXMinYMin, stAlignXMidYMin, stAlignXMaxYMin,
		stAlignXMinYMid, stAlignXMidYMid, stAlignXMaxYMid,
		stAlignXMinYMax, stAlignXMidYMax, stAlignXMaxYMax,
	}
	for i, align := range aligns {
		xi, yi := i%3, i/3
		vb.aspect = stAspectRatio{Align: align}
		meetY := [3]float32{-40, 10, 60}[yi]
		if got, want := vb.toTransform(img), skia.TransformFromRow(2, 0, 0, 2, -20, meetY); got != want {
			t.Errorf("align %d meet = %+v, want %+v", align, got, want)
		}
		vb.aspect.Slice = true
		sliceX := [3]float32{-40, -140, -240}[xi]
		if got, want := vb.toTransform(img), skia.TransformFromRow(4, 0, 0, 4, sliceX, -80); got != want {
			t.Errorf("align %d slice = %+v, want %+v", align, got, want)
		}
	}
}

func testStroke(width float32) *Stroke {
	return &Stroke{paint: ColorBlack(), miterlimit: DefaultStrokeMiterlimit(), opacity: 1, width: width}
}

func TestPathBoundingBoxes(t *testing.T) {
	data := rectPath(t, 0, 0, 10, 10)

	p, ok := newSimplePath(data)
	if !ok {
		t.Fatal("newSimplePath failed")
	}
	for name, r := range map[string]skia.Rect{
		"bbox": p.BoundingBox(), "abs": p.AbsBoundingBox(),
		"stroke": p.StrokeBoundingBox(), "absStroke": p.AbsStrokeBoundingBox(),
	} {
		if ltrb(r) != [4]float32{0, 0, 10, 10} {
			t.Errorf("simple %s = %v", name, ltrb(r))
		}
	}

	ts := skia.TransformFromRow(2, 0, 0, 2, 5, 5)
	p, ok = newPath("p", true, nil, testStroke(2), PaintOrderFillAndStroke, DefaultShapeRendering, data, ts)
	if !ok {
		t.Fatal("newPath with stroke failed")
	}
	if got := ltrb(p.StrokeBoundingBox()); got != [4]float32{-1, -1, 11, 11} {
		t.Errorf("stroke bbox = %v", got)
	}
	if got := ltrb(p.AbsBoundingBox()); got != [4]float32{5, 5, 25, 25} {
		t.Errorf("abs bbox = %v", got)
	}
	if got := ltrb(p.AbsStrokeBoundingBox()); got != [4]float32{3, 3, 27, 27} {
		t.Errorf("abs stroke bbox = %v", got)
	}

	// With a skew the path itself is transformed: (0,0) (10,0) (15,10) (5,10).
	skew := skia.TransformFromRow(1, 0, 0.5, 1, 0, 0)
	p, ok = newPath("", true, nil, testStroke(2), PaintOrderFillAndStroke, DefaultShapeRendering, data, skew)
	if !ok {
		t.Fatal("newPath with skew failed")
	}
	if got := ltrb(p.AbsBoundingBox()); got != [4]float32{0, 0, 15, 10} {
		t.Errorf("skewed abs bbox = %v", got)
	}
	as := p.AbsStrokeBoundingBox()
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-5 }
	if !near(as.Top(), -1) || !near(as.Bottom(), 11) || !(as.Left() < -1) || !(as.Right() > 16) {
		t.Errorf("skewed abs stroke bbox = %v", ltrb(as))
	}
	viaRect, _ := p.StrokeBoundingBox().Transform(skew)
	if ltrb(viaRect) == ltrb(as) {
		t.Error("skewed abs stroke bbox was computed from the bbox")
	}

	// A horizontal line without a stroke has a bbox but no layer bbox.
	b := skia.NewPathBuilder()
	b.MoveTo(0, 5)
	b.LineTo(10, 5)
	p, ok = newSimplePath(b.Finish())
	if !ok {
		t.Fatal("line path failed")
	}
	var n Node = p
	if _, ok := n.AbsLayerBoundingBox(); ok {
		t.Error("horizontal line has a layer bbox")
	}
}

func TestStrokeToTinySkia(t *testing.T) {
	s := &Stroke{
		width: 3, miterlimit: NewStrokeMiterlimit(7), linecap: LineCapSquare, linejoin: LineJoinMiterClip,
		dasharray: []float32{1, 2}, dashoffset: 0.5,
	}
	ts := s.ToTinySkia()
	if ts.Width != 3 || ts.MiterLimit != 7 || ts.LineCap != skia.LineCapSquare ||
		ts.LineJoin != skia.LineJoinMiterClip || ts.Dash == nil {
		t.Errorf("ToTinySkia = %+v", ts)
	}
	s.dasharray = []float32{1, 2, 3} // odd: StrokeDash::new gives None
	if s.ToTinySkia().Dash != nil {
		t.Error("odd dash array gave a dash")
	}
	s.dasharray = nil
	if s.ToTinySkia().Dash != nil {
		t.Error("no dash array gave a dash")
	}
}

func TestCalculateBoundingBoxes(t *testing.T) {
	tr := skia.TransformFromTranslate
	p1, ok := newPath("p1", true, nil, nil, PaintOrderFillAndStroke, DefaultShapeRendering,
		rectPath(t, 0, 0, 10, 10), tr(10, 10))
	if !ok {
		t.Fatal("path failed")
	}

	// An empty group keeps its dummy boxes and 0,0,1,1 layer box.
	g3 := newEmptyGroup()
	g3.transform = tr(-5, -5)
	g3.absTransform = tr(5, 5)
	if g3.calculateBoundingBoxes() {
		t.Error("empty group calculated bounding boxes")
	}
	if ltrb(g3.LayerBoundingBox()) != [4]float32{0, 0, 1, 1} || ltrb(g3.BoundingBox()) != [4]float32{} {
		t.Errorf("empty group boxes changed: %v %v", ltrb(g3.LayerBoundingBox()), ltrb(g3.BoundingBox()))
	}

	g1 := newEmptyGroup()
	g1.transform = tr(10, 10)
	g1.absTransform = tr(10, 10)
	g1.children = []Node{p1, &g3}
	if !g1.calculateBoundingBoxes() {
		t.Fatal("g1 failed")
	}
	for name, c := range map[string]struct{ got, want [4]float32 }{
		"bbox":      {ltrb(g1.BoundingBox()), [4]float32{-5, -5, 10, 10}},
		"abs":       {ltrb(g1.AbsBoundingBox()), [4]float32{0, 0, 20, 20}},
		"stroke":    {ltrb(g1.StrokeBoundingBox()), [4]float32{-5, -5, 10, 10}},
		"absStroke": {ltrb(g1.AbsStrokeBoundingBox()), [4]float32{0, 0, 20, 20}},
		"layer":     {ltrb(g1.LayerBoundingBox()), [4]float32{-5, -5, 10, 10}},
	} {
		if c.got != c.want {
			t.Errorf("g1 %s = %v, want %v", name, c.got, c.want)
		}
	}
	if r, _ := g1.AbsLayerBoundingBox(); ltrb(r) != [4]float32{5, 5, 20, 20} {
		t.Errorf("g1 abs layer = %v", ltrb(r))
	}

	root := newEmptyGroup()
	root.children = []Node{&g1}
	if !root.calculateBoundingBoxes() {
		t.Fatal("root failed")
	}
	if got := ltrb(root.BoundingBox()); got != [4]float32{5, 5, 20, 20} {
		t.Errorf("root bbox = %v", got)
	}
	if got := ltrb(root.AbsBoundingBox()); got != [4]float32{0, 0, 20, 20} {
		t.Errorf("root abs bbox = %v", got)
	}
	if got := ltrb(root.LayerBoundingBox()); got != [4]float32{5, 5, 20, 20} {
		t.Errorf("root layer = %v", got)
	}
	if r, ok := root.calculateObjectBBox(); !ok || ltrb(r) != [4]float32{5, 5, 20, 20} {
		t.Errorf("root object bbox = %v %v", ltrb(r), ok)
	}

	// The filter region replaces the layer box.
	root.filters = []*Filter{{id: "f1", rect: nzRect(t, -1, -2, 3, 4)}, {id: "f2", rect: nzRect(t, 0, 0, 30, 1)}}
	if !root.calculateBoundingBoxes() {
		t.Fatal("root with filters failed")
	}
	if got := ltrb(root.LayerBoundingBox()); got != [4]float32{-1, -2, 30, 4} {
		t.Errorf("filtered layer = %v", got)
	}
}

func TestImageNode(t *testing.T) {
	size, _ := skia.SizeFromWH(4, 3)
	img := &Image{size: size, absTransform: skia.Identity(), absBoundingBox: nzRect(t, 1, 1, 9, 7)}
	var n Node = img
	if ltrb(n.StrokeBoundingBox()) != [4]float32{0, 0, 4, 3} || ltrb(n.AbsStrokeBoundingBox()) != [4]float32{1, 1, 9, 7} {
		t.Errorf("image stroke boxes = %v %v", ltrb(n.StrokeBoundingBox()), ltrb(n.AbsStrokeBoundingBox()))
	}

	png := func(w uint32) []byte {
		b := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 0, 0, 0, 0, 2}
		binary.BigEndian.PutUint32(b[16:], w)
		return b
	}
	if s, ok := ImageKindPNG(png(16777217)).actualSize(); !ok || s.Width() != 16777216 || s.Height() != 2 {
		t.Errorf("png size = %v %v", s, ok) // usize as f32 rounds to nearest even
	}
	if _, ok := ImageKindPNG(png(0)).actualSize(); ok {
		t.Error("zero-width image has a size")
	}
	sub := &Tree{size: size}
	if s, ok := (ImageKindSVG{sub}).actualSize(); !ok || s != size {
		t.Errorf("svg size = %v %v", s, ok)
	}
}

func TestCollect(t *testing.T) {
	lg := &LinearGradient{}
	rg := &RadialGradient{}
	patt := &Pattern{root: newEmptyGroup()}
	inPattern, _ := newSimplePath(rectPath(t, 0, 0, 1, 1))
	inPattern.fill = &Fill{paint: rg, opacity: 1}
	patt.root.children = []Node{inPattern}

	c2 := newEmptyClipPath("c2")
	c1 := newEmptyClipPath("c1")
	c1.clipPath = c2
	m2 := &Mask{id: "m2", root: newEmptyGroup()}
	m1 := &Mask{id: "m1", mask: m2, root: newEmptyGroup()}
	c3 := newEmptyClipPath("c3")
	m1.root.children = []Node{&Group{clipPath: c3}} // reached through a subroot
	f1, f2 := &Filter{id: "f1"}, &Filter{id: "f2"}

	p1, _ := newSimplePath(rectPath(t, 0, 0, 1, 1))
	p1.fill = &Fill{paint: patt, opacity: 1}
	p1.stroke = &Stroke{paint: lg, width: 1}
	p2, _ := newSimplePath(rectPath(t, 0, 0, 1, 1))
	p2.fill = &Fill{paint: lg, opacity: 1}
	p2.stroke = &Stroke{paint: ColorWhite(), width: 1}

	tree := &Tree{root: newEmptyGroup()}
	tree.root.children = []Node{
		&Group{clipPath: c1, mask: m1, filters: []*Filter{f1, f2}, children: []Node{p1}},
		&Group{clipPath: c2, mask: m1, filters: []*Filter{f2}, children: []Node{p2, &Group{clipPath: c1}}},
	}

	tree.collectPaintServers()
	if len(tree.linearGradients) != 1 || tree.linearGradients[0] != lg ||
		len(tree.radialGradients) != 1 || tree.radialGradients[0] != rg ||
		len(tree.patterns) != 1 || tree.patterns[0] != patt {
		t.Errorf("paint servers = %v %v %v", tree.linearGradients, tree.radialGradients, tree.patterns)
	}

	clips := tree.root.collectClipPaths(nil)
	if len(clips) != 3 || clips[0] != c1 || clips[1] != c2 || clips[2] != c3 {
		t.Errorf("clip paths = %v", clips)
	}
	masks := tree.root.collectMasks(nil)
	if len(masks) != 2 || masks[0] != m1 || masks[1] != m2 {
		t.Errorf("masks = %v", masks)
	}
	filters := tree.root.collectFilters(nil)
	if len(filters) != 2 || filters[0] != f1 || filters[1] != f2 {
		t.Errorf("filters = %v", filters)
	}

	if !tree.HasDefsNodes() {
		t.Error("no defs reported")
	}
	if (&Tree{masks: masks}).HasDefsNodes() != true || (&Tree{}).HasDefsNodes() {
		t.Error("HasDefsNodes ignores masks or reports an empty tree")
	}
}

func TestPaintEquality(t *testing.T) {
	lg1, lg2 := &LinearGradient{}, &LinearGradient{}
	if Paint(NewColorRGB(1, 2, 3)) != Paint(Color{1, 2, 3}) {
		t.Error("equal colors differ")
	}
	if Paint(lg1) == Paint(lg2) || Paint(lg1) != Paint(lg1) {
		t.Error("gradients not compared by pointer")
	}
	if Paint(ColorBlack()) == Paint(lg1) {
		t.Error("color equals gradient")
	}
	if DefaultFill().paint != Paint(ColorBlack()) || DefaultFill().opacity != 1 {
		t.Error("bad default fill")
	}
}

func TestNodeLookup(t *testing.T) {
	nested := &Text{id: "deep"}
	inner := newEmptyGroup()
	inner.id = "inner"
	inner.children = []Node{nested}
	tree := &Tree{root: newEmptyGroup()}
	tree.root.children = []Node{&inner}

	if n := tree.NodeByID("deep"); n != Node(nested) {
		t.Errorf("NodeByID(deep) = %v", n)
	}
	if tree.NodeByID("") != nil || tree.NodeByID("missing") != nil {
		t.Error("NodeByID found a missing node")
	}
	// Like Rust, text inside a nested group is not found.
	if tree.HasTextNodes() {
		t.Error("nested text reported")
	}
	clip := newEmptyClipPath("c")
	clip.root.children = []Node{&Text{}}
	inner.clipPath = clip
	if !tree.HasTextNodes() {
		t.Error("text in a clip path subroot not reported")
	}
}

func TestStrokeMiterlimit(t *testing.T) {
	nan := float32(math.NaN())
	for _, c := range []struct{ in, want float32 }{{0.5, 1}, {nan, 1}, {1, 1}, {4, 4}, {10.5, 10.5}} {
		if got := NewStrokeMiterlimit(c.in).Get(); got != c.want {
			t.Errorf("NewStrokeMiterlimit(%v) = %v", c.in, got)
		}
	}
	if DefaultStrokeMiterlimit().Get() != 4 {
		t.Error("bad default miterlimit")
	}
	next := math.Nextafter32(4, 5)
	if !DefaultStrokeMiterlimit().Equal(StrokeMiterlimit(next)) || DefaultStrokeMiterlimit().Equal(4.01) {
		t.Error("miterlimit equality is not 4 ulps")
	}
}

func TestParsers(t *testing.T) {
	if v, ok := ParseShapeRendering("crispEdges"); !ok || v != ShapeRenderingCrispEdges || v.UseShapeAntialiasing() {
		t.Error("crispEdges")
	}
	if _, ok := ParseShapeRendering("auto"); ok {
		t.Error("shape-rendering auto accepted")
	}
	if !DefaultShapeRendering.UseShapeAntialiasing() || DefaultTextRendering != TextRenderingOptimizeLegibility {
		t.Error("bad rendering defaults")
	}
	if v, ok := ParseTextRendering("geometricPrecision"); !ok || v != TextRenderingGeometricPrecision {
		t.Error("text geometricPrecision")
	}
	if _, ok := ParseTextRendering("optimizeQuality"); ok {
		t.Error("text optimizeQuality accepted")
	}
	for s, want := range map[string]ImageRendering{
		"optimizeQuality": ImageRenderingOptimizeQuality, "optimizeSpeed": ImageRenderingOptimizeSpeed,
		"smooth": ImageRenderingSmooth, "high-quality": ImageRenderingHighQuality,
		"crisp-edges": ImageRenderingCrispEdges, "pixelated": ImageRenderingPixelated,
	} {
		if v, ok := ParseImageRendering(s); !ok || v != want {
			t.Errorf("image-rendering %q = %v %v", s, v, ok)
		}
	}
	if _, ok := ParseImageRendering("crispEdges"); ok {
		t.Error("image-rendering crispEdges accepted")
	}
	if BlendModeNormal.String() != "normal" || BlendModeColorDodge.String() != "color-dodge" ||
		BlendModeLuminosity.String() != "luminosity" {
		t.Error("bad blend mode names")
	}
}

func TestSmallHelpers(t *testing.T) {
	if _, ok := newNonEmptyString(" \t\n"); ok {
		t.Error("blank string accepted")
	}
	if s, ok := newNonEmptyString(" a "); !ok || s != " a " {
		t.Error("non-blank string rejected or trimmed")
	}
	if _, ok := newNonZeroF32(math.Float32frombits(3)); ok {
		t.Error("near-zero accepted")
	}
	if _, ok := newNonZeroF32(1e-30); !ok {
		t.Error("small non-zero rejected")
	}
	g := newEmptyGroup()
	if g.ShouldIsolate() {
		t.Error("empty group isolates")
	}
	g.opacity = 0.5
	if !g.ShouldIsolate() {
		t.Error("translucent group does not isolate")
	}
}
