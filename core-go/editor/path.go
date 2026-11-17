package editor

// Pen tool and vector editing: the canvas edits anchors in canvas coordinates; these map them to and from a
// path's geometry.

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/libm"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

type point = [2]float64

// anchor is one anchor; its handles are absolute points (nil: none).
type anchor struct {
	p   point
	in  *point
	out *point
}

type contour struct {
	anchors []anchor
	closed  bool
}

func mapPoint(m Affine, p point) point {
	x, y := apply(m, p[0], p[1])
	return point{x, y}
}

func (a anchor) mapped(m Affine) anchor {
	out := anchor{p: mapPoint(m, a.p)}
	if a.in != nil {
		h := mapPoint(m, *a.in)
		out.in = &h
	}
	if a.out != nil {
		h := mapPoint(m, *a.out)
		out.out = &h
	}
	return out
}

func mapContours(cs []contour, m Affine) []contour {
	out := make([]contour, len(cs))
	for i, c := range cs {
		out[i] = contour{make([]anchor, len(c.anchors)), c.closed}
		for k, a := range c.anchors {
			out[i].anchors[k] = a.mapped(m)
		}
	}
	return out
}

// contoursOf reads path data as contours: quadratics become cubics, a handle equal to its anchor is no handle,
// and a closing point on the start merges into it.
func contoursOf(d string) []contour {
	far := func(a, b point) bool { return math.Abs(a[0]-b[0]) > 1e-6 || math.Abs(a[1]-b[1]) > 1e-6 }
	var out []contour
	var cur, start point
	open := false
	curve := func(c1, c2, to, from point) {
		if !open {
			out = append(out, contour{anchors: []anchor{{p: start}}})
			open = true
		}
		c := &out[len(out)-1]
		if far(c1, from) {
			h := c1
			c.anchors[len(c.anchors)-1].out = &h
		}
		a := anchor{p: to}
		if far(c2, to) {
			h := c2
			a.in = &h
		}
		c.anchors = append(c.anchors, a)
	}
	for _, s := range usvg.SimplifyPath(d) {
		switch s.Kind {
		case 'M':
			p := point{s.X, s.Y}
			out = append(out, contour{anchors: []anchor{{p: p}}})
			cur, start, open = p, p, true
		case 'L':
			p := point{s.X, s.Y}
			curve(cur, p, p, cur)
			cur = p
		case 'C':
			p := point{s.X, s.Y}
			curve(point{s.X1, s.Y1}, point{s.X2, s.Y2}, p, cur)
			cur = p
		case 'Q':
			k := 2.0 / 3.0
			c1 := point{cur[0] + float64(k*(s.X1-cur[0])), cur[1] + float64(k*(s.Y1-cur[1]))}
			c2 := point{s.X + float64(k*(s.X1-s.X)), s.Y + float64(k*(s.Y1-s.Y))}
			p := point{s.X, s.Y}
			curve(c1, c2, p, cur)
			cur = p
		case 'Z':
			if open && len(out) > 0 {
				c := &out[len(out)-1]
				if n := len(c.anchors); n > 1 && !far(c.anchors[0].p, c.anchors[n-1].p) {
					c.anchors[0].in = c.anchors[n-1].in
					c.anchors = c.anchors[:n-1]
				}
				c.closed = true
			}
			open, cur = false, start
		}
	}
	return out
}

// drawable: enough to draw, two anchors or one closed onto itself with a handle.
func drawable(c contour) bool {
	return len(c.anchors) >= 2 || (c.closed && len(c.anchors) == 1 && (c.anchors[0].out != nil || c.anchors[0].in != nil))
}

// pathD writes contours as path data, straight where both facing handles are absent.
func pathD(contours []contour) string {
	n := func(v float64) string {
		r := math.Round(float64(v*1000)) / 1000
		if r == 0 {
			return "0"
		}
		return engine.RustF64(r)
	}
	var out []string
	seg := func(a, b anchor) {
		if a.out != nil || b.in != nil {
			c1, c2 := a.p, b.p
			if a.out != nil {
				c1 = *a.out
			}
			if b.in != nil {
				c2 = *b.in
			}
			out = append(out, fmt.Sprintf("C%s %s %s %s %s %s", n(c1[0]), n(c1[1]), n(c2[0]), n(c2[1]), n(b.p[0]), n(b.p[1])))
		} else {
			out = append(out, fmt.Sprintf("L%s %s", n(b.p[0]), n(b.p[1])))
		}
	}
	for _, c := range contours {
		if len(c.anchors) == 0 {
			continue
		}
		first := c.anchors[0]
		out = append(out, fmt.Sprintf("M%s %s", n(first.p[0]), n(first.p[1])))
		for i := 0; i+1 < len(c.anchors); i++ {
			seg(c.anchors[i], c.anchors[i+1])
		}
		if c.closed && drawable(c) {
			seg(c.anchors[len(c.anchors)-1], first)
			out = append(out, "Z")
		}
	}
	return strings.Join(out, " ")
}

