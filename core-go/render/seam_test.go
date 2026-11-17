package render

import (
	"slices"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// The rasterizer flattens a curve cut at a pixmap edge piece by piece, so a repaint cut elsewhere than the
// full paint moved antialiased edges by up to ~30 levels. After edits (moved there and back) the canvas
// equals a full render (solo) of the same document: around a rotated, flipped shadow (it moves up, so its
// whole layer repaints), and around an unchanged ellipse that a small moved rectangle's repaint cuts.
func TestRepaintMatchesFullRender(t *testing.T) {
	docs := []string{
		`{"type":"ellipse","id":"e","x":100,"y":200,"width":120,"height":80,"fill":"#ff00ff80","flipY":true,"rotation":45,
		  "effect":{"type":"shadow","offset":{"x":10,"y":10},"blur":4,"color":"#00000080"}}`,
		`{"type":"rectangle","id":"e","x":40,"y":180,"width":60,"height":40,"fill":"#0000ff"},
		 {"type":"ellipse","id":"o","x":60,"y":40,"width":290,"height":330,"fill":"#ff000080"}`,
	}
	white := [3]uint8{255, 255, 255}
	for i, kids := range docs {
		for _, threads := range []int{1, 4, 16} {
			d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"root","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#eeeeee","children":[`+kids+`]}]}`)
			cx := offline(t, d)
			r := &Renderer{Threads: threads}
			r.Sync(cx)
			fr := func(solo string) []byte { return slices.Clone(r.Render(400, 400, 0, 0, 1, white, solo, true)) }
			fr("")
			x, _ := jsonv.F64(jsonv.Get(node(d, 0, 0), "x"))
			for _, dx := range []float64{1, 0} {
				node(d, 0, 0).Set("x", jsonv.Float(x+dx))
				r.Sync(cx)
			}
			live := fr("")
			full := fr("root")
			worst := 0
			for j := range live {
				worst = max(worst, abs8(live[j], full[j]))
			}
			if worst > 1 {
				t.Errorf("doc %d, %d threads: the edited canvas differs from a full render by up to %d", i, threads, worst)
			}
		}
	}
}

func abs8(a, b byte) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// A repaint cuts the bands the cached view's full paint used, even when the thread count changed since.
func TestRepaintKeepsTheViewsBands(t *testing.T) {
	js := `{"version":"2.19","children":[{"type":"frame","id":"root","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#eeeeee","children":[
		{"type":"ellipse","id":"e","x":100,"y":200,"width":120,"height":80,"fill":"#ff00ff80","flipY":true,"rotation":45,
		 "effect":{"type":"shadow","offset":{"x":10,"y":10},"blur":4,"color":"#00000080"}}]}]}`
	white := [3]uint8{255, 255, 255}
	d := openJSON(t, js)
	cx := offline(t, d)
	r := &Renderer{Threads: 16}
	r.Sync(cx)
	r.Render(400, 400, 0, 0, 1, white, "", true)
	r.Threads = 1
	for _, x := range []int64{101, 100} {
		node(d, 0, 0).Set("x", jsonv.Int(x))
		r.Sync(cx)
	}
	live := slices.Clone(r.Render(400, 400, 0, 0, 1, white, "", true))
	c := &Renderer{Threads: 16}
	c.Sync(cx)
	full := c.Render(400, 400, 0, 0, 1, white, "", true)
	worst := 0
	for j := range live {
		worst = max(worst, abs8(live[j], full[j]))
	}
	if worst > 1 {
		t.Errorf("a repaint after the thread count changed differs from the view's full paint by up to %d", worst)
	}
}
