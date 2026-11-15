package skia

import (
	"fmt"
	"slices"
	"testing"
)

// The Rust files of this area (line_clipper.rs, scan/mod.rs, scan/hairline.rs, scan/hairline_aa.rs)
// have no unit tests; these are hand-checked expectations.

// hairRecBlitter records blit calls as strings.
type hairRecBlitter struct {
	unreachableBlitter
	calls []string
}

func (r *hairRecBlitter) blitH(x, y, width uint32) {
	r.calls = append(r.calls, fmt.Sprintf("H %d %d %d", x, y, width))
}

func (r *hairRecBlitter) blitAntiH(x, y uint32, aa []uint8, runs []uint16) {
	s := fmt.Sprintf("AH %d %d", x, y)
	for i := 0; runs[i] != 0; i += int(runs[i]) {
		s += fmt.Sprintf(" %d:%d", runs[i], aa[i])
	}
	r.calls = append(r.calls, s)
}

func (r *hairRecBlitter) blitV(x, y, height uint32, alpha uint8) {
	r.calls = append(r.calls, fmt.Sprintf("V %d %d %d %d", x, y, height, alpha))
}

func (r *hairRecBlitter) blitAntiH2(x, y uint32, a0, a1 uint8) {
	r.calls = append(r.calls, fmt.Sprintf("AH2 %d %d %d %d", x, y, a0, a1))
}

func (r *hairRecBlitter) blitAntiV2(x, y uint32, a0, a1 uint8) {
	r.calls = append(r.calls, fmt.Sprintf("AV2 %d %d %d %d", x, y, a0, a1))
}

func (r *hairRecBlitter) blitRect(rect screenIntRect) {
	r.calls = append(r.calls, fmt.Sprintf("R %d %d %d %d", rect.x, rect.y, rect.width, rect.height))
}

func hairCheckCalls(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got  %q\n want %q", name, got, want)
	}
}

func hairRect(t *testing.T, l, tp, r, b float32) Rect {
	t.Helper()
	rect, ok := RectFromLTRB(l, tp, r, b)
	if !ok {
		t.Fatalf("bad rect %v %v %v %v", l, tp, r, b)
	}
	return rect
}

var hairClip10 = screenIntRectFromXYWHSafe(0, 0, 10, 10)

func TestScanFillRect(t *testing.T) {
	cases := []struct {
		name string
		rect Rect
		want []string
	}{
		{"inside", hairRect(t, 1, 2, 5, 7), []string{"R 1 2 4 5"}},
		{"clipped", hairRect(t, -3, 8, 4, 20), []string{"R 0 8 4 2"}},
		{"outside", hairRect(t, 12, 1, 15, 5), nil},
	}
	for _, c := range cases {
		var b hairRecBlitter
		fillRect(c.rect, hairClip10, &b)
		hairCheckCalls(t, c.name, b.calls, c.want)
	}
}

func TestScanFillRectAA(t *testing.T) {
	cases := []struct {
		name string
		rect Rect
		want []string
	}{
		{"one scanline", hairRect(t, 1.5, 2, 4, 3), []string{"V 1 2 1 127", "AH 2 2 2:255"}},
		{"fractional", hairRect(t, 1.25, 1.5, 3.75, 4.25), []string{
			"V 1 1 1 96", "AH 2 1 1:128", "V 3 1 1 96",
			"V 1 2 2 192", "R 2 2 1 2", "V 3 2 2 192",
			"V 1 4 1 48", "AH 2 4 1:64", "V 3 4 1 48",
		}},
		{"integral", hairRect(t, 2, 3, 6, 5), []string{"R 2 3 4 2"}},
		{"outside", hairRect(t, 12, 1, 15, 5), nil},
	}
	for _, c := range cases {
		var b hairRecBlitter
		fillRectAA(c.rect, hairClip10, &b)
		hairCheckCalls(t, c.name, b.calls, c.want)
	}
}

func hairLinePath(x0, y0, x1, y1 float32) *Path {
	pb := NewPathBuilder()
	pb.MoveTo(x0, y0)
	pb.LineTo(x1, y1)
	return pb.Finish()
}

