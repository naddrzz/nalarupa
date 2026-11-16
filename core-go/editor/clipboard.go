package editor

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/figma"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
)

// The node clipboard's markers: ours, and the .pen one (the compatibility literal) accepted on paste.
const (
	mark    = "data-nala-node-clipboard"
	penMark = "data-pen-node-clipboard"
)

// session marks the clipboard this engine writes, so a paste can tell its own copies (and their sources).
func session(e *engine.Engine) string {
	if e.ClipSession == "" {
		e.ClipSession = fmt.Sprintf("%016x", e.IDs().Next())
	}
	return e.ClipSession
}

// Copy is Ctrl+C: `text/html` with the nodes as base64 JSON in a span (.pen clipboard scheme, own marker;
// positions are world coordinates) and a `text/plain` label.
func Copy(e *engine.Engine, ids []string) *jsonv.Object {
	e.Sync()
	v := newView(e)
	var list []string
	for _, id := range v.paintOrder(e.Doc.Outermost(ids)) {
		if !strings.Contains(id, "/") && e.Doc.Find(id) != nil {
			list = append(list, id)
		}
	}
	nodes, boxes := jsonv.NewArray(), jsonv.NewArray()
	for _, id := range list {
		n := jsonv.Clone(e.Doc.Find(id))
		if x, y, ok := v.origin(id); ok {
			if o := jsonv.Obj(n); o != nil {
				o.Set("x", Num(x))
				o.Set("y", Num(y))
			}
		}
		nodes.Items = append(nodes.Items, n)
		var box jsonv.Value
		if r, ok := v.rect(id); ok {
			box = jsonv.NewArray(jsonv.Float(r.X), jsonv.Float(r.Y), jsonv.Float(r.W), jsonv.Float(r.H))
		}
		boxes.Items = append(boxes.Items, box)
	}
	variables, themes, components := needs(e.Doc, nodes.Items)
	payload := engine.Object("source", session(e), "localData", engine.Strings(list), "worldPositions", true, "boxes", boxes, "remoteData", engine.Object("nodes", nodes),
		"variables", variables, "themes", themes, "components", components)
	label := "Node IDs: " + strings.Join(list, ", ")
	if len(list) == 1 {
		label = "Node ID: " + list[0]
	}
	html := fmt.Sprintf(`<meta charset="utf-8"><span %s="%s">%s</span>`, mark, b64Encode(jsonv.Marshal(payload)), label)
	return engine.Object("html", html, "text", label, "ids", engine.Strings(list))
}

// scan notes every `$name` string and instance `ref` target under v (overrides included).
func scan(v jsonv.Value, vars, refs map[string]bool) {
	switch x := v.(type) {
	case string:
		if name, ok := strings.CutPrefix(x, "$"); ok {
			vars[name] = true
		}
	case *jsonv.Array:
		for _, it := range x.Items {
			scan(it, vars, refs)
		}
	case *jsonv.Object:
		if t, ok := jsonv.Str(jsonv.Get(x, "type")); ok && t == "ref" {
			if r, ok := jsonv.Str(jsonv.Get(x, "ref")); ok {
				refs[r] = true
			}
		}
		x.Each(func(_ string, it jsonv.Value) { scan(it, vars, refs) })
	}
}

