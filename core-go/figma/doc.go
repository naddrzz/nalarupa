package figma

// The Figma node tree, its styles, variables and instance overrides.

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// ── serde_json-style access ──────────────────────────────────────────────────

func get(v jsonv.Value, keys ...string) jsonv.Value {
	for _, k := range keys {
		v = jsonv.Get(v, k)
	}
	return v
}

// at is serde's `v[i]`: nil when v is no array or i is out of range.
func at(v jsonv.Value, i int) jsonv.Value {
	items := jsonv.Items(v)
	if i < 0 || i >= len(items) {
		return nil
	}
	return items[i]
}

// num is the Rust `f`: a finite number.
func num(v jsonv.Value) (float64, bool) {
	x, ok := jsonv.F64(v)
	if !ok || math.IsNaN(x) || math.IsInf(x, 0) {
		return 0, false
	}
	return x, true
}

func numOr(v jsonv.Value, d float64) float64 {
	if x, ok := num(v); ok {
		return x
	}
	return d
}

func u64(v jsonv.Value) (uint64, bool) {
	n, ok := v.(jsonv.Number)
	if !ok {
		return 0, false
	}
	return n.Uint64()
}

func isObj(v jsonv.Value) bool {
	o, ok := v.(*jsonv.Object)
	return ok && o != nil
}

func isArr(v jsonv.Value) bool {
	a, ok := v.(*jsonv.Array)
	return ok && a != nil
}

// arr is the items of an array (a fresh slice), or none.
func arr(v jsonv.Value) []jsonv.Value {
	return slices.Clone(jsonv.Items(v))
}

func truthy(v jsonv.Value) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case jsonv.Number:
		f, ok := x.Float64()
		return !ok || f != 0
	}
	return true
}

// with is chain + [v] in a new slice, so sibling chains never share a backing array.
func with(chain []jsonv.Value, v jsonv.Value) []jsonv.Value {
	out := make([]jsonv.Value, len(chain)+1)
	copy(out, chain)
	out[len(chain)] = v
	return out
}