func TestStrokeHairline(t *testing.T) {
	cases := []struct {
		name           string
		x0, y0, x1, y1 float32
		lineCap        LineCap
		want           []string
	}{
		{"horizontal", 1, 2.5, 6, 2.5, LineCapButt, []string{"H 1 2 1", "H 2 2 1", "H 3 2 1", "H 4 2 1", "H 5 2 1"}},
		{"vertical", 3.5, 1, 3.5, 4, LineCapButt, []string{"H 3 1 1", "H 3 2 1", "H 3 3 1"}},
		{"diagonal", 1, 1, 4, 4, LineCapButt, []string{"H 1 1 1", "H 2 2 1", "H 3 3 1"}},
		{"butt", 2, 2.5, 5, 2.5, LineCapButt, []string{"H 2 2 1", "H 3 2 1", "H 4 2 1"}},
		{"square cap", 2, 2.5, 5, 2.5, LineCapSquare, []string{"H 2 2 1", "H 3 2 1", "H 4 2 1", "H 5 2 1"}},
		{"clipped", -5, 2.5, 5, 2.5, LineCapButt, []string{"H 0 2 1", "H 1 2 1", "H 2 2 1", "H 3 2 1", "H 4 2 1"}},
		{"outside", 12, 1, 15, 5, LineCapButt, nil},
	}
	for _, c := range cases {
		var b hairRecBlitter
		strokeHairline(hairLinePath(c.x0, c.y0, c.x1, c.y1), c.lineCap, hairClip10, &b)
		hairCheckCalls(t, c.name, b.calls, c.want)
	}
}

func TestStrokeHairlineAA(t *testing.T) {
	cases := []struct {
		name           string
		x0, y0, x1, y1 float32
		want           []string
	}{
		{"horizontal", 1, 2.5, 6, 2.5, []string{"AH 1 2 1:255", "AH 2 2 4:255"}},
		{"vertical", 3.5, 1, 3.5, 4, []string{"V 3 1 1 255", "V 3 2 2 255"}},
		{"diagonal", 1, 1, 4, 4, []string{"AH2 1 1 255 0", "AH2 2 2 255 0", "AH2 3 3 255 0"}},
		{"clipped", -5, 2.5, 5, 2.5, []string{"AH 0 2 1:255", "AH 1 2 4:255"}},
		{"outside", 12, 1, 15, 5, nil},
	}
	for _, c := range cases {
		var b hairRecBlitter
		strokeHairlineAA(hairLinePath(c.x0, c.y0, c.x1, c.y1), LineCapButt, hairClip10, &b)
		hairCheckCalls(t, c.name, b.calls, c.want)
	}
}

// TestStrokeHairlineCurvesInClip checks that quad and (nice) cubic hairlines only blit inside the clip.
func TestStrokeHairlineCurvesInClip(t *testing.T) {
	pb := NewPathBuilder()
	pb.MoveTo(-3, 2)
	pb.QuadTo(5, 14, 13, 2)
	pb.MoveTo(-2, 8)
	pb.CubicTo(2, 6, 8, 6, 12, 8)
	path := pb.Finish()

	clip := screenIntRectFromXYWHSafe(0, 0, 10, 10)
	for _, aa := range []bool{false, true} {
		b := &hairBoundsBlitter{clip: clip, t: t}
		if aa {
			strokeHairlineAA(path, LineCapRound, clip, b)
		} else {
			strokeHairline(path, LineCapRound, clip, b)
		}
		if b.n == 0 {
			t.Errorf("aa=%v: nothing drawn", aa)
		}
	}
}

type hairBoundsBlitter struct {
	unreachableBlitter
	clip screenIntRect
	t    *testing.T
	n    int
}

func (b *hairBoundsBlitter) check(x, y, w, h uint32) {
	b.n++
	r := screenIntRectFromXYWHSafe(x, y, w, h)
	if !b.clip.Contains(r) {
		b.t.Errorf("blit %v outside clip", r)
	}
}

func (b *hairBoundsBlitter) blitH(x, y, width uint32) { b.check(x, y, width, 1) }
func (b *hairBoundsBlitter) blitAntiH(x, y uint32, aa []uint8, runs []uint16) {
	b.check(x, y, antiHairComputeWidth(runs), 1)
}
func (b *hairBoundsBlitter) blitV(x, y, height uint32, alpha uint8) { b.check(x, y, 1, height) }
func (b *hairBoundsBlitter) blitAntiH2(x, y uint32, a0, a1 uint8)   { b.check(x, y, 2, 1) }
func (b *hairBoundsBlitter) blitAntiV2(x, y uint32, a0, a1 uint8)   { b.check(x, y, 1, 2) }

