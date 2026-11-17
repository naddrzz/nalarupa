package script

// Layout and content checks on the nodes an execute call touched (script.rs `validate`, `painted`, `contrast`,
// `near_misses`): invisible text, overflow, x/y ignored by auto layout, fill_container without one, collapsed
// frames, missing icons and fonts, low text contrast, small body text and near-miss alignment. Floating-point
// products are wrapped in explicit conversions so they are never fused into a multiply-add.

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// rgb is a colour, or nil when unknown.
type rgb = *[3]float64

// rmax and rmin are Rust's f64::max and f64::min: a NaN operand loses.
func rmax(a, b float64) float64 {
	if math.IsNaN(a) || b > a {
		return b
	}
	return a
}

func rmin(a, b float64) float64 {
	if math.IsNaN(a) || b < a {
		return b
	}
	return a
}

// fnum is `n.f(k).unwrap_or(or)`.
func fnum(n *resolve.RNode, k string, or float64) float64 {
	if f, ok := n.F(k); ok {
		return f
	}
	return or
}

// painted is the colour behind a node's children: its flat fills painted over under, bottom to top. A gradient,
// image, shader or blended fill makes it unknown (nil), and so do a translucent fill over the bare canvas and a
// translucent node (its whole subtree blends with what lies under it).
func painted(under rgb, n *resolve.RNode) rgb {
	if fnum(n, "opacity", 1) < 1 {
		return nil
	}
	bg := under
	fill, _ := n.P.Get("fill")
	for _, f := range svg.Fills(fill) {
		// A blended paint's colour depends on what is under it.
		if m, _ := jsonv.Get(f, "blendMode").(string); svg.BlendMode(m) != "" {
			return nil
		}
		c, ok := svg.Flat(f)
		if !ok {
			return nil
		}
		switch {
		case c[3] >= 1:
			bg = &[3]float64{c[0], c[1], c[2]}
		case bg != nil:
			u := *bg
			bg = &[3]float64{u[0] + float64((c[0]-u[0])*c[3]), u[1] + float64((c[1]-u[1])*c[3]), u[2] + float64((c[2]-u[2])*c[3])}
		}
	}
	return bg
}

// contrast is the WCAG contrast ratio of two sRGB colours (1 to 21).
func contrast(a, b [3]float64) float64 {
	l := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	lum := func(c [3]float64) float64 {
		return float64(0.2126*l(c[0])) + float64(0.7152*l(c[1])) + float64(0.0722*l(c[2]))
	}
	x, y := lum(a), lum(b)
	return (rmax(x, y) + 0.05) / (rmin(x, y) + 0.05)
}

// flex reports whether a node places its children by auto layout (a frame without `layout: "none"`).
func flex(n *resolve.RNode) bool {
	l, ok := n.S("layout")
	return n.Type == "frame" && (!ok || l != "none")
}

// upright reports whether a node's box is where its edges are: no rotation and no flip.
func upright(n *resolve.RNode) bool {
	return fnum(n, "rotation", 0) == 0 && !n.B("flipX") && !n.B("flipY")
}

// nearMisses reports sibling edges a pixel or two apart outside auto layout: a slip, not a choice. Each pair with a
// node the call touched is compared per axis (start, end, centre): one exact match means aligned; otherwise a start
// or end edge 0.5–3px off is reported, at most once per node and axis. Neighbours only: the boxes are at most
// 240px apart on the other axis. Rotated or flipped children and parents past 200 children are skipped.
func nearMisses(n *resolve.RNode, touched map[string]bool, out *[]string) {
	if len(n.Kids) < 2 || len(n.Kids) > 200 {
		return
	}
	var kids []*resolve.RNode
	for _, k := range n.Kids {
		if upright(k) {
			kids = append(kids, k)
		}
	}
	type mark struct {
		id   string
		axis int
	}
	reported := map[mark]bool{}
	gap := func(p, pl, q, ql float64) float64 { return rmax(rmax(q-(p+pl), p-(q+ql)), 0) }
	type edge struct {
		name string
		p, q float64
	}
	for i, a := range kids {
		for _, b := range kids[i+1:] {
			if !touched[a.ID] && !touched[b.ID] {
				continue
			}
			axes := [2]struct {
				apart float64
				edges [3]edge
			}{
				{gap(a.Y, a.H, b.Y, b.H), [3]edge{{"left", a.X, b.X}, {"right", a.X + a.W, b.X + b.W}, {"centre", a.X + a.W/2, b.X + b.W/2}}},
				{gap(a.X, a.W, b.X, b.W), [3]edge{{"top", a.Y, b.Y}, {"bottom", a.Y + a.H, b.Y + b.H}, {"middle", a.Y + a.H/2, b.Y + b.H/2}}},
			}
			for axis, ax := range axes {
				var offs [3]float64
				exact := false
				for j, e := range ax.edges {
					offs[j] = math.Abs(e.p - e.q)
					exact = exact || offs[j] <= 0.5
				}
				if ax.apart > 240 || exact || reported[mark{a.ID, axis}] || reported[mark{b.ID, axis}] {
					continue
				}
				// Centres only count as aligned: two unlike sizes with close centres are not a slip.
				for j := range 2 {
					if offs[j] <= 3 {
						*out = append(*out, fmt.Sprintf("\"%s\" (%s) and \"%s\" (%s) nearly align: their %s edges are %spx apart; align them exactly",
							a.Name(), a.ID, b.Name(), b.ID, ax.edges[j].name, strconv.FormatFloat(offs[j], 'f', 1, 64)))
						reported[mark{a.ID, axis}], reported[mark{b.ID, axis}] = true, true
						break
					}
				}
			}
		}
	}
}