func obj(kv ...any) *jsonv.Object {
	o := jsonv.NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// ── guids and references ─────────────────────────────────────────────────────

func key(g jsonv.Value) (string, bool) {
	s, ok := u64(get(g, "sessionID"))
	if !ok {
		return "", false
	}
	l, ok := u64(get(g, "localID"))
	if !ok {
		return "", false
	}
	return strconv.FormatUint(s, 10) + ":" + strconv.FormatUint(l, 10), true
}

var none = jsonv.Int(math.MaxUint32)

// real reports a guid that is set (Figma marks "none" with 0xFFFFFFFF).
func real(g jsonv.Value) bool {
	return isObj(g) && !jsonv.Equal(get(g, "sessionID"), none) && !jsonv.Equal(get(g, "localID"), none)
}

func same(a, b jsonv.Value) bool {
	return isObj(a) && isObj(b) && jsonv.Equal(get(a, "sessionID"), get(b, "sessionID")) && jsonv.Equal(get(a, "localID"), get(b, "localID"))
}

// refKey is the library key, else the local guid, of a style/variable/set reference.
func refKey(t jsonv.Value) (string, bool) {
	if k, ok := jsonv.Str(get(t, "assetRef", "key")); ok && k != "" {
		return k, true
	}
	if real(get(t, "guid")) {
		return key(get(t, "guid"))
	}
	return "", false
}

// scope is what variable resolution reads from a node: its modes and component property assignments.
func scope(p jsonv.Value) *jsonv.Object {
	return obj("variableModeBySetMap", jsonv.Clone(get(p, "variableModeBySetMap")), "componentPropAssignments", jsonv.Clone(get(p, "componentPropAssignments")))
}

// ── the document ─────────────────────────────────────────────────────────────

type node struct {
	p    *jsonv.Object
	kids []*node
}

type variable struct {
	set     string
	hasSet  bool
	entries []jsonv.Value
}

type doc struct {
	props map[string]*jsonv.Object
	// kids are child guids, bottom (lowest position) first.
	kids  map[string][]string
	blobs [][]byte
	vars  map[string]variable
	modes map[string]string
	// styles hold props (read only).
	styles map[string]*jsonv.Object
	// budget is the nodes left to convert: nested instances could otherwise multiply without bound.
	budget int
}

func newDoc(msg *jsonv.Object) *doc {
	var changes []*jsonv.Object
	for _, c := range jsonv.Items(get(msg, "nodeChanges")) {
		if o, ok := c.(*jsonv.Object); ok {
			changes = append(changes, o)
		}
	}
	d := &doc{props: map[string]*jsonv.Object{}, kids: map[string][]string{}, vars: map[string]variable{}, modes: map[string]string{},
		styles: map[string]*jsonv.Object{}, budget: maxNodes}
	var order []string
	for _, c := range changes {
		if k, ok := key(get(c, "guid")); ok {
			if _, seen := d.props[k]; !seen {
				order = append(order, k)
			}
			d.props[k] = c
		}
	}
	type kid struct{ pos, k string }
	kids := map[string][]kid{}
	for _, c := range changes {
		k, ok := key(get(c, "guid"))
		parent, ok2 := key(get(c, "parentIndex", "guid"))
		if !ok || !ok2 {
			continue
		}
		if _, has := d.props[parent]; has && parent != k {
			kids[parent] = append(kids[parent], kid{jsonv.StrOr(get(c, "parentIndex", "position")), k})
		}
	}
	for p, v := range kids {
		slices.SortStableFunc(v, func(a, b kid) int { return strings.Compare(a.pos, b.pos) })
		v = slices.CompactFunc(v, func(a, b kid) bool { return a.k == b.k })
		ks := make([]string, len(v))
		for i, x := range v {
			ks[i] = x.k
		}
		d.kids[p] = ks
	}
	for _, b := range jsonv.Items(get(msg, "blobs")) {
		s, _ := jsonv.Str(get(b, "bytes"))
		d.blobs = append(d.blobs, hexBytes(s))
	}
	for _, c := range changes {
		if !jsonv.StrIs(get(c, "type"), "VARIABLE_SET") {
			continue
		}
		mode, ok := key(get(at(get(c, "variableSetModes"), 0), "id"))
		if !ok {
			continue
		}
		var ks []string
		if k, ok := refKey(get(c, "variableSetID")); ok {
			ks = append(ks, k)
		} else if k, ok := key(get(c, "guid")); ok {
			ks = append(ks, k)
		}
		if k, ok := key(get(c, "guid")); ok {
			ks = append(ks, k)
		}
		if k, ok := jsonv.Str(get(c, "variableSetID", "assetRef", "key")); ok {
			ks = append(ks, k)
		}
		if k, ok := jsonv.Str(get(c, "key")); ok {
			ks = append(ks, k)
		}
		for _, k := range ks {
			d.modes[k] = mode
		}
	}
	for _, c := range changes {
		if !jsonv.StrIs(get(c, "type"), "VARIABLE") || !isArr(get(c, "variableDataValues", "entries")) {
			continue
		}
		k, ok := jsonv.Str(get(c, "key"))
		if !ok || k == "" {
			k, ok = key(get(c, "guid"))
		}
		if !ok {
			continue
		}
		if _, has := d.vars[k]; !has {
			set, hasSet := refKey(get(c, "variableSetID"))
			d.vars[k] = variable{set, hasSet, arr(get(c, "variableDataValues", "entries"))}
		}
	}
	// Rust walks a HashMap here; keys rarely collide, and this order is at least stable.
	for _, k := range order {
		p := d.props[k]
		if s, ok := jsonv.Str(get(p, "key")); ok {
			d.styles[s] = p
		}
		if g, ok := key(get(p, "guid")); ok {
			d.styles[g] = p
		}
	}
	return d
}

// hexBytes decodes pairs as Rust's u8::from_str_radix(_, 16) does (a "+" sign is allowed), skipping
// pairs that do not parse or split a character.
func hexBytes(s string) []byte {
	boundary := func(i int) bool { return i == 0 || i >= len(s) || s[i]&0xc0 != 0x80 }
	digit := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10, true
		}
		return 0, false
	}
	out := []byte{}
	for i := 0; i < len(s)/2; i++ {
		a, b := i*2, i*2+2
		if !boundary(a) || !boundary(b) {
			continue
		}
		hi, lo := s[a], s[a+1]
		if hi == '+' {
			if v, ok := digit(lo); ok {
				out = append(out, v)
			}
			continue
		}
		h, ok1 := digit(hi)
		l, ok2 := digit(lo)
		if ok1 && ok2 {
			out = append(out, h<<4|l)
		}
	}
	return out
}