func TestAntiHairRectClipBlitter(t *testing.T) {
	var rec hairRecBlitter
	c := &antiHairRectClipBlitter{b: &rec, clip: screenIntRectFromXYWHSafe(2, 1, 3, 3)}

	aa := make([]uint8, 8)
	runs := make([]uint16, 8)
	aa[0], runs[0], runs[7] = 100, 7, 0
	c.blitAntiH(0, 1, aa, runs) // cut on both sides
	c.blitAntiH(0, 0, aa, runs) // above the clip
	c.blitV(3, 0, 10, 50)       // clipped in y
	c.blitV(6, 1, 1, 50)        // right of the clip
	c.blitAntiH2(4, 2, 10, 20)  // second pixel cut
	c.blitAntiV2(1, 3, 30, 40)  // left of the clip
	hairCheckCalls(t, "rect clip", rec.calls, []string{"AH 2 1 3:100", "V 3 1 3 50", "AH 4 2 1:10"})
}

func TestHairHelpers(t *testing.T) {
	if got := hairComputeQuadLevel(&[3]Point{{0, 0}, {10, 10}, {20, 0}}); got != 2 {
		t.Errorf("quad level = %d, want 2", got)
	}
	if got := hairComputeCubicSegments(&[4]Point{{0, 0}, {1, 0}, {2, 0}, {3, 0}}); got != 1 {
		t.Errorf("cubic segments = %d, want 1", got)
	}
	if got := antiHairContribution64(128); got != 64 {
		t.Errorf("contribution64(128) = %d, want 64", got)
	}
	if got := antiHairContribution64(130); got != 2 {
		t.Errorf("contribution64(130) = %d, want 2", got)
	}
	if antiHairAnyBadInts(0, 1, -5, 1<<20) != 0 || antiHairAnyBadInts(0, -0x80000000, 0, 0) == 0 {
		t.Error("antiHairAnyBadInts")
	}
}

func TestLineClipperClip(t *testing.T) {
	clip := hairRect(t, 0, 0, 10, 10)
	cases := []struct {
		name string
		src  [2]Point
		cull bool
		want []Point
	}{
		{"above", [2]Point{{1, -5}, {2, -1}}, false, nil},
		{"below", [2]Point{{1, 10}, {2, 15}}, false, nil},
		{"crosses left", [2]Point{{-5, 2}, {5, 8}}, false, []Point{{0, 2}, {0, 5}, {5, 8}}},
		{"crosses left reversed", [2]Point{{5, 8}, {-5, 2}}, false, []Point{{5, 8}, {0, 5}, {0, 2}}},
		{"right culled", [2]Point{{12, 1}, {15, 5}}, true, nil},
		{"right", [2]Point{{12, 1}, {15, 5}}, false, []Point{{10, 1}, {10, 5}}},
		{"left", [2]Point{{-3, 1}, {-1, 5}}, false, []Point{{0, 1}, {0, 5}}},
		{"chopped in y", [2]Point{{2, -10}, {2, 20}}, false, []Point{{2, 0}, {2, 10}}},
		{"both sides", [2]Point{{-10, 0}, {20, 6}}, false, []Point{{0, 0}, {0, 2}, {10, 4}, {10, 6}}},
	}
	for _, c := range cases {
		var points [lineClipperMaxPoints]Point
		got := lineClip(&c.src, &clip, c.cull, &points)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLineClipperIntersect(t *testing.T) {
	clip := hairRect(t, 0, 0, 10, 10)
	cases := []struct {
		name string
		src  [2]Point
		ok   bool
		want [2]Point
	}{
		{"contained", [2]Point{{1, 1}, {5, 5}}, true, [2]Point{{1, 1}, {5, 5}}},
		{"outside", [2]Point{{11, 1}, {12, 5}}, false, [2]Point{}},
		{"crossing", [2]Point{{-5, 5}, {5, 5}}, true, [2]Point{{0, 5}, {5, 5}}},
		{"crossing reversed", [2]Point{{15, 5}, {5, 5}}, true, [2]Point{{10, 5}, {5, 5}}},
		{"coincident vertical", [2]Point{{0, -5}, {0, 15}}, true, [2]Point{{0, 0}, {0, 10}}},
		{"touching corner", [2]Point{{10, 10}, {12, 12}}, false, [2]Point{}},
	}
	for _, c := range cases {
		var dst [2]Point
		ok := lineIntersect(&c.src, &clip, &dst)
		if ok != c.ok || (ok && dst != c.want) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, ok, dst, c.ok, c.want)
		}
	}
}