// geometryToCanvas maps geometry to the canvas: the viewBox (default: tight bounds) stretched onto the box,
// then the painted rotation/flips (as svg.rs draws it).
func geometryToCanvas(n jsonv.Value, f *render.Flat) Affine {
	var vb svg.Rect
	if a := jsonv.Arr(jsonv.Get(n, "viewBox")); a != nil && len(a.Items) == 4 {
		or := func(v jsonv.Value, d float64) float64 {
			if f, ok := jsonv.F64(v); ok {
				return f
			}
			return d
		}
		vb = svg.Rect{X: or(a.Items[0], 0), Y: or(a.Items[1], 0), W: or(a.Items[2], 1), H: or(a.Items[3], 1)}
	} else {
		vb, _ = svg.PathBBox(jsonv.StrOr(jsonv.Get(n, "geometry")))
	}
	sx, sy := 1.0, 1.0
	if vb.W > 0 {
		sx = f.W / vb.W
	}
	if vb.H > 0 {
		sy = f.H / vb.H
	}
	return compose(matrix(f), Affine{sx, 0, 0, sy, f.X - float64(vb.X*sx), f.Y - float64(vb.Y*sy)})
}

func pointJSON(p *point) jsonv.Value {
	if p == nil {
		return nil
	}
	return jsonv.NewArray(jsonv.Float(p[0]), jsonv.Float(p[1]))
}

func contoursJSON(cs []contour) *jsonv.Array {
	out := jsonv.NewArray()
	out.Items = []jsonv.Value{}
	for _, c := range cs {
		anchors := jsonv.NewArray()
		anchors.Items = []jsonv.Value{}
		for _, a := range c.anchors {
			o := engine.Object("x", jsonv.Float(a.p[0]), "y", jsonv.Float(a.p[1]))
			if a.in != nil {
				o.Set("in", pointJSON(a.in))
			}
			if a.out != nil {
				o.Set("out", pointJSON(a.out))
			}
			anchors.Items = append(anchors.Items, o)
		}
		out.Items = append(out.Items, engine.Object("closed", c.closed, "anchors", anchors))
	}
	return out
}

func finite(v jsonv.Value) (float64, bool) {
	f, ok := jsonv.F64(v)
	return f, ok && !math.IsInf(f, 0) && !math.IsNaN(f)
}

func contoursFromJSON(v jsonv.Value) ([]contour, error) {
	pt := func(v jsonv.Value) *point {
		items := jsonv.Items(v)
		if len(items) < 2 {
			return nil
		}
		x, ok := finite(items[0])
		y, ok2 := finite(items[1])
		if !ok || !ok2 {
			return nil
		}
		return &point{x, y}
	}
	list := jsonv.Arr(v)
	if list == nil {
		return nil, errors.New("contours required")
	}
	out := []contour{}
	for _, c := range list.Items {
		as := jsonv.Arr(jsonv.Get(c, "anchors"))
		if as == nil {
			return nil, errors.New("anchors required")
		}
		var anchors []anchor
		for _, a := range as.Items {
			x, ok := finite(jsonv.Get(a, "x"))
			if !ok {
				return nil, errors.New("bad anchor")
			}
			y, ok := finite(jsonv.Get(a, "y"))
			if !ok {
				return nil, errors.New("bad anchor")
			}
			anchors = append(anchors, anchor{p: point{x, y}, in: pt(jsonv.Get(a, "in")), out: pt(jsonv.Get(a, "out"))})
		}
		out = append(out, contour{anchors, jsonv.IsTrue(jsonv.Get(c, "closed"))})
	}
	return out, nil
}

// PathEdit is a path's contours in canvas coordinates, for the vector editor.
func PathEdit(e *engine.Engine, id string) (jsonv.Value, error) {
	e.Sync()
	n := e.Doc.Find(id)
	if n == nil {
		return nil, fmt.Errorf("node not found: %s", id)
	}
	if !jsonv.StrIs(jsonv.Get(n, "type"), "path") || strings.Contains(id, "/") {
		return nil, errors.New("only paths can be edited point by point")
	}
	f := e.Renderer.Find(id)
	if f == nil {
		return nil, fmt.Errorf("node not found: %s", id)
	}
	m := geometryToCanvas(n, f)
	return contoursJSON(mapContours(contoursOf(jsonv.StrOr(jsonv.Get(n, "geometry"))), m)), nil
}