// validate is the issues of the nodes the call touched, sorted, plus the icons still downloading (their "not found"
// warning waits until the engine knows whether they exist).
func (ex *exec) validate() ([]string, []Icon) {
	touched := map[string]bool{}
	for _, id := range ex.touched {
		touched[id] = true
	}
	if len(touched) == 0 {
		return nil, nil
	}
	d := ex.host.Doc()
	var tops []int
	for id := range touched {
		if t := d.TopIndex(id); t >= 0 && !slices.Contains(tops, t) {
			tops = append(tops, t)
		}
	}
	slices.Sort(tops)
	// An instance's resolved props carry its component's x/y; only its own count: set on the instance, or by a
	// `descendants` override for a nested one (id `outer/inner`).
	ownXY := map[string]bool{}
	setsXY := func(v jsonv.Value) bool {
		o := jsonv.Obj(v)
		return o != nil && (o.Has("x") || o.Has("y"))
	}
	doc.Walk(d.Children(), func(n jsonv.Value) {
		id, ok := jsonv.Get(n, "id").(string)
		if !ok || !jsonv.StrIs(jsonv.Get(n, "type"), "ref") {
			return
		}
		if setsXY(n) {
			ownXY[id] = true
		}
		if desc := jsonv.Obj(jsonv.Get(n, "descendants")); desc != nil {
			desc.Each(func(path string, over jsonv.Value) {
				if setsXY(over) {
					ownXY[id+"/"+path] = true
				}
			})
		}
	})
	fonts, assets := ex.host.Fonts(), ex.host.Assets()
	var out []string
	var icons []Icon
	type item struct {
		n, parent *resolve.RNode
		behind    rgb
	}
	for _, t := range tops {
		root := ex.host.LaidOut(t)
		if root == nil {
			continue
		}
		// Each node travels with the colour painted behind it (nil: the canvas, or unknown).
		stack := []item{{n: root}}
		for len(stack) > 0 {
			it := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n := it.n
			fill := painted(it.behind, n)
			// The sibling scan is quadratic: past 200 children every background is left unknown.
			few := len(n.Kids) <= 200
			for i, k := range n.Kids {
				var under rgb
				if few {
					overlaps := func(o *resolve.RNode) bool {
						return o.X < k.X+k.W && k.X < o.X+o.W && o.Y < k.Y+k.H && k.Y < o.Y+o.H
					}
					// Only an upright box is its own outline (an ellipse or path covers less than its box).
					covers := func(o *resolve.RNode) bool {
						return (o.Type == "rectangle" || o.Type == "frame") && upright(o) &&
							o.X <= k.X && o.Y <= k.Y && k.X+k.W <= o.X+o.W && k.Y+k.H <= o.Y+o.H
					}
					// The topmost earlier sibling under k decides: a childless one that covers k is its background;
					// any other overlap (a photo, an edge running across k) leaves it unknown.
					var top *resolve.RNode
					for j := i - 1; j >= 0; j-- {
						if o := n.Kids[j]; o.Type != "text" && overlaps(o) {
							top = o
							break
						}
					}
					switch {
					case top == nil:
						under = fill
					case len(top.Kids) == 0 && covers(top):
						under = painted(nil, top)
					}
				}
				stack = append(stack, item{k, n, under})
			}
			if !flex(n) {
				nearMisses(n, touched, &out)
			}
			if !touched[n.ID] && !(it.parent != nil && touched[it.parent.ID]) {
				continue
			}
			label := fmt.Sprintf("\"%s\" (%s)", n.Name(), n.ID)
			fillV, _ := n.P.Get("fill")
			fills := svg.Fills(fillV)
			if n.Type == "text" && len(fills) == 0 {
				out = append(out, fmt.Sprintf("text %s has no fill and is invisible", label))
			}
			if n.Problem != "" {
				out = append(out, fmt.Sprintf("%s sticks out of its parent (%s); resize the parent or use fill_container/fit_content", label, n.Problem))
			}
			parentLayout := it.parent != nil && flex(it.parent)
			pos, _ := n.S("layoutPosition")
			absolute := pos == "absolute"
			hasXY := n.P.Has("x") || n.P.Has("y")
			if n.Inst != "" {
				hasXY = ownXY[n.ID]
			}
			if parentLayout && !absolute && hasXY && touched[n.ID] {
				out = append(out, fmt.Sprintf("%s: x/y are ignored inside a flex layout parent", label))
			}
			w, _ := n.S("width")
			h, _ := n.S("height")
			fillsParent := strings.HasPrefix(w, "fill_container") || strings.HasPrefix(h, "fill_container")
			if fillsParent && (!parentLayout || absolute) && !strings.Contains(w, "(") && !strings.Contains(h, "(") {
				out = append(out, fmt.Sprintf("%s uses fill_container but its parent has no flex layout", label))
			}
			if n.Type == "frame" && n.W < 0.5 && n.H < 0.5 && len(n.Kids) > 0 {
				out = append(out, fmt.Sprintf("%s collapsed to 0×0 (fit_content parent with only fill_container children?)", label))
			}
			if n.Type == "icon" {
				library, ok := n.S("library")
				if !ok {
					library = "lucide"
				}
				name, _ := n.S("icon")
				weight := fnum(n, "weight", 200)
				if _, ok := assets.IconWeight(library, name, weight); !ok {
					warning := fmt.Sprintf("icon %s: '%s' not found in library '%s'", label, name, library)
					if assets.IconLoading(library, name, weight) {
						icons = append(icons, Icon{Warning: warning, Library: library, Name: name, Weight: satU32(weight)})
					} else {
						out = append(out, warning)
					}
				}
			}
			if n.Type == "text" {
				st := layout.StyleOf(n)
				ink := false
				for _, f := range fills {
					if c, ok := svg.Flat(f); !ok || c[3] > 0 {
						ink = true
						break
					}
				}
				content, _ := n.S("content")
				shown := ink && strings.TrimSpace(content) != ""
				if fg := painted(it.behind, n); shown && it.behind != nil && fg != nil {
					bg := *it.behind
					// WCAG AA: large text is 24px, or 18.66px (14pt) bold.
					need := 4.5
					if st.Size >= 24 || (st.Size >= 18.66 && st.Weight >= 700) {
						need = 3
					}
					if ratio := contrast(*fg, bg); ratio < need {
						// Cut, not rounded: 4.48 must not read as 4.5.
						cut := math.Floor(ratio*100) / 100
						out = append(out, fmt.Sprintf("text %s: contrast %s:1 against its background %s is below WCAG AA (%s:1); change the text or background colour",
							label, strconv.FormatFloat(cut, 'f', 2, 64), svg.Hex([4]float64{bg[0], bg[1], bg[2], 1}), strconv.FormatFloat(need, 'f', -1, 64)))
					}
				}
				// Reading text, not a label: short strings (badges, captions, metadata) may be smaller.
				if st.Size < 12 && utf8.RuneCountInString(content) >= 40 {
					out = append(out, fmt.Sprintf("text %s: body text at %spx is below 12px; set reading text at 12px or more", label, strconv.FormatFloat(st.Size, 'f', -1, 64)))
				}
				fonts.Ensure(st)
				if !fonts.HasFamily(st.Family) && !fonts.Loading(st.Family) {
					out = append(out, fmt.Sprintf("text %s: font family '%s' is not available", label, st.Family))
				}
			}
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	slices.SortFunc(icons, func(a, b Icon) int {
		if c := strings.Compare(a.Warning, b.Warning); c != 0 {
			return c
		}
		if c := strings.Compare(a.Library, b.Library); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return int(a.Weight) - int(b.Weight)
	})
	icons = slices.Compact(icons)
	return out, icons
}

// satU32 is Rust's `weight.max(0.0) as u32`: NaN and negatives are 0, and it saturates.
func satU32(v float64) uint32 {
	switch {
	case !(v > 0):
		return 0
	case v >= math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(v)
}