func (d *doc) subtree(k string, depth int) *node {
	if depth > maxDepth {
		return nil
	}
	p, ok := d.props[k]
	if !ok {
		return nil
	}
	n := &node{p: jsonv.Clone(p).(*jsonv.Object)}
	for _, c := range d.kids[k] {
		if s := d.subtree(c, depth+1); s != nil {
			n.kids = append(n.kids, s)
		}
	}
	return n
}

// page is the first visible page's layers (a copy has one).
func (d *doc) page() []Node {
	root, ok := d.props["0:0"]
	if !ok {
		return nil
	}
	page := ""
	for _, k := range d.kids["0:0"] {
		if jsonv.IsTrue(get(d.props[k], "visible")) {
			page = k
			break
		}
	}
	if page == "" {
		return nil
	}
	up := []jsonv.Value{root, d.props[page]}
	var out []Node
	for _, k := range d.kids[page] {
		p := d.props[k]
		x, y := numOr(get(p, "transform", "m02"), 0), numOr(get(p, "transform", "m12"), 0)
		w, h := numOr(get(p, "size", "x"), 0), numOr(get(p, "size", "y"), 0)
		s := d.subtree(k, 1)
		if s == nil {
			continue
		}
		if v := d.convert(s, up, false, 1); v != nil {
			out = append(out, Node{v, x, y, w, h})
		}
	}
	return out
}

// ── styles, variables, overrides ─────────────────────────────────────────────

func (d *doc) style(id jsonv.Value) *jsonv.Object {
	k, ok := refKey(id)
	if !ok {
		return nil
	}
	return d.styles[k]
}

func (d *doc) applyStyles(p *jsonv.Object, chain []jsonv.Value) {
	if s := d.style(get(p, "styleIdForFill")); s != nil && get(s, "fillPaints") != nil {
		p.Set("fillPaints", d.paints(get(s, "fillPaints"), chain))
	}
	if s := d.style(get(p, "styleIdForStrokeFill")); s != nil && get(s, "fillPaints") != nil {
		p.Set("strokePaints", d.paints(get(s, "fillPaints"), chain))
	}
	if s := d.style(get(p, "styleIdForEffect")); s != nil && get(s, "effects") != nil {
		p.Set("effects", jsonv.Clone(get(s, "effects")))
	}
	if s := d.style(get(p, "styleIdForText")); s != nil {
		for _, k := range []string{"fontName", "textCase", "fontSize", "textDecoration", "letterSpacing", "lineHeight", "paragraphSpacing"} {
			if v := get(s, k); v != nil {
				p.Set(k, jsonv.Clone(v))
			}
		}
	}
}

// paints are paint colors bound to variables, resolved for the node at the end of chain.
func (d *doc) paints(paints jsonv.Value, chain []jsonv.Value) *jsonv.Array {
	out := []jsonv.Value{}
	for _, r := range jsonv.Items(paints) {
		r = jsonv.Clone(r)
		ro, ok := r.(*jsonv.Object)
		if !ok {
			out = append(out, r)
			continue
		}
		if stops := jsonv.Items(get(ro, "stops")); isArr(get(ro, "stops")) {
			vars := jsonv.Items(get(ro, "stopsVar"))
			for i, s := range stops {
				so, ok := s.(*jsonv.Object)
				if !ok {
					continue
				}
				v := get(so, "colorVar")
				if v == nil && i < len(vars) {
					v = get(vars[i], "colorVar")
				}
				if c := d.colorVar(v, chain); c != nil {
					so.Set("color", c)
				}
			}
		} else if c := d.colorVar(get(ro, "colorVar"), chain); c != nil {
			ro.Set("color", c)
			ro.Set("opacity", jsonv.Int(1))
		}
		out = append(out, ro)
	}
	return jsonv.NewArray(out...)
}