// fresh is the keys of set not yet in seen, sorted, now marked seen. Rust walks a HashSet here, in an order
// that changes from run to run; sorted is one of its orders, and the only one when a round finds one name.
func fresh(set, seen map[string]bool) []string {
	var out []string
	for k := range set {
		if !seen[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	for _, k := range out {
		seen[k] = true
	}
	return out
}

// needs is what copied nodes lean on besides themselves, so a paste into another document looks the same:
// the variables they use (chained ones too) with the theme axes those name, and the components their
// instances point at (instances of instances too).
func needs(d *doc.Doc, nodes []jsonv.Value) (variables, themes *jsonv.Object, components *jsonv.Array) {
	vars, refs := map[string]bool{}, map[string]bool{}
	for _, n := range nodes {
		scan(n, vars, refs)
	}
	seenVars, seenRefs := map[string]bool{}, map[string]bool{}
	variables, components = jsonv.NewObject(), jsonv.NewArray()
	components.Items = []jsonv.Value{}
	defs := jsonv.Obj(jsonv.Get(d.Root, "variables"))
	for {
		grew := false
		for _, r := range fresh(refs, seenRefs) {
			grew = true
			if c := d.Find(r); c != nil {
				scan(c, vars, refs)
				components.Items = append(components.Items, jsonv.Clone(c))
			}
		}
		for _, name := range fresh(vars, seenVars) {
			grew = true
			if defs == nil {
				continue
			}
			if def, ok := defs.Get(name); ok {
				scan(def, vars, refs)
				variables.Set(name, jsonv.Clone(def))
			}
		}
		if !grew {
			break
		}
	}
	themes = jsonv.NewObject()
	lists := jsonv.Obj(jsonv.Get(d.Root, "themes"))
	variables.Each(func(_ string, def jsonv.Value) {
		for _, entry := range jsonv.Items(jsonv.Get(def, "value")) {
			if t := jsonv.Obj(jsonv.Get(entry, "theme")); t != nil {
				for _, axis := range t.Keys() {
					if lists == nil {
						continue
					}
					if list, ok := lists.Get(axis); ok {
						themes.Set(axis, jsonv.Clone(list))
					}
				}
			}
		}
	})
	return variables, themes, components
}

// clip is decoded clipboard content.
type clip struct {
	nodes []jsonv.Value
	// origins and boxes are each node's world origin and box.
	origins [][2]float64
	boxes   []Rect
	// sources are the source ids when copied in this session (enables paste in place).
	sources []string
	// center: plain text and SVG always land centered (no meaningful position).
	center, text bool
	// needs holds the variables, theme axes and components of the source document the nodes use.
	needs jsonv.Value
}

// svgSize is an SVG's width and height attributes, else its viewBox size, else 100.
func svgSize(svg string) (float64, float64) {
	tag := svg[max(strings.Index(svg, "<svg"), 0):]
	if i := strings.IndexByte(tag, '>'); i >= 0 {
		tag = tag[:i]
	}
	attr := func(name string) (string, bool) {
		i := strings.Index(tag, " "+name+`="`)
		if i < 0 {
			return "", false
		}
		v, _, _ := strings.Cut(tag[i+len(name)+3:], `"`)
		return v, true
	}
	num := func(s string, ok bool) (float64, bool) {
		if !ok {
			return 0, false
		}
		for strings.HasSuffix(s, "px") {
			s = strings.TrimSuffix(s, "px")
		}
		f, ok := resolve.ParseFloat(strings.TrimSpace(s))
		return f, ok && f > 0
	}
	var vb []float64
	if s, ok := attr("viewBox"); ok {
		for _, p := range splitAny(s, " ,") {
			if f, ok := resolve.ParseFloat(p); ok {
				vb = append(vb, f)
			}
		}
	}
	w, ok := num(attr("width"))
	if !ok {
		w = 100
		if len(vb) == 4 {
			w = vb[2]
		}
	}
	h, ok := num(attr("height"))
	if !ok {
		h = 100
		if len(vb) == 4 {
			h = vb[3]
		}
	}
	return w, h
}

// splitAny is str::split with a set of single-byte separators: every piece, empty ones included.
func splitAny(s, seps string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(seps, s[i]) >= 0 {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func f64Or0(v jsonv.Value) float64 {
	f, _ := jsonv.F64(v)
	return f
}

func decode(e *engine.Engine, html, text string) (*clip, error) {
	for _, m := range []string{mark, penMark} {
		i := strings.Index(html, m+`="`)
		if i < 0 {
			continue
		}
		rest := html[i+len(m)+2:]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			return nil, errors.New("bad clipboard data")
		}
		raw, err := b64Decode(strings.TrimSpace(rest[:j]))
		if err != nil {
			return nil, errors.New("bad clipboard data: " + err.Error())
		}
		p, err := jsonv.Parse(raw)
		if err != nil {
			return nil, errors.New("bad clipboard data: " + err.Error())
		}
		c := &clip{}
		for _, n := range jsonv.Items(jsonv.Get(jsonv.Get(p, "remoteData"), "nodes")) {
			if jsonv.Obj(n) != nil {
				c.nodes = append(c.nodes, n)
			}
		}
		boxes := jsonv.Items(jsonv.Get(p, "boxes"))
		for i, n := range c.nodes {
			o := [2]float64{f64Or0(jsonv.Get(n, "x")), f64Or0(jsonv.Get(n, "y"))}
			c.origins = append(c.origins, o)
			box := Rect{X: o[0], Y: o[1], W: f64Or0(jsonv.Get(n, "width")), H: f64Or0(jsonv.Get(n, "height"))}
			if i < len(boxes) {
				if b := jsonv.Arr(boxes[i]); b != nil && len(b.Items) == 4 {
					box = Rect{X: f64Or0(b.Items[0]), Y: f64Or0(b.Items[1]), W: f64Or0(b.Items[2]), H: f64Or0(b.Items[3])}
				}
			}
			c.boxes = append(c.boxes, box)
		}
		own := session(e)
		if src, ok := jsonv.Str(jsonv.Get(p, "source")); ok && src == own {
			for _, s := range jsonv.Items(jsonv.Get(p, "localData")) {
				if s, ok := s.(string); ok {
					c.sources = append(c.sources, s)
				}
			}
		}
		c.needs = engine.Object("variables", jsonv.Get(p, "variables"), "themes", jsonv.Get(p, "themes"), "components", jsonv.Get(p, "components"))
		return c, nil
	}
	fig, ok, err := figma.Decode(html)
	if err != nil {
		return nil, err
	}
	if ok {
		c := &clip{}
		for _, n := range fig {
			c.nodes = append(c.nodes, n.Value)
			c.origins = append(c.origins, [2]float64{n.X, n.Y})
			c.boxes = append(c.boxes, Rect{X: n.X, Y: n.Y, W: n.W, H: n.H})
		}
		return c, nil
	}
	t := strings.TrimSpace(text)
	svg := strings.HasPrefix(t, "<svg") || (strings.HasPrefix(t, "<?xml") && strings.Contains(t, "<svg"))
	var node *jsonv.Object
	var w, h float64
	switch {
	case svg:
		w, h = svgSize(t)
		url := "data:image/svg+xml;base64," + b64Encode([]byte(t))
		node = engine.Object("type", "rectangle", "name", "SVG", "width", Num(w), "height", Num(h), "fill", engine.Object("type", "image", "url", url, "mode", "fit"))
	case t != "":
		ls := script.Lines(text)
		widest := 0
		for _, l := range ls {
			widest = max(widest, utf8.RuneCountInString(l))
		}
		w, h = float64(widest)*7, float64(max(len(ls), 1))*15
		node = engine.Object("type", "text", "name", "Text", "content", text, "fontFamily", "Inter", "fontSize", jsonv.Int(12), "fill", "#000000")
	default:
		return &clip{center: true}, nil
	}
	return &clip{nodes: []jsonv.Value{node}, origins: [][2]float64{{0, 0}}, boxes: []Rect{{W: w, H: h}}, center: true, text: !svg}, nil
}

// Paste is Ctrl+V. Nodes go into each selected container (centered, or in place when copied from it), else
// next to their visible sources, else onto the canvas where they were (centered in the view when that is
// off-screen). Plain text becomes a text layer, SVG an image. view is nil when unknown. It returns the
// pasted layers' ids.
func Paste(e *engine.Engine, html, text string, targets []string, view *Rect) ([]string, error) {
	c, err := decode(e, html, text)
	if err != nil {
		return nil, err
	}
	if len(c.nodes) == 0 {
		return []string{}, nil
	}
	e.Sync()
	v := newView(e)
	u, _ := union(c.boxes)
	centerOn := func(r Rect) [2]float64 {
		return [2]float64{math.Round(r.X + r.W/2 - u.X - u.W/2), math.Round(r.Y + r.H/2 - u.Y - u.H/2)}
	}
	var parents []string
	for _, id := range targets {
		if slices.Contains(c.sources, id) {
			continue
		}
		cur, ok := id, true
		for ok && cur != doc.Root {
			if v.acceptsChildren(cur) {
				if !slices.Contains(parents, cur) {
					parents = append(parents, cur)
				}
				break
			}
			cur, _, ok = e.Doc.ParentOf(cur)
		}
	}
	srcParent, hasSrcParent := "", false
	for _, s := range c.sources {
		if p, _, ok := e.Doc.ParentOf(s); ok {
			srcParent, hasSrcParent = p, true
			break
		}
	}
	visible := func(r Rect) bool { return view == nil || overlaps(r, *view) }
	srcVisible := false
	for _, s := range c.sources {
		if r, ok := v.rect(s); ok && visible(r) {
			srcVisible = true
			break
		}
	}
	type place struct {
		parent string
		shift  [2]float64
	}
	var places []place
	switch {
	case len(parents) > 0:
		for _, p := range parents {
			var shift [2]float64
			if c.center || !hasSrcParent || srcParent != p {
				if r, ok := v.rect(p); ok {
					shift = centerOn(r)
				}
			}
			places = append(places, place{p, shift})
		}
	case hasSrcParent && srcVisible && !c.center:
		places = []place{{srcParent, [2]float64{}}}
	case !c.center && visible(u):
		places = []place{{doc.Root, [2]float64{}}}
	default:
		var shift [2]float64
		if view != nil {
			shift = centerOn(*view)
		}
		places = []place{{doc.Root, shift}}
	}
	// Components the nodes use that this document lacks go in a row right of everything, pasted nodes
	// included, ids intact. One whose ids clash with this document's is left out.
	var row []Rect
	for _, n := range e.Doc.Children() {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
			if r, ok := v.rect(id); ok {
				row = append(row, r)
			}
		}
	}
	for _, p := range places {
		if p.parent == doc.Root {
			row = append(row, Rect{X: u.X + p.shift[0], Y: u.Y + p.shift[1], W: u.W, H: u.H})
		}
	}
	var cx, cy float64
	if r, ok := union(row); ok {
		cx, cy = r.X+r.W+100, r.Y
	}
	var comps []*jsonv.Object
	for _, comp := range jsonv.Items(jsonv.Get(c.needs, "components")) {
		_, ok := jsonv.Get(comp, "id").(string)
		doc.Walk([]jsonv.Value{comp}, func(n jsonv.Value) {
			id, isStr := jsonv.Str(jsonv.Get(n, "id"))
			ok = ok && isStr && e.Doc.Find(id) == nil
		})
		if !ok {
			continue
		}
		cc := jsonv.Obj(jsonv.Clone(comp))
		cc.Set("x", Num(cx))
		cc.Set("y", Num(cy))
		cx += fmax(f64Or0(jsonv.Get(cc, "width")), 100) + 100
		comps = append(comps, cc)
	}
	type batch struct {
		parent string
		nodes  []jsonv.Value
	}
	var plan []batch
	for _, p := range places {
		pox, poy := v.originOr0(p.parent)
		flow := v.hasLayout(p.parent)
		var nodes []jsonv.Value
		for i, n := range c.nodes {
			n := jsonv.Clone(n)
			o := jsonv.Obj(n)
			o.Remove("reusable")
			if !flow || jsonv.StrIs(jsonv.Get(n, "layoutPosition"), "absolute") {
				o.Set("x", Num(c.origins[i][0]+p.shift[0]-pox))
				o.Set("y", Num(c.origins[i][1]+p.shift[1]-poy))
			}
			if c.text && p.parent == doc.Root {
				o.Set("fill", "#FFFFFF")
			}
			nodes = append(nodes, n)
		}
		plan = append(plan, batch{p.parent, nodes})
	}
	var out []string
	err = e.EditOrDrag(func(d *doc.Doc) error {
		out = []string{}
		// Variables the nodes use that this document lacks (its own win on a name clash), with their theme axes.
		own := jsonv.Obj(jsonv.Get(d.Root, "variables"))
		vars := jsonv.NewObject()
		if src := jsonv.Obj(jsonv.Get(c.needs, "variables")); src != nil {
			src.Each(func(k string, val jsonv.Value) {
				if own == nil || !own.Has(k) {
					vars.Set(k, jsonv.Clone(val))
				}
			})
		}
		if vars.Len() > 0 {
			if lists := jsonv.Obj(jsonv.Get(c.needs, "themes")); lists != nil {
				lists.Each(func(axis string, list jsonv.Value) {
					// Only into a themes object (or where there is none yet), never over an axis it has.
					cur, _ := d.Root.Get("themes")
					themes := jsonv.Obj(cur)
					if jsonv.Arr(list) == nil || (cur != nil && themes == nil) || (themes != nil && themes.Has(axis)) {
						return
					}
					if themes == nil {
						themes = jsonv.NewObject()
						d.Root.Set("themes", themes)
					}
					themes.Set(axis, jsonv.Clone(list))
				})
			}
			if err := d.SetVariables(vars, false); err != nil {
				return err
			}
		}
		for _, comp := range comps {
			if _, err := d.ImportComponent(comp); err != nil {
				return err
			}
		}
		for _, b := range plan {
			for _, n := range b.nodes {
				id, err := d.Insert(b.parent, n, -1)
				if err != nil {
					return err
				}
				out = append(out, id)
			}
		}
		return nil
	})
	return out, err
}
