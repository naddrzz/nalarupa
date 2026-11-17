package editor

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func newEngine(t *testing.T) *engine.Engine {
	t.Helper()
	e := engine.New(t.TempDir())
	e.Offline()
	e.LoadTemplate("empty")
	return e
}

func insert(t *testing.T, e *engine.Engine, parent, node string) string {
	t.Helper()
	var id string
	if err := e.Edit(func(d *doc.Doc) (err error) {
		id, err = d.Insert(parent, jsonv.MustParse(node), -1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func update(t *testing.T, e *engine.Engine, id, props string) {
	t.Helper()
	if err := e.Edit(func(d *doc.Doc) error { return d.Update(id, jsonv.MustParse(props)) }); err != nil {
		t.Fatal(err)
	}
}

func painted(e *engine.Engine, ids ...string) []Rect {
	e.Sync()
	v := newView(e)
	out := make([]Rect, len(ids))
	for i, id := range ids {
		out[i], _ = v.painted(id, nil)
	}
	return out
}

// footprint is the centre and the canvas position of the box's top-left corner (the pair pins rotation and
// mirroring too).
func footprint(e *engine.Engine, id string) [4]float64 {
	e.Sync()
	c, _ := newView(e).corners(id, nil)
	return [4]float64{(c[0][0] + c[3][0]) / 2, (c[0][1] + c[3][1]) / 2, c[0][0], c[0][1]}
}

func assertSame(t *testing.T, a, b [4]float64, what string) {
	t.Helper()
	for i := range a {
		if math.Abs(a[i]-b[i]) >= 0.05 {
			t.Fatalf("%s: %v → %v", what, a, b)
		}
	}
}

func kidIDs(e *engine.Engine, id string) []string {
	var out []string
	for _, k := range kids(e.Doc, id) {
		out = append(out, idOf(k))
	}
	return out
}

func parentOf(e *engine.Engine, id string) string {
	p, _, _ := e.Doc.ParentOf(id)
	return p
}

func f64(v jsonv.Value) float64 { f, _ := jsonv.F64(v); return f }

func TestPathContoursRoundtrip(t *testing.T) {
	// A corner, a smooth point and a closing segment; the duplicate closing point merges.
	cs := contoursOf("M0 0 L10 0 C15 0 20 5 20 10 Q20 20 10 20 L0 0 Z m30 0 h5")
	if len(cs) != 2 || !cs[0].closed || len(cs[0].anchors) != 4 || cs[1].closed {
		t.Fatalf("%+v", cs)
	}
	if *cs[0].anchors[1].out != (point{15, 0}) || *cs[0].anchors[2].in != (point{20, 5}) {
		t.Fatal("handles")
	}
	if cs[1].anchors[0].p != (point{30, 0}) || cs[1].anchors[1].p != (point{35, 0}) {
		t.Fatal("relative contour")
	}
	d := pathD(cs)
	if want := "M0 0 L10 0 C15 0 20 5 20 10 C20 16.667 16.667 20 10 20 L0 0 Z M30 0 L35 0"; d != want {
		t.Fatalf("%s, want %s", d, want)
	}
	if pathD(contoursOf(d)) != d {
		t.Fatal("reading and writing again is lossless")
	}
}

func TestAlignUsesThePaintedBounds(t *testing.T) {
	e := newEngine(t)
	frame := insert(t, e, doc.Root, `{"type": "frame", "x": 0, "y": 0, "width": 400, "height": 400, "layout": "none"}`)
	var ids []string
	for _, n := range []string{`"x": 100, "y": 50`, `"x": 200, "y": 150, "flipX": true`, `"x": 300, "y": 250, "rotation": 90`} {
		ids = append(ids, insert(t, e, frame, `{"type": "rectangle", "width": 40, "height": 40, `+n+`}`))
	}
	check := func(dir string, ok func(r Rect) bool) {
		t.Helper()
		if err := Align(e, ids, dir); err != nil {
			t.Fatal(err)
		}
		for _, r := range painted(e, ids...) {
			if !ok(r) {
				t.Fatalf("%s: %v", dir, painted(e, ids...))
			}
		}
	}
	check("left", func(r Rect) bool { return math.Abs(r.X-100) < 1e-6 })
	check("top", func(r Rect) bool { return math.Abs(r.Y-50) < 1e-6 })
	check("right", func(r Rect) bool { return math.Abs(r.X+r.W-140) < 1e-6 })
	if err := Align(e, ids, "vcenter"); err != nil {
		t.Fatal(err)
	}
	ps := painted(e, ids...)
	for _, r := range ps {
		if math.Abs(r.Y+r.H/2-(ps[0].Y+ps[0].H/2)) >= 1e-6 {
			t.Fatalf("vcenter %v", ps)
		}
	}
	// One rotated layer aligns to its parent's box.
	if err := Align(e, ids[2:], "left"); err != nil {
		t.Fatal(err)
	}
	if r := painted(e, ids[2])[0]; math.Abs(r.X) >= 1e-6 {
		t.Fatalf("left of the parent: %v", r)
	}
}

func TestAlignInsideARotatedParent(t *testing.T) {
	e := newEngine(t)
	frame := insert(t, e, doc.Root, `{"type": "frame", "x": 100, "y": 100, "width": 400, "height": 300, "rotation": 30, "layout": "none"}`)
	ids := []string{
		insert(t, e, frame, `{"type": "rectangle", "x": 40, "y": 30, "width": 60, "height": 20}`),
		insert(t, e, frame, `{"type": "rectangle", "x": 200, "y": 150, "width": 30, "height": 50, "rotation": 15}`),
	}
	before := painted(e, ids...)
	if err := Align(e, ids, "left"); err != nil {
		t.Fatal(err)
	}
	after := painted(e, ids...)
	left := math.Min(before[0].X, before[1].X)
	// Positions are stored to 0.01, so a parent-space step lands within that of the canvas edge.
	for _, r := range after {
		if math.Abs(r.X-left) >= 0.01 {
			t.Fatalf("canvas-space left edges: %v", after)
		}
	}
	if math.Abs(before[0].X-left) <= 1e-6 && math.Abs(before[1].X-left) <= 1e-6 {
		t.Fatal("the layers started unaligned")
	}
}

func TestReparentKeepsAGroupWithATurnedChildInPlace(t *testing.T) {
	e := newEngine(t)
	frame := insert(t, e, doc.Root, `{"type": "frame", "x": 300, "y": 300, "width": 400, "height": 400, "layout": "none"}`)
	// Turned 180°, the second child paints up-left of its origin (0..40, 0..20): the group's box starts there,
	// not at the lowest child x/y (40, 20).
	group := insert(t, e, doc.Root, `{"type": "group", "x": 100, "y": 100, "children": [
		{"type": "rectangle", "x": 100, "y": 100, "width": 40, "height": 20},
		{"type": "rectangle", "x": 40, "y": 20, "width": 40, "height": 20, "rotation": 180}]}`)
	before := painted(e, group)[0]
	if err := Reparent(e, []string{group}, frame, -1); err != nil {
		t.Fatal(err)
	}
	after := painted(e, group)[0]
	if math.Abs(after.X-before.X) >= 1e-6 || math.Abs(after.Y-before.Y) >= 1e-6 {
		t.Fatalf("%v → %v", before, after)
	}
}

func TestUnwrapKeepsChildrenInATurnedParent(t *testing.T) {
	for _, turn := range []string{`"rotation": 30`, `"flipX": true`, `"flipY": true, "rotation": -50`, `"flipX": true, "rotation": 20`} {
		for _, ty := range []string{"frame", "group"} {
			e := newEngine(t)
			extra := ""
			if ty == "frame" {
				extra = `"width": 200, "height": 150, "layout": "none", `
			}
			id := insert(t, e, doc.Root, `{"type": "`+ty+`", "x": 200, "y": 200, `+extra+turn+`, "children": [
				{"type": "rectangle", "x": 50, "y": 40, "width": 80, "height": 60},
				{"type": "rectangle", "x": 120, "y": 10, "width": 30, "height": 30, "rotation": 15, "flipY": true}]}`)
			kids := kidIDs(e, id)
			var before [][4]float64
			for _, k := range kids {
				before = append(before, footprint(e, k))
			}
			out, err := Unwrap(e, []string{id})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(out, kids) {
				t.Fatalf("%v, want %v", out, kids)
			}
			for i, k := range kids {
				assertSame(t, before[i], footprint(e, k), ty+" "+turn)
			}
		}
	}
}

func TestReparentKeepsLayersAcrossTurnedFrames(t *testing.T) {
	for _, turn := range []string{`"rotation": 30`, `"flipX": true`, `"flipY": true, "rotation": 100`} {
		e := newEngine(t)
		rect := insert(t, e, doc.Root, `{"type": "rectangle", "x": 300, "y": 100, "width": 80, "height": 60, "rotation": 10}`)
		frame := insert(t, e, doc.Root, `{"type": "frame", "x": 100, "y": 300, "width": 300, "height": 200, "layout": "none", `+turn+`}`)
		before := footprint(e, rect)
		if err := Reparent(e, []string{rect}, frame, -1); err != nil {
			t.Fatal(err)
		}
		assertSame(t, before, footprint(e, rect), "into "+turn)
		if err := Reparent(e, []string{rect}, doc.Root, -1); err != nil {
			t.Fatal(err)
		}
		assertSame(t, before, footprint(e, rect), "out of "+turn)
		if r := f64(jsonv.Get(e.Doc.Find(rect), "rotation")); r != 10 {
			t.Fatalf("back to the original rotation: %v", r)
		}
	}
}

func TestWrapKeepsLayersFromDifferentTurnedFrames(t *testing.T) {
	for _, turn := range []string{`, "rotation": 90`, `, "rotation": 30`, `, "flipX": true`, `, "flipY": true, "rotation": -50`} {
		for _, kind := range []string{"group", "frame"} {
			// Each parent in turn takes the wrapper (the topmost member decides).
			for _, plainOnTop := range []bool{false, true} {
				e := newEngine(t)
				frame := func(x, y int, turn string) string {
					return fmt.Sprintf(`{"type": "frame", "x": %d, "y": %d, "width": 300, "height": 300, "layout": "none"%s}`, x, y, turn)
				}
				var f, g string
				if plainOnTop {
					g = insert(t, e, doc.Root, frame(500, 100, turn))
					f = insert(t, e, doc.Root, frame(0, 0, ""))
				} else {
					f = insert(t, e, doc.Root, frame(0, 0, ""))
					g = insert(t, e, doc.Root, frame(500, 100, turn))
				}
				a := insert(t, e, f, `{"type": "rectangle", "x": 20, "y": 20, "width": 60, "height": 50}`)
				b := insert(t, e, g, `{"type": "rectangle", "x": 20, "y": 120, "width": 60, "height": 50, "rotation": 15}`)
				ids := []string{a, b}
				before := [][4]float64{footprint(e, a), footprint(e, b)}
				w, err := Wrap(e, ids, kind)
				if err != nil {
					t.Fatal(err)
				}
				what := fmt.Sprintf("%s in %s, plain frame on top: %v", kind, turn, plainOnTop)
				want := g
				if plainOnTop {
					want = f
				}
				if parentOf(e, w) != want {
					t.Fatalf("%s: wrapper in %s", what, parentOf(e, w))
				}
				for i, id := range ids {
					assertSame(t, before[i], footprint(e, id), what)
				}
				if kind == "frame" {
					// The new frame holds everything its members paint.
					e.Sync()
					v := newView(e)
					wf := v.flat(w)
					for _, id := range ids {
						p, _ := v.painted(id, wf.M)
						// The frame's size is stored rounded to hundredths.
						const tol = 0.01
						if p.X < wf.X-tol || p.Y < wf.Y-tol || p.X+p.W > wf.X+wf.W+tol || p.Y+p.H > wf.Y+wf.H+tol {
							t.Fatalf("%s: members stick out of the new frame", what)
						}
					}
				}
				if _, err := Unwrap(e, []string{w}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestReparentBetweenUprightParentsLeavesTheTurnAlone(t *testing.T) {
	e := newEngine(t)
	rect := insert(t, e, doc.Root, `{"type": "rectangle", "x": 300, "y": 100, "width": 80, "height": 60, "flipY": true}`)
	frame := insert(t, e, doc.Root, `{"type": "frame", "x": 100, "y": 300, "width": 300, "height": 200, "layout": "none"}`)
	if err := Reparent(e, []string{rect}, frame, -1); err != nil {
		t.Fatal(err)
	}
	n := jsonv.Obj(e.Doc.Find(rect))
	if !jsonv.IsTrue(jsonv.Get(n, "flipY")) || n.Has("rotation") || n.Has("flipX") {
		t.Fatal(string(jsonv.Marshal(n)))
	}
}

func TestPathWriteKeepsTheCanvasPosition(t *testing.T) {
	e := newEngine(t)
	frame := insert(t, e, doc.Root, `{"type": "frame", "x": 100, "y": 50, "width": 400, "height": 300, "layout": "none"}`)
	// Drawn on the canvas: a triangle with one curved side, inside the frame.
	drawn := jsonv.MustParse(`[{"closed": true, "anchors": [{"x": 150, "y": 100}, {"x": 250, "y": 100, "out": [270, 140]}, {"x": 200, "y": 180, "in": [230, 190]}]}]`)
	id, err := PathWrite(e, "", false, frame, drawn)
	if err != nil {
		t.Fatal(err)
	}
	n := e.Doc.Find(id)
	if f64(jsonv.Get(n, "x")) != 50 || f64(jsonv.Get(n, "y")) != 50 || !jsonv.StrIs(jsonv.Get(n, "stroke"), "#000000") {
		t.Fatalf("stored relative to the frame: %s", jsonv.Marshal(n))
	}
	close := func(v jsonv.Value, want float64) bool { return math.Abs(f64(v)-want) < 1e-6 }
	at := func(v jsonv.Value, path ...any) jsonv.Value {
		for _, p := range path {
			switch k := p.(type) {
			case int:
				v = jsonv.Items(v)[k]
			case string:
				v = jsonv.Get(v, k)
			}
		}
		return v
	}
	back, err := PathEdit(e, id)
	if err != nil {
		t.Fatal(err)
	}
	if !close(at(back, 0, "anchors", 2, "in", 0), 230) || !close(at(back, 0, "anchors", 1, "y"), 100) {
		t.Fatal(string(jsonv.Marshal(back)))
	}
	// Dragging the first anchor left and up grows the box that way; the other anchors stay.
	moved := jsonv.Clone(back)
	jsonv.Arr(at(moved, 0, "anchors")).Items[0] = jsonv.MustParse(`{"x": 120, "y": 70}`)
	if _, err := PathWrite(e, id, true, doc.Root, moved); err != nil {
		t.Fatal(err)
	}
	n = e.Doc.Find(id)
	if !close(jsonv.Get(n, "x"), 20) || !close(jsonv.Get(n, "y"), 20) {
		t.Fatal(string(jsonv.Marshal(n)))
	}
	again, _ := PathEdit(e, id)
	if !close(at(again, 0, "anchors", 0, "x"), 120) || !close(at(again, 0, "anchors", 2, "in", 0), 230) {
		t.Fatal(string(jsonv.Marshal(again)))
	}
	// Rotated paths are edited where they are painted.
	update(t, e, id, `{"rotation": 90}`)
	turned, _ := PathEdit(e, id)
	if _, err := PathWrite(e, id, true, doc.Root, turned); err != nil {
		t.Fatal(err)
	}
	if got, _ := PathEdit(e, id); string(jsonv.Marshal(got)) != string(jsonv.Marshal(turned)) {
		t.Fatalf("%s\n%s", jsonv.Marshal(got), jsonv.Marshal(turned))
	}
	if _, err := PathWrite(e, id, true, doc.Root, jsonv.MustParse(`[{"closed": false, "anchors": [{"x": 0, "y": 0}]}]`)); err == nil {
		t.Fatal("one point is no path")
	}
}

func TestFlexInference(t *testing.T) {
	// Three boxes in a row, 20 apart, bottom-aligned, inside a frame with 16/24 margins.
	row := []Kid{{rect(40, 30, 50, 20), false}, {rect(110, 10, 50, 40), false}, {rect(180, 20, 50, 30), false}}
	f := InferFlex(ptr(rect(16, -6, 238, 72)), row)
	if f.Dir != 0 || f.Gap != 20 || f.Align != "end" || f.Padding != [4]float64{16, 24, 16, 24} || !slices.Equal(f.Order, []int{0, 1, 2}) {
		t.Fatalf("%+v", f)
	}
	// A column given out of order; two full-width children fill the cross axis.
	col := []Kid{{rect(0, 50, 100, 20), true}, {rect(0, 0, 100, 30), false}, {rect(20, 90, 60, 10), false}}
	f = InferFlex(nil, col)
	if f.Dir != 1 || f.Gap != 20 || f.Align != "center" || f.Padding != [4]float64{} || !slices.Equal(f.Order, []int{1, 0, 2}) || !slices.Equal(f.Fill, []int{0, 1}) {
		t.Fatalf("%+v", f)
	}
	// A lone text is centered with the smaller margins as padding.
	f = InferFlex(ptr(rect(0, 0, 100, 40)), []Kid{{rect(30, 12, 40, 16), true}})
	if f.Align != "center" || f.Justify != "center" || f.Padding != [4]float64{12, 30, 12, 30} {
		t.Fatalf("%+v", f)
	}
	if InferFlex(nil, nil).Padding != [4]float64{10, 10, 10, 10} {
		t.Fatal("no children")
	}
}

func TestClipboardDecoding(t *testing.T) {
	e := newEngine(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="24px" viewBox="0 0 24 12"><path d="M0 0"/></svg>`
	if w, h := svgSize(svg); w != 24 || h != 12 {
		t.Fatal(w, h)
	}
	c, err := decode(e, "", svg)
	if err != nil || !jsonv.StrIs(jsonv.Get(c.nodes[0], "type"), "rectangle") || !c.center {
		t.Fatal(err, c)
	}
	c, _ = decode(e, "", "hello\nworld")
	if !jsonv.StrIs(jsonv.Get(c.nodes[0], "content"), "hello\nworld") {
		t.Fatal(c.nodes)
	}
	// The .pen clipboard marker and payload shape are accepted.
	payload := `{"source": "other", "localData": ["a"], "remoteData": {"nodes": [{"type": "rectangle", "id": "a", "x": 5, "y": 6, "width": 10, "height": 20}]}}`
	html := `<span ` + penMark + `="` + base64.StdEncoding.EncodeToString([]byte(payload)) + `">Node ID: a</span>`
	c, err = decode(e, html, "Node ID: a")
	if err != nil || c.boxes[0] != (rect(5, 6, 10, 20)) || len(c.sources) != 0 || c.center {
		t.Fatal(err, c)
	}
}

// The rows of the plan's matrix the replay sequences (testdata/replay/editor-*.jsonl) cover for parity, checked
// here for behaviour.

func TestStructureUndoRestores(t *testing.T) {
	e := newEngine(t)
	f := insert(t, e, doc.Root, `{"type": "frame", "x": 0, "y": 0, "width": 300, "height": 300, "layout": "none", "rotation": 20}`)
	g := insert(t, e, doc.Root, `{"type": "frame", "x": 500, "y": 0, "width": 300, "height": 300, "layout": "horizontal", "flipX": true}`)
	a := insert(t, e, f, `{"type": "rectangle", "x": 10, "y": 10, "width": 40, "height": 40, "flipY": true}`)
	start := string(jsonv.Marshal(e.Doc.Root))
	if err := Reparent(e, []string{a}, "nope", -1); err == nil || err.Error() != "'nope' cannot contain other layers" {
		t.Fatal(err)
	}
	if err := Reparent(e, []string{a}, g, 0); err != nil {
		t.Fatal(err)
	}
	if n := jsonv.Obj(e.Doc.Find(a)); n.Has("x") && f64(jsonv.Get(n, "x")) != 10 {
		t.Fatal("a layer entering auto layout keeps no new x/y")
	}
	w, err := Wrap(e, []string{a}, "group")
	if err != nil || parentOf(e, a) != w {
		t.Fatal(err)
	}
	if _, err := Unwrap(e, []string{w}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e.Undo()
	}
	if got := string(jsonv.Marshal(e.Doc.Root)); got != start {
		t.Fatalf("undo restores:\n%s\n%s", got, start)
	}
	if _, err := Wrap(e, nil, "frame"); err == nil || err.Error() != "select layers to wrap" {
		t.Fatal(err)
	}
	if _, err := Unwrap(e, []string{a}); err == nil || err.Error() != "select a group or frame to ungroup" {
		t.Fatal(err)
	}
}

func TestLayoutRoundTrip(t *testing.T) {
	e := newEngine(t)
	f := insert(t, e, doc.Root, `{"type": "frame", "x": 0, "y": 0, "width": 400, "height": 200, "layout": "none"}`)
	ids := []string{
		insert(t, e, f, `{"type": "rectangle", "x": 20, "y": 30, "width": 50, "height": 50}`),
		insert(t, e, f, `{"type": "rectangle", "x": 100, "y": 30, "width": 50, "height": 50}`),
	}
	before := painted(e, ids...)
	l, err := AddLayout(e, ids)
	if err != nil {
		t.Fatal(err)
	}
	n := e.Doc.Find(l)
	if !jsonv.StrIs(jsonv.Get(n, "layout"), "horizontal") || f64(jsonv.Get(n, "gap")) != 30 {
		t.Fatal(string(jsonv.Marshal(n)))
	}
	if err := SetLayout(e, []string{l}, "vertical"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLayout(e, []string{l}); err != nil {
		t.Fatal(err)
	}
	e.Undo()
	e.Undo()
	if after := painted(e, ids...); after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("the row keeps the arrangement: %v → %v", before, after)
	}
	e.Undo()
	if parentOf(e, ids[0]) != f {
		t.Fatal("add_layout undoes in one step")
	}
	if err := SetLayout(e, []string{f}, "vertical"); err == nil || err.Error() != f+" is not an auto layout frame" {
		t.Fatal(err)
	}
}

func TestDuplicateBesideATopLevelFrame(t *testing.T) {
	e := newEngine(t)
	f := insert(t, e, doc.Root, `{"type": "frame", "x": 0, "y": 0, "width": 100, "height": 100, "layout": "none", "reusable": true}`)
	out, err := Duplicate(e, []string{f}, false)
	if err != nil {
		t.Fatal(err)
	}
	n := e.Doc.Find(out[0])
	if f64(jsonv.Get(n, "x")) != 140 || jsonv.IsTrue(jsonv.Get(n, "reusable")) {
		t.Fatalf("a plain copy right of the frame: %s", jsonv.Marshal(n))
	}
	out, _ = Duplicate(e, []string{f}, true)
	if f64(jsonv.Get(e.Doc.Find(out[0]), "x")) != 0 {
		t.Fatal("in place")
	}
}

func TestQueriesAndBase64Errors(t *testing.T) {
	e := newEngine(t)
	f := insert(t, e, doc.Root, `{"type": "frame", "x": 0, "y": 0, "width": 400, "height": 300, "layout": "none"}`)
	a := insert(t, e, f, `{"type": "rectangle", "x": 10, "y": 10, "width": 50, "height": 50}`)
	e.Sync()
	if got := NodesInRect(e, rect(0, 0, 70, 70), false); !slices.Equal(got, []string{a}) {
		t.Fatal(got)
	}
	if got := DropTarget(e, 30, 30, 10, 10, nil, false, "", false); !jsonv.StrIs(jsonv.Get(got, "id"), f) {
		t.Fatal(string(jsonv.Marshal(got)))
	}
	if b, ok := FitBounds(e); !ok || b != (rect(0, 0, 400, 300)) {
		t.Fatal(b)
	}
	for in, want := range map[string]string{
		"@@@": "Invalid symbol 64, offset 0.", "aGVsbG8": "Invalid padding", "aGVsbG9=": "Invalid last symbol 57, offset 6.",
		"a": "Invalid input length: 1", "ab=c": "Invalid symbol 61, offset 2.", "====": "Invalid symbol 61, offset 0.",
	} {
		if _, err := b64Decode(in); err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %s", in, err, want)
		}
	}
	for _, s := range []string{"", "a", "ab", "abc", "hello world", "\x00\xff\xfe"} {
		enc := b64Encode([]byte(s))
		if got, err := b64Decode(enc); err != nil || string(got) != s {
			t.Errorf("%q: %q %v", s, got, err)
		}
	}
	for v, want := range map[float64]string{1.005: "1", 2: "2", 2.5: "2.5", 1.239: "1.24", -0.004: "0", 1e15: "1000000000000000.0", -3.456: "-3.46"} {
		if got := string(jsonv.Marshal(Num(v))); got != want {
			t.Errorf("Num(%v) = %s, want %s", v, got, want)
		}
	}
}

func TestClipboardBetweenDocuments(t *testing.T) {
	e := newEngine(t)
	if err := e.Edit(func(d *doc.Doc) error {
		return d.SetVariables(jsonv.MustParse(`{"brand": {"type": "color", "value": [{"value": "#ff0000"}, {"value": "#00ff00", "theme": {"mode": "dark"}}]}, "alias": {"type": "color", "value": "$brand"}}`), false)
	}); err != nil {
		t.Fatal(err)
	}
	comp := insert(t, e, doc.Root, `{"type": "frame", "name": "Comp", "reusable": true, "width": 100, "height": 50, "fill": "$alias", "layout": "none"}`)
	p := insert(t, e, doc.Root, `{"type": "frame", "name": "P", "x": 300, "width": 200, "height": 100, "layout": "none"}`)
	insert(t, e, p, `{"type": "ref", "ref": "`+comp+`", "x": 10, "y": 10}`)
	clip := Copy(e, []string{p})
	html := jsonv.StrOr(jsonv.Get(clip, "html"))
	if !strings.Contains(html, mark) {
		t.Fatal(html)
	}
	// In place beside the visible source, same document: nothing imported.
	out, err := Paste(e, html, "", nil, ptr(rect(0, 0, 1000, 800)))
	if err != nil || len(out) != 1 || parentOf(e, out[0]) != doc.Root {
		t.Fatal(out, err)
	}
	e.LoadTemplate("empty")
	out, err = Paste(e, html, "", nil, ptr(rect(0, 0, 1000, 800)))
	if err != nil || len(out) != 1 {
		t.Fatal(out, err)
	}
	vars := jsonv.Obj(jsonv.Get(e.Doc.Root, "variables"))
	if vars == nil || !vars.Has("brand") || !vars.Has("alias") || e.Doc.Find(comp) == nil {
		t.Fatalf("variables and components follow: %s", jsonv.Marshal(e.Doc.Root))
	}
	if !slices.Contains(jsonv.Items(jsonv.Get(jsonv.Get(e.Doc.Root, "themes"), "mode")), jsonv.Value("dark")) {
		t.Fatal("theme axes follow")
	}
	e.Undo()
	if vars := jsonv.Obj(jsonv.Get(e.Doc.Root, "variables")); len(e.Doc.Children()) != 0 || vars != nil && vars.Len() != 0 {
		t.Fatalf("one undo takes it all out: %s", jsonv.Marshal(e.Doc.Root))
	}
	if _, err := Paste(e, `<span `+mark+`="@@@">`, "", nil, nil); err == nil || err.Error() != "bad clipboard data: Invalid symbol 64, offset 0." {
		t.Fatal(err)
	}
}

func TestReorderAndMove(t *testing.T) {
	e := newEngine(t)
	f := insert(t, e, doc.Root, `{"type": "frame", "x": 0, "y": 0, "width": 400, "height": 300, "layout": "none"}`)
	var ids []string
	for i := range 4 {
		ids = append(ids, insert(t, e, f, fmt.Sprintf(`{"type": "rectangle", "name": "%c", "width": 10, "height": 10}`, 'A'+i)))
	}
	order := func() string {
		var s string
		for _, k := range kids(e.Doc, f) {
			s += jsonv.StrOr(jsonv.Get(k, "name"))
		}
		return s
	}
	for _, step := range []struct {
		ids  []int
		dir  string
		want string
	}{{[]int{2, 3}, "up", "ABCD"}, {[]int{0, 1}, "down", "ABCD"}, {[]int{1, 2}, "up", "ADBC"}, {[]int{0}, "front", "DBCA"}, {[]int{0}, "back", "ADBC"}} {
		var sel []string
		for _, i := range step.ids {
			sel = append(sel, ids[i])
		}
		if err := e.Reorder(sel, step.dir); err != nil {
			t.Fatal(err)
		}
		if order() != step.want {
			t.Fatalf("%s %v: %s, want %s", step.dir, step.ids, order(), step.want)
		}
	}
	// Coalesced moves are one undo step; each stores two decimals (1.004 lands as 1).
	for range 3 {
		if _, err := moveBy(e, jsonv.MustParse(`{"ids": ["`+ids[0]+`"], "dx": 1.004, "coalesce": "n"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if x := jsonv.Get(e.Doc.Find(ids[0]), "x"); string(jsonv.Marshal(x)) != "3" {
		t.Fatal(string(jsonv.Marshal(x)))
	}
	e.Undo()
	if jsonv.Obj(e.Doc.Find(ids[0])).Has("x") {
		t.Fatal("one undo for the coalesced moves")
	}
}

// png renders like export's NodePNG; its bytes depend on the PNG encoder, so the replay checks only its errors.
func TestPNGCommand(t *testing.T) {
	e := newEngine(t)
	f := insert(t, e, doc.Root, `{"type": "frame", "x": 0, "y": 0, "width": 40, "height": 30, "layout": "none", "fill": "#ff0000"}`)
	r := engine.NewReplayer(e)
	line, err := r.Line(`{"cmd":"png","id":"` + f + `","scale":2}`)
	if err != nil || !jsonv.IsTrue(jsonv.Get(line, "ok")) {
		t.Fatal(err, string(jsonv.Marshal(line)))
	}
	b, _ := base64.StdEncoding.DecodeString(jsonv.StrOr(jsonv.Get(line, "data")))
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 80 || img.Bounds().Dy() != 60 {
		t.Fatal(err)
	}
	want, _ := (&export.Ctx{Doc: e.Doc, Fonts: e.Fonts, Assets: e.Assets}).NodePNG(f, 2)
	if !bytes.Equal(b, want) {
		t.Fatal("png is export's NodePNG")
	}
}

// Every editor command reaches the dispatcher.
func TestCommandsRegistered(t *testing.T) {
	e := newEngine(t)
	r := engine.NewReplayer(e)
	for _, cmd := range []string{"bounds", "move_by", "duplicate", "reparent", "snap_targets", "nodes_in_rect", "drop_target", "wrap", "unwrap",
		"add_layout", "set_layout", "remove_layout", "align", "copy", "paste", "path_edit", "path_write", "layers", "reorder", "node", "hit",
		"boxes", "children_boxes", "frames", "live", "variables", "font_state", "png"} {
		line, err := r.Line(`{"cmd":"` + cmd + `"}`)
		if err != nil || strings.Contains(jsonv.StrOr(jsonv.Get(line, "error")), "unknown command") {
			t.Errorf("%s: %v %s", cmd, err, jsonv.Marshal(line))
		}
	}
}

func rect(x, y, w, h float64) Rect { return Rect{X: x, Y: y, W: w, H: h} }

func ptr(r Rect) *Rect { return &r }
