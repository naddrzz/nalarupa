// Package layout is the flex layout (core/src/layout.rs), run as five passes: fit (H), fill (H),
// fit (V), fill (V), position. Single axis, no wrapping. Fill children never count toward a fit_content
// size, fit_content keeps its fallback when nothing can size it, rotated/flipped children are placed by
// their bounding box and groups are the union of their children. Writes absolute boxes into the RNode tree.
package layout

import (
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/libm"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

type sizing uint8

const (
	fixed sizing = iota
	fit
	fill
)

// fmax and fmin are Rust's f64::max/min: a NaN argument yields the other one.
func fmax(a, b float64) float64 {
	if math.IsNaN(a) || b > a {
		return b
	}
	return a
}

func fmin(a, b float64) float64 {
	if math.IsNaN(a) || b < a {
		return b
	}
	return a
}

// parseSpec reads a sizing mode and initial size: a number, or `fit_content(N)` / `fill_container(N)`
// with fallback N (default 0).
func parseSpec(v jsonv.Value, present bool) (sizing, float64, bool) {
	if !present {
		return 0, 0, false
	}
	if n, ok := resolve.Num(v); ok {
		return fixed, fmax(n, 0), true
	}
	s, ok := jsonv.Str(v)
	if !ok {
		return 0, 0, false
	}
	fb := 0.0
	if i := strings.IndexByte(s, '('); i >= 0 {
		if f, ok := resolve.ParseFloat(strings.TrimSpace(strings.TrimRight(s[i+1:], ")"))); ok {
			fb = f
		}
	}
	fb = fmax(fb, 0)
	switch {
	case strings.HasPrefix(s, "fit_content"):
		return fit, fb, true
	case strings.HasPrefix(s, "fill_container"):
		return fill, fb, true
	}
	return 0, 0, false
}

func spec(n *resolve.RNode, ax int) (sizing, float64) {
	key := "width"
	if ax == 1 {
		key = "height"
	}
	v, present := n.P.Get(key)
	if s, size, ok := parseSpec(v, present); ok {
		return s, size
	}
	switch n.Type {
	case "frame":
		return fit, 0
	case "note", "prompt", "context":
		return fixed, []float64{240, 140}[ax]
	case "browser":
		return fixed, []float64{1280, 800}[ax]
	}
	return fixed, 0
}

func sizingOf(n *resolve.RNode, ax int) sizing {
	s, _ := spec(n, ax)
	return s
}

// dir is the main axis (0 = horizontal, 1 = vertical). Only frames with a layout have one; groups never do.
func dir(n *resolve.RNode) (int, bool) {
	if n.Type != "frame" {
		return 0, false
	}
	switch l, _ := n.S("layout"); l {
	case "none":
		return 0, false
	case "vertical":
		return 1, true
	}
	return 0, true
}

// Padding is [top, right, bottom, left]: a number, [vertical, horizontal] or four values (anything else is 0).
func Padding(n *resolve.RNode) [4]float64 {
	v, ok := n.P.Get("padding")
	if !ok {
		return [4]float64{}
	}
	num := func(v jsonv.Value) float64 {
		f, _ := resolve.Num(v)
		return f
	}
	if a := jsonv.Arr(v); a != nil {
		switch len(a.Items) {
		case 2:
			vv, h := num(a.Items[0]), num(a.Items[1])
			return [4]float64{vv, h, vv, h}
		case 4:
			return [4]float64{num(a.Items[0]), num(a.Items[1]), num(a.Items[2]), num(a.Items[3])}
		}
		return [4]float64{}
	}
	p := num(v)
	return [4]float64{p, p, p, p}
}

func pad(n *resolve.RNode, ax int) float64 {
	p := Padding(n)
	if ax == 0 {
		return p[1] + p[3]
	}
	return p[0] + p[2]
}

// gap is the main-axis gap; space_between/space_around ignore it everywhere (hug, fill share, placement).
func gap(n *resolve.RNode) float64 {
	if j, _ := n.S("justifyContent"); j == "space_between" || j == "space_around" {
		return 0
	}
	g, _ := n.F("gap")
	return g
}

func inFlow(n *resolve.RNode) bool {
	p, _ := n.S("layoutPosition")
	return p != "absolute"
}

func size(n *resolve.RNode, ax int) float64 {
	if ax == 0 {
		return n.W
	}
	return n.H
}

// pi is a variable so pi/180 rounds in float64, as Rust's to_radians does.
var pi = math.Pi

// LocalAABB is the bounding box [x0, y0, x1, y1] of the node's local bounds under its rotation and
// flips, relative to its origin (flip about the origin, then rotate CCW).
func LocalAABB(n *resolve.RNode) [4]float64 {
	rot, _ := n.F("rotation")
	r := -rot * (pi / 180)
	sx, sy := 1.0, 1.0
	if n.B("flipX") {
		sx = -1
	}
	if n.B("flipY") {
		sy = -1
	}
	sin, cos := libm.SinCos(r)
	a, b, c, d := cos*sx, sin*sx, -sin*sy, cos*sy
	o := [4]float64{math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64}
	for _, p := range [][2]float64{{n.BX, n.BY}, {n.BX + n.W, n.BY}, {n.BX, n.BY + n.H}, {n.BX + n.W, n.BY + n.H}} {
		x, y := float64(a*p[0])+float64(c*p[1]), float64(b*p[0])+float64(d*p[1])
		o = [4]float64{fmin(o[0], x), fmin(o[1], y), fmax(o[2], x), fmax(o[3], y)}
	}
	return o
}

func outer(n *resolve.RNode, ax int) float64 {
	b := LocalAABB(n)
	return b[ax+2] - b[ax]
}

// Layout lays out one top-level node (and its subtree) at its own x/y.
func Layout(root *resolve.RNode, m TextMeasurer) {
	initNode(root, m)
	for ax := range 2 {
		fitPass(root, ax, m)
		fillPass(root, ax, m)
	}
	position(root)
	place(root, 0, 0, m)
}

// initNode sets initial sizes (fixed values and fallbacks), origins from x/y, text and group bounds.
func initNode(n *resolve.RNode, m TextMeasurer) {
	n.X, _ = n.F("x")
	n.Y, _ = n.F("y")
	_, n.W = spec(n, 0)
	_, n.H = spec(n, 1)
	for _, k := range n.Kids {
		initNode(k, m)
	}
	switch n.Type {
	case "text":
		textBounds(n, m)
	case "group":
		groupBounds(n)
	}
}

// Hugs reports whether a fit_content size on this axis follows the children (a frame with a layout and
// an in-flow, non-fill child). Anything else keeps the value in `fit_content(N)`.
func Hugs(n *resolve.RNode, ax int) bool {
	if _, ok := dir(n); !ok || sizingOf(n, ax) != fit {
		return false
	}
	for _, k := range n.Kids {
		if inFlow(k) && sizingOf(k, ax) != fill {
			return true
		}
	}
	return false
}

// HugsFill reports a hugging node with a fill child on this axis: the fill child gets what its siblings
// leave, but a CSS flex item adds its content width to a shrink-to-fit container, so `fit-content` would
// size the node differently from the canvas.
func HugsFill(n *resolve.RNode, ax int) bool {
	if !Hugs(n, ax) {
		return false
	}
	for _, k := range n.Kids {
		if inFlow(k) && sizingOf(k, ax) == fill {
			return true
		}
	}
	return false
}

// fitPass is post-order: a fit_content node with a layout hugs its in-flow, non-fill children.
func fitPass(n *resolve.RNode, ax int, m TextMeasurer) {
	for _, k := range n.Kids {
		fitPass(k, ax, m)
	}
	if d, ok := dir(n); ok && Hugs(n, ax) {
		var sized []float64
		for _, k := range n.Kids {
			if inFlow(k) && sizingOf(k, ax) != fill {
				sized = append(sized, outer(k, ax))
			}
		}
		content := 0.0
		if d == ax {
			for _, s := range sized {
				content += s
			}
			content += gap(n) * float64(len(sized)-1)
		} else {
			for _, s := range sized {
				content = fmax(content, s)
			}
		}
		commit(n, ax, content+pad(n, ax), m)
	}
	if n.Type == "group" {
		groupBounds(n)
	}
}

// fillPass is pre-order: fill children share what the others leave (main axis) or take the inner size
// (cross), min 1.
func fillPass(n *resolve.RNode, ax int, m TextMeasurer) {
	if d, ok := dir(n); ok {
		share := size(n, ax) - pad(n, ax)
		if d == ax {
			all, fills := 0, 0
			for _, k := range n.Kids {
				if !inFlow(k) {
					continue
				}
				all++
				if sizingOf(k, ax) == fill {
					fills++
				} else {
					share -= outer(k, ax)
				}
			}
			share -= gap(n) * float64(max(all-1, 0))
			if fills == 0 {
				share = 0
			} else {
				share /= float64(fills)
			}
		}
		share = fmax(share, 1)
		for _, k := range n.Kids {
			if inFlow(k) && sizingOf(k, ax) == fill {
				commit(k, ax, share, m)
			}
		}
	}
	for _, k := range n.Kids {
		fillPass(k, ax, m)
	}
	if n.Type == "group" {
		groupBounds(n)
	}
}

// position places in-flow children: justify along the main axis (space_* ignore gap and never go
// negative: overflow packs from the start, like CSS), align on the cross axis; each child's
// rotated/flipped bounding box lands on its slot.
func position(n *resolve.RNode) {
	if e, ok := dir(n); ok {
		c := 1 - e
		p := Padding(n)
		inner := [2]float64{n.W - p[1] - p[3], n.H - p[0] - p[2]}
		cur := [2]float64{p[3], p[0]}
		var flow []*resolve.RNode
		var boxes [][4]float64
		used := 0.0
		for _, k := range n.Kids {
			if inFlow(k) {
				b := LocalAABB(k)
				flow, boxes = append(flow, k), append(boxes, b)
				used += b[e+2] - b[e]
			}
		}
		free := inner[e] - used
		o := len(flow)
		step := gap(n)
		if o > 0 {
			gaps := step * float64(o-1)
			switch j, _ := n.S("justifyContent"); j {
			case "end":
				cur[e] += free - gaps
			case "center":
				cur[e] += (free - gaps) / 2
			case "space_between":
				// One child divides by zero; the step is never used then.
				step = fmax(free, 0) / float64(o-1)
			case "space_around":
				step = fmax(free, 0) / float64(o)
				cur[e] += step / 2
			}
		}
		align, _ := n.S("alignItems")
		for i, k := range flow {
			b := boxes[i]
			s := [2]float64{b[2] - b[0], b[3] - b[1]}
			q := cur
			switch align {
			case "end":
				q[c] += inner[c] - s[c]
			case "center":
				q[c] += (inner[c] - s[c]) / 2
			}
			k.X = q[0] - b[0]
			k.Y = q[1] - b[1]
			cur[e] += s[e] + step
		}
	}
	for _, k := range n.Kids {
		position(k)
	}
	if n.Type == "group" {
		groupBounds(n)
	}
}

// commit applies a size assigned by a fit or fill pass.
func commit(n *resolve.RNode, ax int, v float64, m TextMeasurer) {
	switch n.Type {
	case "group":
		// ponytail: groups keep their union here; scaling a filled group's children to the new size would fix it.
	case "text":
		g, _ := n.S("textGrowth")
		switch {
		case (g == "fixed-width" || g == "fixed-width-height") && ax == 0 && n.W != v:
			n.W = v
			textBounds(n, m)
		case g == "fixed-width-height":
			n.H = v
		}
		// auto text keeps its intrinsic box and never wraps; fixed-width height follows the lines.
	default:
		if ax == 0 {
			n.W = v
		} else {
			n.H = v
		}
	}
}

// textBounds sizes a text box: auto = intrinsic (ceil), fixed-width = width × ceil(lines),
// fixed-width-height = as set. Lines wrap at ceil(width); a fixed width of 0 breaks at every word
// boundary but never inside a word.
func textBounds(n *resolve.RNode, m TextMeasurer) {
	st := StyleOf(n)
	lineH := m.Metrics(st).LineH
	n.LineH = lineH
	content := ""
	switch c, _ := n.P.Get("content"); x := c.(type) {
	case string:
		content = x
	case jsonv.Number:
		content = string(x)
	}
	switch g, _ := n.S("textGrowth"); g {
	case "fixed-width", "fixed-width-height":
		n.Lines = m.Wrap(content, math.Ceil(n.W), st)
		if g == "fixed-width" {
			n.H = math.Ceil(float64(len(n.Lines)) * lineH)
		}
	default:
		n.Lines = m.Wrap(content, math.Inf(1), st)
		w := 0.0
		for _, l := range strings.Split(content, "\n") {
			w = fmax(w, m.Width(strings.TrimSuffix(l, "\r"), st))
		}
		n.W = math.Ceil(w)
		n.H = math.Ceil(float64(len(n.Lines)) * lineH)
	}
}

// groupBounds is the union of the children's transformed boxes; it may start at a non-zero (even
// negative) origin.
func groupBounds(n *resolve.RNode) {
	b := [4]float64{math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64}
	for _, k := range n.Kids {
		l := LocalAABB(k)
		b = [4]float64{fmin(b[0], k.X+l[0]), fmin(b[1], k.Y+l[1]), fmax(b[2], k.X+l[2]), fmax(b[3], k.Y+l[3])}
	}
	if len(n.Kids) == 0 {
		b = [4]float64{}
	}
	n.BX, n.BY, n.W, n.H = b[0], b[1], b[2]-b[0], b[3]-b[1]
}

// NoteStyle is the text style of a note's body (14 px unless set).
func NoteStyle(n *resolve.RNode) TextStyle {
	st := StyleOf(n)
	if !n.P.Has("fontSize") {
		st.Size = 14
	}
	return st
}

// place turns origins (relative to the parent's origin) into absolute boxes and flags children sticking
// out of frames.
func place(n *resolve.RNode, ox, oy float64, m TextMeasurer) {
	ax, ay := ox+n.X, oy+n.Y
	n.X = ax + n.BX
	n.Y = ay + n.BY
	if n.Type == "note" || n.Type == "prompt" || n.Type == "context" {
		body, _ := n.S("content")
		n.Lines = m.Wrap(body, fmax(n.W-24, 10), NoteStyle(n))
	}
	fx, fy, fw, fh, isFrame := n.X, n.Y, n.W, n.H, n.Type == "frame"
	for _, k := range n.Kids {
		place(k, ax, ay, m)
		if isFrame && k.W > 0 && k.H > 0 {
			// Visual box (rotation/flip included) relative to the frame.
			l := LocalAABB(k)
			kx, ky, kw, kh := k.X-k.BX+l[0]-fx, k.Y-k.BY+l[1]-fy, l[2]-l[0], l[3]-l[1]
			switch {
			case kx >= fw || ky >= fh || kx+kw <= 0 || ky+kh <= 0:
				k.Problem = "fully clipped"
			case kx < -0.5 || ky < -0.5 || kx+kw > fw+0.5 || ky+kh > fh+0.5:
				k.Problem = "partially clipped"
			}
		}
	}
}