func (d *doc) colorVar(v jsonv.Value, chain []jsonv.Value) jsonv.Value {
	if !jsonv.StrIs(get(v, "resolvedDataType"), "COLOR") {
		return nil
	}
	x := d.variable(v, chain, 0)
	if c := get(x, "colorValue"); isObj(c) {
		return jsonv.Clone(c)
	}
	return nil
}

// variable is a variable's value for the node at the end of chain (its mode, else the set's first); nil
// when there is none.
func (d *doc) variable(n jsonv.Value, chain []jsonv.Value, depth int) jsonv.Value {
	if !isObj(get(n, "value")) || depth > maxDepth {
		return nil
	}
	if !jsonv.StrIs(get(n, "dataType"), "ALIAS") {
		return jsonv.Clone(get(n, "value"))
	}
	k, ok := refKey(get(n, "value", "alias"))
	if !ok {
		return nil
	}
	v, ok := d.vars[k]
	if !ok {
		return nil
	}
	if len(v.entries) > 1 && v.hasSet {
		if mode, ok := d.mode(v.set, chain); ok {
			for _, e := range v.entries {
				if mk, ok := key(get(e, "modeID")); ok && mk == mode {
					return d.variable(get(e, "variableData"), chain, depth+1)
				}
			}
		}
	}
	if len(v.entries) == 0 {
		return nil
	}
	return d.variable(get(v.entries[0], "variableData"), chain, depth+1)
}

func (d *doc) mode(set string, chain []jsonv.Value) (string, bool) {
	for i := len(chain) - 1; i >= 0; i-- {
		for _, s := range jsonv.Items(get(chain[i], "variableModeBySetMap", "entries")) {
			if k, ok := refKey(get(s, "variableSetID")); ok && k == set && get(s, "variableModeID") != nil {
				return key(get(s, "variableModeID"))
			}
		}
	}
	m, ok := d.modes[set]
	return m, ok
}

// propRef follows component-property references up the tree to the assigned value.
func (d *doc) propRef(chain []jsonv.Value, n jsonv.Value, depth int) jsonv.Value {
	id := get(n, "value", "propRefValue", "defId")
	if id == nil || depth > maxDepth {
		return n
	}
	for i := len(chain) - 1; i >= 0; i-- {
		for _, s := range jsonv.Items(get(chain[i], "componentPropAssignments")) {
			if same(id, get(s, "defID")) {
				return d.propRef(chain[:i+1], get(s, "varValue"), depth+1)
			}
		}
	}
	return n
}

