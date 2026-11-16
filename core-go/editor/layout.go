package editor

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/libm"
)

// Flex is the Shift+A guess for a freeform frame's children.
type Flex struct {
	// Dir is 0 for horizontal, 1 for vertical.
	Dir     int
	Gap     float64
	Align   string
	Justify string // "" for none
	// Padding is [top, right, bottom, left].
	Padding [4]float64
	// Order is the child indices in layout order.
	Order []int
	// Fill is the children that fill the cross axis (then the frame keeps a fixed cross size).
	Fill []int
}

// Kid is a child's box and whether it is a text, for InferFlex.
type Kid struct {
	Box  Rect
	Text bool
}

// sum is Iterator::sum over f64 (from -0.0).
func sum(vals []float64) float64 {
	s := math.Copysign(0, -1)
	for _, v := range vals {
		s += v
	}
	return s
}

// InferFlex infers direction, gap, alignment, padding and cross-axis fills from the children's boxes. frame
// is nil when a new frame is wrapped around them.
func InferFlex(frame *Rect, kids []Kid) Flex {
	f := Flex{Dir: 1, Gap: 10, Align: "start", Padding: [4]float64{10, 10, 10, 10}, Order: make([]int, len(kids)), Fill: []int{}}
	for i := range f.Order {
		f.Order[i] = i
	}
	boxes := make([]Rect, len(kids))
	for i, k := range kids {
		boxes[i] = k.Box
	}
	r, ok := union(boxes)
	if !ok {
		return f
	}
	edges := func(fr Rect) [4]float64 {
		p := [4]float64{r.Y - fr.Y, fr.X + fr.W - r.X - r.W, fr.Y + fr.H - r.Y - r.H, r.X - fr.X}
		for i := range p {
			p[i] = math.Round(fmax(p[i], 0))
		}
		return p
	}
	if len(kids) == 1 && kids[0].Text {
		f.Align, f.Justify = "center", "center"
		if frame != nil {
			p := edges(*frame)
			v, h := fmin(p[0], p[2]), fmin(p[1], p[3])
			f.Padding = [4]float64{v, h, v, h}
		}
		return f
	}
	ws, hs := make([]float64, len(kids)), make([]float64, len(kids))
	for i, k := range kids {
		ws[i], hs[i] = k.Box.W, k.Box.H
	}
	overX, overY := sum(ws) > r.W+0.5, sum(hs) > r.H+0.5
	spread := func(c func(Rect) float64) float64 {
		hi, lo := -math.MaxFloat64, math.MaxFloat64
		for _, k := range kids {
			hi, lo = fmax(hi, c(k.Box)), fmin(lo, c(k.Box))
		}
		return hi - lo
	}
	switch {
	case !overX && overY:
		f.Dir = 0
	case !overY && overX:
		f.Dir = 1
	case spread(func(b Rect) float64 { return b.X + b.W/2 }) > spread(func(b Rect) float64 { return b.Y + b.H/2 }):
		f.Dir = 0
	default:
		f.Dir = 1
	}
	e, c := f.Dir, 1-f.Dir
	lo := func(b Rect, a int) float64 {
		if a == 0 {
			return b.X
		}
		return b.Y
	}
	size := func(b Rect, a int) float64 {
		if a == 0 {
			return b.W
		}
		return b.H
	}
	slices.SortStableFunc(f.Order, func(i, j int) int { return libm.TotalCmp(lo(kids[i].Box, e), lo(kids[j].Box, e)) })
	var gaps []float64
	for w := 0; w+1 < len(f.Order); w++ {
		a, b := kids[f.Order[w]].Box, kids[f.Order[w+1]].Box
		if g := lo(b, e) - lo(a, e) - size(a, e); g > 0 {
			gaps = append(gaps, g)
		}
	}
	slices.SortStableFunc(gaps, libm.TotalCmp)
	if len(gaps) > 0 {
		f.Gap = math.Round(gaps[len(gaps)/2])
	}
	dev := func(pick func(Rect) float64) float64 {
		d := make([]float64, len(kids))
		for i, k := range kids {
			d[i] = math.Abs(pick(k.Box) - pick(r))
		}
		return sum(d)
	}
	devs := [3]float64{
		dev(func(b Rect) float64 { return lo(b, c) }),
		dev(func(b Rect) float64 { return lo(b, c) + size(b, c)/2 }),
		dev(func(b Rect) float64 { return lo(b, c) + size(b, c) }),
	}
	best := 0
	for i := range 3 {
		if devs[i] < devs[best] {
			best = i
		}
	}
	f.Align = [3]string{"start", "center", "end"}[best]
	var full []int
	for i, k := range kids {
		if math.Abs(size(k.Box, c)-size(r, c)) <= 0.5 {
			full = append(full, i)
		}
	}
	if len(full) >= 2 {
		f.Fill = full
	}
	switch {
	case frame != nil:
		f.Padding = edges(*frame)
	case len(kids) == 1:
		f.Padding = [4]float64{10, 10, 10, 10}
	default:
		f.Padding = [4]float64{}
	}
	return f
}

func paddingValue(p [4]float64) jsonv.Value {
	switch {
	case p[1] == p[0] && p[2] == p[0] && p[3] == p[0]:
		return Num(p[0])
	case p[0] == p[2] && p[1] == p[3]:
		return jsonv.NewArray(Num(p[0]), Num(p[1]))
	}
	return jsonv.NewArray(Num(p[0]), Num(p[1]), Num(p[2]), Num(p[3]))
}

