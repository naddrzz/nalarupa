package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func approxEq(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

func TestMatrixAndTransforms(t *testing.T) {
	// 90 deg CCW around origin + translation (100, 50)
	om := OwnMatrix(90, false, false)
	m := Matrix6{om[0], om[1], om[2], om[3], 100, 50}
	if !Turned(&m) {
		t.Fatalf("expected 90deg matrix to report Turned=true")
	}
	flipOnly := OwnMatrix(0, true, true)
	if Turned(&flipOnly) {
		t.Fatalf("expected axis-aligned flip matrix to report Turned=false")
	}

	p := ApplyMatrix(&m, 20, 10)
	back := UnapplyMatrix(&m, p[0], p[1], false)
	if !approxEq(back[0], 20, 1e-6) || !approxEq(back[1], 10, 1e-6) {
		t.Fatalf("UnapplyMatrix roundtrip got %v, want (20, 10)", back)
	}

	lp := InParent(&m, 15, -30)
	fwd := LinMatrix(&m, lp[0], lp[1])
	if !approxEq(fwd[0], 15, 1e-4) || !approxEq(fwd[1], -30, 1e-4) {
		t.Fatalf("InParent/LinMatrix mismatch: lp=%v fwd=%v", lp, fwd)
	}

	// TurnAroundCenter keeps the visual center invariant across rotation changes.
	x0, y0, w, h := 120.0, 80.0, 200.0, 100.0
	cBefore := CenterOffset(w, h, 0, false, false, 0, 0)
	center0 := Pt{x0 + cBefore[0], y0 + cBefore[1]}
	nextOrigin := TurnAroundCenter(x0, y0, w, h, 0, 45, false, false, 0, 0)
	cAfter := CenterOffset(w, h, 45, false, false, 0, 0)
	center1 := Pt{nextOrigin[0] + cAfter[0], nextOrigin[1] + cAfter[1]}
	if !approxEq(center0[0], center1[0], 0.02) || !approxEq(center0[1], center1[1], 0.02) {
		t.Fatalf("TurnAroundCenter moved center from %v to %v", center0, center1)
	}

	// Painted bounds of rotated box
	rotBox := Box{ID: "b1", X: 0, Y: 0, W: 100, H: 50, M: &m}
	pb := Painted(rotBox)
	if pb.W < 49.9 || pb.H < 99.9 {
		t.Fatalf("Painted 90deg box expected ~50x100, got %+v", pb)
	}
}

func TestShimmerFlipAndOutlineHelpers(t *testing.T) {
	for _, pos := range []float64{-13.7, 0.0, 41.3, 128.8} {
		h1, h2 := ShimmerHalves(pos, 55.0)
		if h1[0]+h1[1] != h2[0] {
			t.Fatalf("ShimmerHalves(%v) left a seam: %v vs %v", pos, h1, h2)
		}
		if h1[1] <= 0 || h2[1] <= 0 {
			t.Fatalf("ShimmerHalves(%v) non-positive width: %v, %v", pos, h1, h2)
		}
	}

	r := [4]float64{4, 8, 12, 16}
	fx := Matrix6{-1, 0, 0, 1, 0, 0}
	fy := Matrix6{1, 0, 0, -1, 0, 0}
	fxy := Matrix6{-1, 0, 0, -1, 0, 0}
	if got := FlipRadii(r, &fx); got != [4]float64{8, 4, 16, 12} {
		t.Fatalf("FlipRadii horizontal got %v", got)
	}
	if got := FlipRadii(r, &fy); got != [4]float64{16, 12, 8, 4} {
		t.Fatalf("FlipRadii vertical got %v", got)
	}
	if got := FlipRadii(r, &fxy); got != [4]float64{12, 16, 4, 8} {
		t.Fatalf("FlipRadii 180 got %v", got)
	}

	if p := OutlineSVGPath(10, 20, 100, 60, &r, false); len(p) < 20 {
		t.Fatalf("unexpected rounded OutlineSVGPath: %q", p)
	}
	if p := OutlineSVGPath(10, 20, 100, 60, nil, true); len(p) < 15 {
		t.Fatalf("unexpected ellipse OutlineSVGPath: %q", p)
	}
}

func TestSnapMeasureResizeAndCamera(t *testing.T) {
	target := RectD{X: 100, Y: 200, W: 80, H: 60}
	drag := RectD{X: 103, Y: 257, W: 40, H: 40}
	off, guides := Snap(DragPoints(drag), AnchorPoints(target), 6.0, [2]bool{true, true})
	if off[0] != -3 || off[1] != 3 {
		t.Fatalf("Snap got offset %v, want (-3, 3)", off)
	}
	if len(guides) != 3 || guides[0].Pos != 100 || guides[1].Pos != 140 || guides[2].Pos != 260 {
		t.Fatalf("Snap expected 3 distinct guides at x=100, x=140, y=260, got %+v", guides)
	}

	// Axis mask disables X snapping
	offYOnly, _ := Snap(DragPoints(drag), AnchorPoints(target), 6.0, [2]bool{false, true})
	if offYOnly[0] != 0 || offYOnly[1] != 3 {
		t.Fatalf("Snap Y-only got %v, want (0, 3)", offYOnly)
	}

	// Measure horizontal & vertical distance with extension line
	ms := Measure(RectD{X: 0, Y: 0, W: 50, H: 50}, RectD{X: 80, Y: 100, W: 40, H: 40})
	if len(ms) != 2 {
		t.Fatalf("Measure expected 2 lines, got %d", len(ms))
	}
	if ms[0].Len != 30 || !ms[0].HasExt || ms[1].Len != 50 || !ms[1].HasExt {
		t.Fatalf("Measure unexpected lines: %+v", ms)
	}

	// ResizeBox: aspect lock, alt center-out, negative flip
	base := RectD{X: 10, Y: 20, W: 100, H: 50}
	seShift := ResizeBox(base, "se", 40, 5, true, false)
	if !approxEq(seShift.W/seShift.H, 2.0, 1e-6) || seShift.W != 140 || seShift.H != 70 {
		t.Fatalf("ResizeBox shift aspect failed: %+v", seShift)
	}
	altE := ResizeBox(base, "e", 15, 0, false, true)
	if altE.W != 130 || altE.X != -5 {
		t.Fatalf("ResizeBox alt center-out failed: %+v", altE)
	}
	flipped := ResizeBox(base, "e", -150, 0, false, false)
	if flipped.W >= 0 || math.Abs(flipped.W) != 50 {
		t.Fatalf("ResizeBox negative flip expected W=-50, got %+v", flipped)
	}

	// Rotated resize cursor
	rot90 := OwnMatrix(90, false, false)
	if got := ResizeUICursor("e", &rot90); got != ui.CursorResizeNS {
		t.Fatalf("90deg rotated east handle expected CursorResizeNS, got %v", got)
	}

	// SlideCam fits 90-degree rotated slide (1920x1080 -> 1080x1920 painted bounds)
	slide := Box{ID: "s1", X: 0, Y: 0, W: 1920, H: 1080, M: &rot90}
	cam := SlideCam(slide, 1440, 900)
	wantZoom := (900.0 - 80.0) / 1920.0
	if !approxEq(cam.Zoom, wantZoom, 1e-6) {
		t.Fatalf("unexpected rotated SlideCam zoom: got %v, want %v (cam=%+v)", cam.Zoom, wantZoom, cam)
	}
}

func TestBezierPenOperations(t *testing.T) {
	a := &Anchor{X: 0, Y: 0, Out: &Pt{30, 60}}
	b := &Anchor{X: 100, Y: 0, In: &Pt{70, 60}}
	c := &Contour{Anchors: []*Anchor{a, b}, Closed: false}

	// Sample curve before split
	samples := make([]Pt, 11)
	for i := 0; i <= 10; i++ {
		samples[i] = PenPointAt(a, b, float64(i)/10.0)
	}

	// Nearest point to midpoint
	midT, midAt, d2 := PenNearest(a, b, samples[5])
	if d2 > 1e-4 || !approxEq(midT, 0.5, 0.02) {
		t.Fatalf("PenNearest midpoint failed: t=%v at=%v d2=%v", midT, midAt, d2)
	}

	// Split at t=0.4 and verify curve geometry stays identical on both halves
	seg := ContourSegments([]*Contour{c})[0]
	ins := PenSplit(seg, 0.4)
	if len(c.Anchors) != 3 || c.Anchors[1] != ins {
		t.Fatalf("PenSplit did not insert anchor at index 1")
	}
	if !HandlesMirrored(ins) {
		// de Casteljau interior tangent is collinear, though handle lengths differ unless t=0.5
		if ins.In == nil || ins.Out == nil {
			t.Fatalf("PenSplit expected both In and Out handles on split anchor")
		}
	}
	pBefore := samples[2] // t = 0.2 on original == t = 0.5 on first half (0..0.4)
	pFirstHalf := PenPointAt(c.Anchors[0], c.Anchors[1], 0.5)
	if dist2Pt(pBefore, pFirstHalf) > 1e-4 {
		t.Fatalf("de Casteljau first half deviated: %v vs %v", pBefore, pFirstHalf)
	}
	pAfter := samples[7] // t = 0.7 on original == t = 0.5 on second half (0.4..1.0)
	pSecondHalf := PenPointAt(c.Anchors[1], c.Anchors[2], 0.5)
	if dist2Pt(pAfter, pSecondHalf) > 1e-4 {
		t.Fatalf("de Casteljau second half deviated: %v vs %v", pAfter, pSecondHalf)
	}

	// Constrain45
	c45 := Constrain45(Pt{10, 10}, Pt{20, 19})
	if !approxEq(math.Abs(c45[0]-10), math.Abs(c45[1]-10), 1e-6) {
		t.Fatalf("Constrain45 expected 45deg diagonal, got %v", c45)
	}

	// ToggleSmooth: smooth -> corner -> smooth
	ToggleSmooth(c, ins)
	if ins.In != nil || ins.Out != nil {
		t.Fatalf("ToggleSmooth on smooth anchor should clear handles")
	}
	ToggleSmooth(c, ins)
	if ins.In == nil || ins.Out == nil || !HandlesMirrored(ins) {
		t.Fatalf("ToggleSmooth on corner anchor should create mirrored handles")
	}

	// ShownHandles & HitAnchor & HitPenHandle
	sel := map[*Anchor]bool{ins: true}
	shown := ShownHandles([]*Contour{c}, sel)
	if !shown[c.Anchors[0]] || !shown[ins] || !shown[c.Anchors[2]] {
		t.Fatalf("ShownHandles should include selected anchor and adjacent anchors")
	}
	if got := HitAnchor([]*Contour{c}, Pt{ins.X + 2, ins.Y - 2}, 1.0); got != ins {
		t.Fatalf("HitAnchor missed split anchor")
	}
	if hr := HitPenHandle(shown, []*Contour{c}, *ins.Out, 1.0); hr == nil || hr.Anchor != ins || hr.Side != PenOut {
		t.Fatalf("HitPenHandle missed outgoing handle: %+v", hr)
	}

	// DeletePenPoints removes middle anchor and keeps 2-anchor drawable contour
	kept, nextSel := DeletePenPoints([]*Contour{c}, map[*Anchor]bool{ins: true})
	if len(kept) != 1 || len(kept[0].Anchors) != 2 || len(nextSel) == 0 {
		t.Fatalf("DeletePenPoints unexpected result: kept=%d next=%d", len(kept), len(nextSel))
	}
	// Deleting one more anchor from a 2-anchor open contour drops the degenerate 1-anchor open contour
	kept2, _ := DeletePenPoints(kept, map[*Anchor]bool{kept[0].Anchors[0]: true})
	if len(kept2) != 0 {
		t.Fatalf("expected degenerate 1-anchor open contour to be dropped, got %d", len(kept2))
	}
}

func TestPathsIconsThemeAndInspectorValidators(t *testing.T) {
	dir := t.TempDir()
	p0 := DefaultSavePath("My Deck: Draft/1", dir)
	if filepath.Base(p0) != "My Deck- Draft-1.nala" {
		t.Fatalf("unexpected sanitized DefaultSavePath: %s", p0)
	}
	if err := os.WriteFile(p0, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	p1 := DefaultSavePath("My Deck: Draft/1.nala", dir)
	if filepath.Base(p1) != "My Deck- Draft-1-1.nala" {
		t.Fatalf("expected -1 collision suffix, got %s", p1)
	}
	pEmpty := DefaultSavePath("   ", dir)
	if filepath.Base(pEmpty) != "Untitled.nala" {
		t.Fatalf("expected Untitled.nala fallback, got %s", pEmpty)
	}

	// Icons & Brand assets
	if BrandLogoSVG() == nil {
		t.Fatal("BrandLogoSVG() returned nil")
	}
	if len(BrandIconPNG()) == 0 {
		t.Fatal("BrandIconPNG() returned empty bytes")
	}
	if IconSVG("saveAs") == nil || IconSVG("saveAs") == IconSVG("open") {
		t.Fatal("saveAs icon must exist and be distinct from open icon")
	}
	for name := range iconFiles {
		if IconSVG(name) == nil {
			t.Fatalf("embedded icon %q failed to parse", name)
		}
	}
	for name := range inlineIcons {
		if IconSVG(name) == nil {
			t.Fatalf("inline icon %q failed to parse", name)
		}
	}
	nodeTypes := []string{"frame", "group", "rectangle", "ellipse", "polygon", "line", "path", "text", "icon", "image", "script", "browser", "note", "prompt", "context", "ref"}
	for _, nt := range nodeTypes {
		k := NodeIconName(nt, false)
		if IconSVG(k) == nil {
			t.Fatalf("NodeIconName(%q) -> %q nil SVG", nt, k)
		}
	}
	if NodeIconName("browser", false) != "globe" {
		t.Fatalf("browser node must use globe icon, got %q", NodeIconName("browser", false))
	}
	if NodeIconName("frame", true) != "component" {
		t.Fatal("reusable frame must use component icon")
	}

	// Theme & motion helpers
	if EllipsisAt(0) != "." || EllipsisAt(400) != ".." || EllipsisAt(800) != "..." || EllipsisAt(1200) != "." {
		t.Fatalf("EllipsisAt cycle mismatch: %q %q %q %q", EllipsisAt(0), EllipsisAt(400), EllipsisAt(800), EllipsisAt(1200))
	}
	for _, ms := range []int64{0, 400, 800} {
		if len(EllipsisAt(ms)+EllipsisPadAt(ms)) != 3 {
			t.Fatalf("padded ellipsis must always be 3 chars wide at %dms", ms)
		}
		if v := PulseAt(ms); v < 0 || v > 1 {
			t.Fatalf("PulseAt(%d) out of [0,1]: %v", ms, v)
		}
	}
	if _, ok := ParseInspectorHexColor("#F63"); !ok {
		t.Fatal("expected #RGB to be accepted")
	}
	if _, ok := ParseInspectorHexColor("#FF6A3D"); !ok {
		t.Fatal("expected #RRGGBB to be accepted")
	}
	if _, ok := ParseInspectorHexColor("#FF6A3D80"); !ok {
		t.Fatal("expected #RRGGBBAA to be accepted")
	}
	if _, ok := ParseInspectorHexColor("#F638"); ok {
		t.Fatal("expected 4-digit #RGBA to be rejected per Nalarupa inspector spec")
	}

	// Inspector validators
	vars := map[string]any{
		"brand":  map[string]any{"type": "color", "value": "#FF6A3D"},
		"pad":    map[string]any{"type": "number", "value": 16.0},
		"weight": map[string]any{"type": "string", "value": "bold"},
	}
	if !ValidPaint("#FF6A3D", vars) || !ValidPaint("$brand", vars) || !ValidPaint(`["#FF6A3D", "$brand"]`, vars) || !ValidPaint(`{"type":"gradient","colors":[{"color":"#FF6A3D","position":0},{"color":"$brand","position":1}]}`, vars) {
		t.Fatal("ValidPaint expected valid colors to pass")
	}
	if ValidPaint("$missing", vars) || ValidPaint("$pad", vars) || ValidPaint("$weight", vars) || ValidPaint("not-a-color", vars) || ValidPaint(`["#zzz"]`, vars) || ValidPaint(`{"color":"banana"}`, vars) || ValidPaint(`{"type":"gradient","colors":[{"position":0}]}`, vars) || ValidPaint(`{"type":"gradient","colors":[{"color":"banana","position":0}]}`, vars) {
		t.Fatal("ValidPaint expected invalid vars/colors to fail")
	}
	if _, ok := SolidSwatchHex("$brand", vars); !ok {
		t.Fatal("SolidSwatchHex failed to resolve $brand variable")
	}
	if v, ok := SidesOf("12, 24", vars, []int{1, 2, 4}); !ok || len(v.([]any)) != 2 {
		t.Fatalf("SidesOf(12, 24) failed: %v %v", v, ok)
	}
	if _, ok := SidesOf("1 2 3", vars, []int{1, 2, 4}); ok {
		t.Fatal("SidesOf(1 2 3) should fail when 3 is not allowed")
	}
	if _, ok := StrokeWidthOf(`{"top":1,"bottom":2}`, vars); !ok {
		t.Fatal("StrokeWidthOf per-side object should pass")
	}
	if _, ok := StrokeWidthOf(`{"diagonal":2}`, vars); ok {
		t.Fatal("StrokeWidthOf unknown side should fail")
	}
	if _, ok := FontWeightOf("600", vars); !ok {
		t.Fatal("FontWeightOf(600) should pass")
	}
	if _, ok := FontWeightOf("0", vars); ok {
		t.Fatal("FontWeightOf(0) should fail")
	}
	if _, ok := EffectOf(`{"type":"shadow","blur":12,"color":"#00000066","offset":{"x":0,"y":4}}`, vars); !ok {
		t.Fatal("EffectOf valid shadow should pass")
	}
	if _, ok := EffectOf(`{"type":"glow"}`, vars); ok {
		t.Fatal("EffectOf unknown effect type should fail")
	}

	// Bounded layer indentation
	if LayerIndent(0) != 0 || LayerIndent(6) != 84 || LayerIndent(50) != 120 {
		t.Fatalf("LayerIndent bounds unexpected: d0=%d d6=%d d50=%d", LayerIndent(0), LayerIndent(6), LayerIndent(50))
	}
}