// bind applies node properties bound to variables / component properties.
func (d *doc) bind(p *jsonv.Object, up []jsonv.Value, entries jsonv.Value) {
	if !isArr(entries) {
		return
	}
	for _, r := range jsonv.Items(entries) {
		field, ok := jsonv.Str(get(r, "variableField"))
		if !ok {
			continue
		}
		chain := with(up, scope(p))
		v := d.variable(d.propRef(chain, get(r, "variableData"), 0), chain, 0)
		if v == nil {
			continue
		}
		x, hasNum := num(get(v, "floatValue"))
		set := func(keys ...string) {
			if hasNum {
				for _, k := range keys {
					p.Set(k, jsonv.Float(x))
				}
			}
		}
		switch field {
		case "TEXT_DATA":
			if t := get(v, "textDataValue"); isObj(t) {
				p.Set("textData", jsonv.Clone(t))
			} else if t, ok := get(v, "textValue").(string); ok {
				p.Set("textData", obj("characters", t))
			}
		case "VISIBLE":
			if b, ok := get(v, "boolValue").(bool); ok {
				p.Set("visible", b)
			}
		case "OVERRIDDEN_SYMBOL_ID":
			if g := get(v, "symbolIdValue", "guid"); real(g) {
				p.Set("overriddenSymbolID", jsonv.Clone(g))
			}
		case "OPACITY":
			set("opacity")
		case "CORNER_RADIUS":
			set("cornerRadius", "rectangleTopLeftCornerRadius", "rectangleTopRightCornerRadius", "rectangleBottomLeftCornerRadius", "rectangleBottomRightCornerRadius")
		case "RECTANGLE_TOP_LEFT_CORNER_RADIUS":
			set("rectangleTopLeftCornerRadius")
		case "RECTANGLE_TOP_RIGHT_CORNER_RADIUS":
			set("rectangleTopRightCornerRadius")
		case "RECTANGLE_BOTTOM_LEFT_CORNER_RADIUS":
			set("rectangleBottomLeftCornerRadius")
		case "RECTANGLE_BOTTOM_RIGHT_CORNER_RADIUS":
			set("rectangleBottomRightCornerRadius")
		case "STROKE_WEIGHT":
			set("strokeWeight")
		case "STACK_SPACING":
			set("stackSpacing")
		case "STACK_PADDING_LEFT":
			set("stackHorizontalPadding")
		case "STACK_PADDING_TOP":
			set("stackVerticalPadding")
		case "STACK_PADDING_RIGHT":
			set("stackPaddingRight")
		case "STACK_PADDING_BOTTOM":
			set("stackPaddingBottom")
		case "BORDER_TOP_WEIGHT":
			set("borderTopWeight")
		case "BORDER_BOTTOM_WEIGHT":
			set("borderBottomWeight")
		case "BORDER_LEFT_WEIGHT":
			set("borderLeftWeight")
		case "BORDER_RIGHT_WEIGHT":
			set("borderRightWeight")
		case "STACK_COUNTER_SPACING":
			set("stackCounterSpacing")
		case "FONT_SIZE":
			set("fontSize")
		case "WIDTH":
			if hasNum {
				p.Set("size", obj("x", jsonv.Float(x), "y", jsonv.Float(numOr(get(p, "size", "y"), 0))))
			}
		case "HEIGHT":
			if hasNum {
				p.Set("size", obj("x", jsonv.Float(numOr(get(p, "size", "x"), 0)), "y", jsonv.Float(x)))
			}
		}
	}
}

// swap turns an instance into an instance of another component, keeping its own placement and
// assignments.
func (d *doc) swap(p *jsonv.Object, symbol jsonv.Value) *jsonv.Object {
	k, ok := key(symbol)
	if !ok {
		return p
	}
	src, ok := d.props[k]
	if !ok {
		return p
	}
	n := jsonv.Clone(src).(*jsonv.Object)
	n.Set("type", "INSTANCE")
	for _, k := range []string{"guid", "parentIndex", "overrideKey"} {
		n.Set(k, jsonv.Clone(get(p, k)))
	}
	for _, k := range []string{"size", "transform", "visible", "parameterConsumptionMap", "componentPropAssignments", "symbolData", "derivedSymbolData", "variableModeBySetMap"} {
		if v := get(p, k); v != nil {
			n.Set(k, jsonv.Clone(v))
		}
	}
	return n
}