// AddLayout is Shift+A: it gives a freeform frame auto layout in place, or wraps the selection in a new
// auto-layout frame, and returns the frame's id.
func AddLayout(e *engine.Engine, ids []string) (string, error) {
	inPlace := false
	if len(ids) == 1 {
		n := e.Doc.Find(ids[0])
		inPlace = n != nil && jsonv.StrIs(jsonv.Get(n, "type"), "frame") && jsonv.StrIs(jsonv.Get(n, "layout"), "none")
	}
	var frame string
	err := e.Batch(func(e *engine.Engine) error {
		if inPlace {
			frame = ids[0]
		} else {
			var err error
			if frame, err = Wrap(e, ids, "frame"); err != nil {
				return err
			}
		}
		e.Sync()
		v := newView(e)
		fr, ok := v.rect(frame)
		if !ok {
			return errors.New("frame not found")
		}
		type shown struct {
			id  string
			kid Kid
		}
		var list []shown
		var boxes []Kid
		for _, k := range kids(e.Doc, frame) {
			id, ok := jsonv.Str(jsonv.Get(k, "id"))
			if !ok {
				continue
			}
			b, ok := v.rect(id)
			if !ok {
				continue
			}
			kid := Kid{b, jsonv.StrIs(jsonv.Get(k, "type"), "text")}
			list = append(list, shown{id, kid})
			boxes = append(boxes, kid)
		}
		var in *Rect
		if inPlace {
			in = &fr
		}
		f := InferFlex(in, boxes)
		c := "width"
		dir := "vertical"
		if f.Dir == 0 {
			c, dir = "height", "horizontal"
		}
		fp := engine.Object("layout", dir, "gap", Num(f.Gap), "padding", paddingValue(f.Padding), "width", "fit_content", "height", "fit_content")
		if f.Align != "start" {
			fp.Set("alignItems", f.Align)
		}
		if f.Justify != "" {
			fp.Set("justifyContent", f.Justify)
		}
		if len(f.Fill) > 0 {
			if c == "width" {
				fp.Set(c, Num(fr.W))
			} else {
				fp.Set(c, Num(fr.H))
			}
		}
		if !inPlace && len(boxes) == 1 {
			n := v.doc.Find(frame)
			ox, _ := jsonv.F64(jsonv.Get(n, "x"))
			oy, _ := jsonv.F64(jsonv.Get(n, "y"))
			fp.Set("x", Num(ox-f.Padding[3]))
			fp.Set("y", Num(oy-f.Padding[0]))
		}
		var kp []move
		for _, i := range f.Fill {
			s := list[i]
			p := jsonv.NewObject()
			p.Set(c, "fill_container")
			if s.kid.Text {
				if c == "width" {
					p.Set("textGrowth", "fixed-width")
				} else {
					p.Set("textGrowth", "fixed-width-height")
					p.Set("width", Num(s.kid.Box.W))
				}
			}
			kp = append(kp, move{s.id, p})
		}
		order := make([]string, len(f.Order))
		for k, i := range f.Order {
			order[k] = list[i].id
		}
		return e.EditOrDrag(func(d *doc.Doc) error {
			if err := d.Update(frame, fp); err != nil {
				return err
			}
			for k, id := range order {
				if err := d.MoveNode(id, frame, k); err != nil {
					return err
				}
			}
			for _, m := range kp {
				if err := d.Update(m.id, m.props); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	return frame, nil
}

// SetLayout switches auto-layout frames between row and column. A child's `fill_container` on the old cross
// axis would now split the main axis (shrinking to a sliver), so it is frozen at its current size first.
func SetLayout(e *engine.Engine, ids []string, layout string) error {
	e.Sync()
	var main string
	switch layout {
	case "vertical":
		main = "height"
	case "horizontal":
		main = "width"
	default:
		return errors.New("layout must be horizontal or vertical")
	}
	v := newView(e)
	var plan []move
	for _, id := range ids {
		dir, ok := v.layout(id)
		if !ok {
			return fmt.Errorf("%s is not an auto layout frame", id)
		}
		plan = append(plan, move{id, engine.Object("layout", layout)})
		want := 0
		if layout == "vertical" {
			want = 1
		}
		if dir == want {
			continue
		}
		for _, k := range kids(e.Doc, id) {
			if jsonv.StrIs(jsonv.Get(k, "layoutPosition"), "absolute") {
				continue
			}
			kid := idOf(k)
			frozen := v.freezeFill(kid)
			p := jsonv.NewObject()
			if val, ok := frozen.Get(main); ok {
				p.Set(main, val)
			}
			if p.Len() > 0 {
				plan = append(plan, move{kid, p})
			}
		}
	}
	return updateAll(e, plan)
}

// RemoveLayout is Shift+Alt+A: back to freeform, children staying where the layout put them.
func RemoveLayout(e *engine.Engine, ids []string) error {
	e.Sync()
	v := newView(e)
	var plan []move
	for _, id := range ids {
		if !v.hasLayout(id) {
			continue
		}
		fox, foy, ok := v.origin(id)
		fr, ok2 := v.rect(id)
		if !ok || !ok2 {
			continue
		}
		plan = append(plan, move{id, engine.Object("layout", "none", "width", Num(fr.W), "height", Num(fr.H))})
		for _, k := range kids(e.Doc, id) {
			kid := idOf(k)
			p := v.freezeFill(kid)
			if ox, oy, ok := v.origin(kid); ok && !jsonv.StrIs(jsonv.Get(k, "layoutPosition"), "absolute") {
				p.Set("x", Num(ox-fox))
				p.Set("y", Num(oy-foy))
			}
			plan = append(plan, move{kid, p})
		}
	}
	return updateAll(e, plan)
}

// updateAll applies each move's properties, one by one, as one edit.
func updateAll(e *engine.Engine, plan []move) error {
	return e.EditOrDrag(func(d *doc.Doc) error {
		for _, m := range plan {
			if err := d.Update(m.id, jsonv.Clone(m.props)); err != nil {
				return err
			}
		}
		return nil
	})
}
