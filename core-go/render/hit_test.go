package render

import (
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// The hit.rs tests.

func shape(t *testing.T, ty, props string, x, y, w, h float64) *Shape {
	t.Helper()
	p := jsonv.Obj(jsonv.MustParse(props))
	n := &resolve.RNode{ID: "n", Type: ty, P: p, X: x, Y: y, W: w, H: h}
	return ShapeOf(n)
}

func TestEllipseHitsItsAreaNotItsBox(t *testing.T) {
	s := shape(t, "ellipse", `{"fill": "#ff0000"}`, 100, 100, 100, 100)
	if !s.Hits(150, 150) || !s.Hits(105, 150) {
		t.Error("inside not hit")
	}
	if s.Hits(103, 103) || s.Hits(197, 197) {
		t.Error("box corners are empty")
	}
	ring := shape(t, "ellipse", `{"fill": "#ff0000", "innerRadius": 0.5}`, 100, 100, 100, 100)
	if !ring.Hits(110, 150) || ring.Hits(150, 150) {
		t.Error("the hole is not painted")
	}
}

func TestOpenStrokeHitsWithinItsWidth(t *testing.T) {
	s := shape(t, "path", `{"geometry": "M0 0 L150 150", "stroke": "#000000", "strokeWidth": 10}`, 220, 40, 150, 150)
	if !s.Hits(300, 120) || !s.Hits(302, 118) || !s.Hits(300, 125) {
		t.Error("on the line")
	}
	if s.Hits(360, 50) || s.Hits(300, 140) {
		t.Error("far from the line")
	}
	line := shape(t, "line", `{"stroke": "#000000", "strokeWidth": 1}`, 0, 0, 100, 100)
	if !line.Hits(50, 51.5) || line.Hits(50, 56) {
		t.Error("thin lines get a little slack")
	}
}

func TestStrokeOnlyRectangleIsAFrame(t *testing.T) {
	s := shape(t, "rectangle", `{"stroke": "#000000", "strokeWidth": 4}`, 0, 0, 100, 100)
	if !s.Hits(1, 50) || !s.Hits(50, 99) || s.Hits(50, 50) {
		t.Error("only the band is painted")
	}
	s = shape(t, "rectangle", `{"stroke": "#000000", "strokeWidth": 4, "strokeAlignment": "inner"}`, 0, 0, 100, 100)
	if !s.Hits(3, 50) || s.Hits(-3, 50) {
		t.Error("inner band")
	}
}

func TestOpenEndsFollowTheCap(t *testing.T) {
	line := func(cap string) *Shape {
		return shape(t, "path", `{"geometry": "M0 0 L100 0", "stroke": "#000000", "strokeWidth": 10, "strokeLinecap": "`+cap+`"}`, 0, 0, 100, 0)
	}
	// 4 past the end, 3 off the axis: inside a round cap (radius 5), outside a butt one, inside a square one.
	if line("butt").Hits(104, 3) || !line("round").Hits(104, 3) || !line("square").Hits(104, 3) {
		t.Error("caps at (104, 3)")
	}
	if line("round").Hits(104, 4) || !line("square").Hits(104, 4) {
		t.Error("a square cap has corners")
	}
	if !line("butt").Hits(100, 4) || line("butt").Hits(101, 0) {
		t.Error("a butt end is flat")
	}
}

func TestRectangleSidesKeepTheirOwnWidths(t *testing.T) {
	s := shape(t, "rectangle", `{"stroke": "#000000", "strokeWidth": {"top": 6}, "strokeAlignment": "inner"}`, 0, 0, 100, 100)
	if !s.Hits(50, 4) {
		t.Error("the painted top")
	}
	if s.Hits(1, 50) || s.Hits(50, 99) || s.Hits(50, 50) {
		t.Error("the unpainted sides and the inside")
	}
}

func TestBoxesStayTheSurface(t *testing.T) {
	for _, c := range []struct{ ty, p string }{
		{"rectangle", `{"fill": "#ff0000", "stroke": "#000000", "strokeWidth": 2}`},
		{"rectangle", `{}`},
		{"ellipse", `{}`},
		{"ellipse", `{"fill": "#00000000"}`},
		{"ellipse", `{"fill": {"type": "color", "color": "#ff0000", "opacity": 0}}`},
		{"path", `{"geometry": "", "fill": "#ff0000"}`},
		{"frame", `{"stroke": "#000000", "strokeWidth": 2}`},
	} {
		if shape(t, c.ty, c.p, 0, 0, 10, 10) != nil {
			t.Errorf("%s %s: want the box as the surface", c.ty, c.p)
		}
	}
	frame := shape(t, "rectangle", `{"fill": "#ffffff00", "stroke": "#000000", "strokeWidth": 2}`, 0, 0, 100, 100)
	if frame == nil || frame.Hits(50, 50) {
		t.Error("a rectangle with a transparent fill is hit on its stroke only")
	}
}

func TestFilledPathFollowsItsRule(t *testing.T) {
	d := "M0 0H100V100H0ZM25 25H75V75H25Z"
	s := shape(t, "path", `{"geometry": "`+d+`", "fill": "#ff0000", "fillRule": "evenodd"}`, 0, 0, 100, 100)
	if !s.Hits(10, 10) || s.Hits(50, 50) {
		t.Error("evenodd")
	}
	s = shape(t, "path", `{"geometry": "`+d+`", "fill": "#ff0000"}`, 0, 0, 100, 100)
	if !s.Hits(50, 50) {
		t.Error("nonzero fills the same-wound inner square")
	}
}