// overrides applies instance overrides addressed by guid path: this node's own ones
// apply here, deeper ones travel down (or into a nested instance's own override list).
func (d *doc) overrides(e *node, up []jsonv.Value, ovr, derived []jsonv.Value, depth int) {
	if depth > maxDepth {
		return
	}
	id := get(e.p, "overrideKey")
	if !isObj(id) {
		id = get(e.p, "guid")
	}
	split := func(list []jsonv.Value) (here, deeper []jsonv.Value) {
		for _, o := range list {
			if !isObj(o) {
				continue
			}
			g := jsonv.Items(get(o, "guidPath", "guids"))
			if len(g) == 0 || !same(g[0], id) {
				continue
			}
			c := jsonv.Clone(o).(*jsonv.Object)
			if len(g) > 1 {
				rest := make([]jsonv.Value, len(g)-1)
				for i, x := range g[1:] {
					rest[i] = jsonv.Clone(x)
				}
				c.Set("guidPath", obj("guids", jsonv.NewArray(rest...)))
				deeper = append(deeper, c)
			} else {
				here = append(here, c)
			}
		}
		return here, deeper
	}
	here, deeper := split(ovr)
	dhere, ddeeper := split(derived)
	var name jsonv.Value
	renamed := false
	if jsonv.StrIs(get(e.p, "type"), "INSTANCE") {
		for _, o := range here {
			if real(get(o, "overriddenSymbolID")) {
				name, renamed = jsonv.Clone(get(e.p, "name")), true
				e.p = d.swap(e.p, get(o, "overriddenSymbolID"))
				break
			}
		}
	}
	for _, dv := range here {
		do := dv.(*jsonv.Object)
		d.applyStyles(do, with(up, scope(e.p)))
		prev := jsonv.Clone(e.p).(*jsonv.Object)
		do.Each(func(k string, v jsonv.Value) { e.p.Set(k, jsonv.Clone(v)) })
		e.p.Remove("guidPath")
		if get(prev, "variableModeBySetMap") != nil && get(e.p, "variableModeBySetMap") == nil {
			e.p.Set("variableModeBySetMap", jsonv.Clone(get(prev, "variableModeBySetMap")))
		}
		if nw, old := get(do, "componentPropAssignments"), get(prev, "componentPropAssignments"); isArr(nw) && isArr(old) {
			var kept []jsonv.Value
			for _, o := range jsonv.Items(old) {
				if !slices.ContainsFunc(jsonv.Items(nw), func(n jsonv.Value) bool { return same(get(n, "defID"), get(o, "defID")) }) {
					kept = append(kept, jsonv.Clone(o))
				}
			}
			if a := jsonv.Arr(get(e.p, "componentPropAssignments")); a != nil {
				a.Items = append(a.Items, kept...)
			}
		}
		for _, k := range []string{"fillPaints", "strokePaints"} {
			was := get(prev, k)
			for i, p := range jsonv.Items(get(e.p, k)) {
				po, ok := p.(*jsonv.Object)
				if !ok {
					continue
				}
				if get(po, "colorVar") == nil && get(at(was, i), "colorVar") != nil {
					po.Set("colorVar", jsonv.Clone(get(at(was, i), "colorVar")))
				}
			}
		}
		chain := with(up, scope(e.p))
		for _, ks := range [][2]string{{"fillPaints", "styleIdForFill"}, {"strokePaints", "styleIdForStrokeFill"}} {
			if get(do, ks[0]) != nil {
				if s := d.style(get(prev, ks[1])); s != nil && get(s, "fillPaints") != nil {
					e.p.Set(ks[0], d.paints(get(s, "fillPaints"), chain))
				}
			}
		}
		for _, k := range []string{"fillPaints", "strokePaints"} {
			if v := get(e.p, k); v != nil {
				e.p.Set(k, d.paints(v, chain))
			}
		}
		if g := get(do, "overriddenSymbolID"); real(g) {
			if !isObj(get(e.p, "symbolData")) {
				e.p.Set("symbolData", jsonv.NewObject())
			}
			jsonv.Obj(get(e.p, "symbolData")).Set("symbolID", jsonv.Clone(g))
		}
		entries := get(do, "variableConsumptionMap", "entries")
		if !isArr(entries) {
			entries = get(do, "parameterConsumptionMap", "entries")
		}
		d.bind(e.p, up, entries)
	}
	if renamed {
		e.p.Set("name", name)
	}
	for _, dv := range dhere {
		dv.(*jsonv.Object).Each(func(k string, v jsonv.Value) {
			if k != "guidPath" && v != nil {
				e.p.Set(k, jsonv.Clone(v))
			}
		})
		for _, k := range []string{"styleIdForFill", "styleIdForStrokeFill", "styleIdForEffect", "styleIdForText"} {
			e.p.Remove(k)
		}
	}
	if jsonv.StrIs(get(e.p, "type"), "INSTANCE") {
		list := func(p *jsonv.Object, k string, extra []jsonv.Value) {
			if !isArr(get(p, k)) {
				p.Set(k, jsonv.NewArray())
			}
			a := jsonv.Arr(get(p, k))
			a.Items = append(a.Items, extra...)
		}
		if !isObj(get(e.p, "symbolData")) {
			e.p.Set("symbolData", jsonv.NewObject())
		}
		list(jsonv.Obj(get(e.p, "symbolData")), "symbolOverrides", deeper)
		list(e.p, "derivedSymbolData", ddeeper)
		return
	}
	ovr = append(slices.Clone(ovr), deeper...)
	derived = append(slices.Clone(derived), ddeeper...)
	chain := with(up, e.p)
	for _, k := range e.kids {
		d.overrides(k, chain, ovr, derived, depth+1)
	}
}