// fine keeps 6 decimals of a box number: coarser rounding would rescale the geometry.
func fine(v float64) jsonv.Value { return jsonv.Float(math.Round(float64(v*1e6)) / 1e6) }

// PathWrite writes contours given in canvas coordinates. Into path id: the geometry keeps its coordinate
// space and the box follows the tight bounds (unless the path has an explicit viewBox), so nothing moves on
// the canvas. Without an id (hasID false): a new path (black 1 px stroke) under parent, outside its auto layout. It
// returns the path's id.
func PathWrite(e *engine.Engine, id string, hasID bool, parent string, contours jsonv.Value) (string, error) {
	all, err := contoursFromJSON(contours)
	if err != nil {
		return "", err
	}
	var cs []contour
	for _, c := range all {
		if drawable(c) {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return "", errors.New("a path needs two points")
	}
	e.Sync()
	if !hasID {
		v := newView(e)
		if parent == doc.Root || !v.acceptsChildren(parent) || v.ty(parent) != "frame" {
			parent = doc.Root
		}
		// Canvas → the parent's own coordinates.
		pm := upright
		if f := v.flat(parent); f != nil {
			var ok bool
			if pm, ok = invert(compose(matrix(f), Affine{1, 0, 0, 1, f.X, f.Y})); !ok {
				return "", errors.New("parent is not invertible")
			}
		}
		b, ok := svg.PathBBox(pathD(mapContours(cs, pm)))
		if !ok {
			return "", errors.New("empty path")
		}
		geometry := pathD(mapContours(cs, compose(Affine{1, 0, 0, 1, -b.X, -b.Y}, pm)))
		node := engine.Object("type", "path", "name", "Path", "x", fine(b.X), "y", fine(b.Y), "width", fine(b.W), "height", fine(b.H),
			"geometry", geometry, "stroke", "#000000", "strokeWidth", jsonv.Int(1))
		if v.hasLayout(parent) {
			node.Set("layoutPosition", "absolute")
		}
		var out string
		err := e.EditOrDrag(func(d *doc.Doc) (err error) {
			out, err = d.Insert(parent, node, -1)
			return err
		})
		return out, err
	}
	n := e.Doc.Find(id)
	if n == nil {
		return "", fmt.Errorf("node not found: %s", id)
	}
	if !jsonv.StrIs(jsonv.Get(n, "type"), "path") {
		return "", errors.New("only paths can be edited point by point")
	}
	f := e.Renderer.Find(id)
	if f == nil {
		return "", fmt.Errorf("node not found: %s", id)
	}
	inv, ok := invert(geometryToCanvas(n, f))
	if !ok {
		return "", errors.New("the path is collapsed")
	}
	geometry := pathD(mapContours(cs, inv))
	props := engine.Object("geometry", geometry)
	if jsonv.Arr(jsonv.Get(n, "viewBox")) == nil {
		// The box follows the geometry's new tight bounds at the current scale.
		old, _ := svg.PathBBox(jsonv.StrOr(jsonv.Get(n, "geometry")))
		nw, ok := svg.PathBBox(geometry)
		if !ok {
			return "", errors.New("empty path")
		}
		sx, sy := 1.0, 1.0
		if old.W > 0 {
			sx = f.W / old.W
		}
		if old.H > 0 {
			sy = f.H / old.H
		}
		ox, oy := float64((nw.X-old.X)*sx), float64((nw.Y-old.Y)*sy)
		if math.Abs(ox) > 1e-6 || math.Abs(oy) > 1e-6 {
			// The origin moves along the node's own rotation/flips.
			rot, _ := jsonv.F64(jsonv.Get(n, "rotation"))
			t := -toRadians(rot)
			fx, fy := 1.0, 1.0
			if jsonv.IsTrue(jsonv.Get(n, "flipX")) {
				fx = -1
			}
			if jsonv.IsTrue(jsonv.Get(n, "flipY")) {
				fy = -1
			}
			sin, cos := libm.SinCos(t)
			dx := float64(float64(cos*fx)*ox) - float64(float64(sin*fy)*oy)
			dy := float64(float64(sin*fx)*ox) + float64(float64(cos*fy)*oy)
			x, _ := jsonv.F64(jsonv.Get(n, "x"))
			y, _ := jsonv.F64(jsonv.Get(n, "y"))
			props.Set("x", fine(x+dx))
			props.Set("y", fine(y+dy))
		}
		props.Set("width", fine(float64(nw.W*sx)))
		props.Set("height", fine(float64(nw.H*sy)))
	}
	err = e.EditOrDrag(func(d *doc.Doc) error { return d.Update(id, props) })
	if err != nil {
		return "", err
	}
	return id, nil
}
