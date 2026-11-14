// Package resolve turns the stored document into a render tree (core/src/resolve.rs): component
// instances are expanded, `$variables` are substituted for the active theme, disabled nodes are dropped.
// It also hosts the on-load migration of older .pen versions.
package resolve

import (
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Line is one laid-out line of text. Width is its advance without trailing spaces (they hang); Soft
// means it was ended by wrapping rather than `\n` (only those lines are justified).
type Line struct {
	Text  string
	Width float64
	Soft  bool
}

// RNode is a node of the render tree.
type RNode struct {
	// ID is the addressable path: `id`, or `instanceId/childId` inside component instances.
	ID   string
	Type string
	P    *jsonv.Object
	Kids []*RNode
	// Filled by layout (absolute document coordinates).
	X, Y, W, H float64
	// BX, BY is the local-bounds origin relative to the node's own origin; non-zero only for groups
	// (union of their children). The transform pivot is (X - BX, Y - BY).
	BX, BY float64
	Lines  []Line
	// LineH is the height of one entry of Lines (text only).
	LineH float64
	// Problem is "" or what is wrong with the node ("missing component", "partially clipped", ...).
	Problem string
	// Inst is the component id when this node is an expanded `ref` instance.
	Inst string
}

// S returns the string property k.
func (n *RNode) S(k string) (string, bool) {
	v, _ := n.P.Get(k)
	return jsonv.Str(v)
}

// F returns the numeric property k (a number or a numeric string).
func (n *RNode) F(k string) (float64, bool) {
	v, _ := n.P.Get(k)
	return Num(v)
}

// B returns the boolean property k (false when unset).
func (n *RNode) B(k string) bool {
	v, _ := n.P.Get(k)
	return jsonv.IsTrue(v)
}

// Name is the node's name, or its type when it has none.
func (n *RNode) Name() string {
	if s, ok := n.S("name"); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return n.Type
}

// Visit calls f on the node and its descendants, parents first, with their depth.
func (n *RNode) Visit(f func(*RNode, int), depth int) {
	f(n, depth)
	for _, k := range n.Kids {
		k.Visit(f, depth+1)
	}
}

func leaf(id, ty string, p *jsonv.Object, problem string) *RNode {
	return &RNode{ID: id, Type: ty, P: p, Problem: problem}
}

// Num is a number, or a string holding one.
func Num(v jsonv.Value) (float64, bool) {
	if f, ok := jsonv.F64(v); ok {
		return f, true
	}
	if s, ok := jsonv.Str(v); ok {
		return ParseFloat(strings.TrimSpace(s))
	}
	return 0, false
}

// ParseFloat parses s as Rust's `str::parse::<f64>` does: no hex, no digit separators, and an
// out-of-range value becomes infinity.
func ParseFloat(s string) (float64, bool) {
	if strings.ContainsAny(s, "xX_") {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if e, ok := err.(*strconv.NumError); !ok || e.Err != strconv.ErrRange {
			return 0, false
		}
	}
	return f, true
}

type themePair struct{ axis, val string }

type scopeInfo struct {
	vars   *jsonv.Object
	themes []themePair
}

// Lib is an imported document: its alias and root.
type Lib struct {
	Alias string
	Root  jsonv.Value
}

// Resolver builds render nodes from a document and its imported libraries.
type Resolver struct {
	// scopes holds variables and default themes per scope: "" is the open document, an import alias is that library.
	scopes map[string]scopeInfo
	index  map[string]jsonv.Value
}

func themeDefaults(root jsonv.Value) []themePair {
	var out []themePair
	if t := jsonv.Obj(jsonv.Get(root, "themes")); t != nil {
		t.Each(func(axis string, vals jsonv.Value) {
			items := jsonv.Items(vals)
			if len(items) == 0 {
				return
			}
			if s, ok := jsonv.Str(items[0]); ok {
				out = append(out, themePair{axis, s})
			}
		})
	}
	return out
}

// applyTheme applies a node's `theme` axes over the active theme.
func applyTheme(theme *[]themePair, node jsonv.Value) {
	t := jsonv.Obj(jsonv.Get(node, "theme"))
	if t == nil {
		return
	}
	t.Each(func(axis string, v jsonv.Value) {
		val := jsonv.StrOr(v)
		for i := range *theme {
			if (*theme)[i].axis == axis {
				(*theme)[i].val = val
				return
			}
		}
		*theme = append(*theme, themePair{axis, val})
	})
}

// settled prefixes a `$name` string already settled in its written scope, so the component's scope
// leaves it alone.
const settledMark = ""

// scoped is the index key of a `ref` written inside scope: a library's plain ids live under `alias:`.
func scoped(scope, r string) string {
	if scope == "" || strings.Contains(r, ":") {
		return r
	}
	return scope + ":" + r
}

func varsOf(root jsonv.Value) *jsonv.Object {
	if v := jsonv.Obj(jsonv.Get(root, "variables")); v != nil {
		return v
	}
	return jsonv.NewObject()
}

// New indexes root and libs; library nodes are addressable as `alias:id`, and resolve their own
// variables and refs inside their own scope.
func New(root jsonv.Value, libs []Lib) *Resolver {
	r := &Resolver{scopes: map[string]scopeInfo{}, index: map[string]jsonv.Value{}}
	r.scopes[""] = scopeInfo{varsOf(root), themeDefaults(root)}
	for _, l := range libs {
		r.scopes[l.Alias] = scopeInfo{varsOf(l.Root), themeDefaults(l.Root)}
	}
	doc.Walk(jsonv.Items(jsonv.Get(root, "children")), func(n jsonv.Value) {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
			r.index[id] = n
		}
	})
	for _, l := range libs {
		doc.Walk(jsonv.Items(jsonv.Get(l.Root, "children")), func(n jsonv.Value) {
			if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
				r.index[l.Alias+":"+id] = n
			}
		})
	}
	return r
}

// Node returns the node indexed as id, or nil.
func (r *Resolver) Node(id string) jsonv.Value {
	return r.index[id]
}

func (r *Resolver) defaults() []themePair {
	return append([]themePair(nil), r.scopes[""].themes...)
}

// HasPath reports whether path names a node: a document id, or an instance path (`inst/child`,
// `inst/nested/child`) into the expanded component, hidden descendants included.
func (r *Resolver) HasPath(path string) bool {
	segs := strings.Split(path, "/")
	n, ok := r.index[segs[0]]
	if !ok {
		return false
	}
	node, scope := jsonv.Clone(n), ""
	theme := r.defaults()
	for _, seg := range segs[1:] {
		if !jsonv.StrIs(jsonv.Get(node, "type"), "ref") {
			return false
		}
		merged, s, ok := r.expand(node, scope, theme, 0)
		if !ok {
			return false
		}
		next := doc.FindIn(jsonv.Items(jsonv.Get(merged, "children")), seg)
		if next == nil {
			return false
		}
		node, scope = jsonv.Clone(next), s
	}
	return true
}

// Resolve builds the render tree of a node; nil when it is disabled.
func (r *Resolver) Resolve(node jsonv.Value) *RNode {
	return r.resolveIn(node, "", r.defaults(), "", 0)
}

func (r *Resolver) resolveIn(node jsonv.Value, prefix string, parentTheme []themePair, scope string, depth int) *RNode {
	if depth > 64 {
		return nil
	}
	obj := jsonv.Obj(node)
	if obj == nil {
		return nil
	}
	theme := append([]themePair(nil), parentTheme...)
	applyTheme(&theme, node)
	ownID := jsonv.StrOr(jsonv.Get(node, "id"))
	path := ownID
	if prefix != "" {
		path = prefix + "/" + ownID
	}
	// Instance roots keep their own id; their children are addressed as instanceId/childId.
	wasRef := jsonv.StrIs(jsonv.Get(node, "type"), "ref")
	refTarget := jsonv.StrOr(jsonv.Get(node, "ref"))
	childPrefix := prefix
	if wasRef {
		childPrefix = path
	}

	// Component instance: clone the component, apply instance props and descendant overrides.
	if wasRef {
		merged, mergedScope, ok := r.expand(node, scope, theme, 0)
		if !ok {
			p := jsonv.Obj(jsonv.Clone(obj))
			p.Set("fill", "#FF3B3022")
			return leaf(path, "frame", p, "missing component")
		}
		if mergedScope != scope {
			// Entering a library: its theme axes default to its own first values.
			theme = addDefaults(theme, r.scopes[mergedScope].themes)
		}
		// The component root's own theme (merged under the instance's) governs its subtree.
		applyTheme(&theme, merged)
		node, scope = merged, mergedScope
		if obj = jsonv.Obj(node); obj == nil {
			return nil
		}
	}

	p := jsonv.NewObject()
	obj.Each(func(k string, v jsonv.Value) {
		if k != "children" {
			p.Set(k, r.subst(v, scope, theme, 0))
		}
	})
	if v, _ := p.Get("enabled"); jsonv.IsFalse(v) {
		return nil
	}
	ty := "frame"
	if t, ok := p.Get("type"); ok {
		if s, ok := jsonv.Str(t); ok && s != "ref" {
			ty = s
		}
	}
	out := leaf(path, ty, p, "")
	for _, k := range jsonv.Items(jsonv.Get(node, "children")) {
		if kid := r.resolveIn(k, childPrefix, theme, scope, depth+1); kid != nil {
			out.Kids = append(out.Kids, kid)
		}
	}
	if wasRef {
		out.Inst = refTarget
	}
	return out
}

func addDefaults(theme, defaults []themePair) []themePair {
	for _, d := range defaults {
		found := false
		for _, t := range theme {
			if t.axis == d.axis {
				found = true
				break
			}
		}
		if !found {
			theme = append(theme, d)
		}
	}
	return theme
}

// expand returns a `ref` instance as a plain node: the component cloned, then the instance's props and
// descendant overrides applied. A component may itself be an instance of another component (refs of
// refs), so the chain is expanded from the innermost component out. scope is where node is written; the
// result comes with the scope its content lives in, the component's library.
func (r *Resolver) expand(node jsonv.Value, scope string, theme []themePair, depth int) (jsonv.Value, string, bool) {
	ref, ok := jsonv.Str(jsonv.Get(node, "ref"))
	if !ok {
		return nil, "", false
	}
	key := scoped(scope, ref)
	comp, ok := r.index[key]
	if !ok {
		return nil, "", false
	}
	compScope := ""
	if a, _, ok := strings.Cut(key, ":"); ok {
		if _, known := r.scopes[a]; known {
			compScope = a
		}
	}
	var merged jsonv.Value
	var mergedScope string
	if jsonv.StrIs(jsonv.Get(comp, "type"), "ref") && depth < 16 {
		if merged, mergedScope, ok = r.expand(comp, compScope, theme, depth+1); !ok {
			return nil, "", false
		}
	} else {
		merged, mergedScope = jsonv.Clone(comp), compScope
	}
	// The instance's own values were written in scope; settle their variables there.
	outer := func(v jsonv.Value) jsonv.Value {
		if mergedScope == scope {
			return jsonv.Clone(v)
		}
		return settled(r.subst(v, scope, theme, 0))
	}
	m := jsonv.Obj(merged)
	inst := jsonv.Obj(node)
	if m == nil || inst == nil {
		return nil, "", false
	}
	inst.Each(func(k string, v jsonv.Value) {
		switch k {
		case "id", "type", "ref", "descendants", "reusable":
		default:
			if t := jsonv.Obj(jsonv.Get(m, "theme")); k == "theme" && t != nil {
				mergeInto(t, v)
				return
			}
			m.Set(k, outer(v))
		}
	})
	m.Set("reusable", false)
	if desc := jsonv.Obj(jsonv.Get(node, "descendants")); desc != nil {
		desc.Each(func(key string, over jsonv.Value) {
			applyOverride(m, key, outer(over))
		})
	}
	return m, mergedScope, true
}

// Inherited returns the props a `ref` instance inherits from its component (the chain expanded), without
// the instance's own values and without children. node is written in the open document. A library
// component's variables mean nothing there, so they are settled in the library under the theme the
// instance paints with; the open document's own `$variables` stay as written.
func (r *Resolver) Inherited(node jsonv.Value) jsonv.Value {
	theme := r.defaults()
	probe := jsonv.NewObject()
	probe.Set("type", "ref")
	probe.Set("ref", jsonv.Clone(jsonv.Get(node, "ref")))
	comp, scope, ok := r.expand(probe, "", theme, 0)
	if !ok {
		return nil
	}
	jsonv.Obj(comp).Remove("children")
	if scope == "" {
		return comp
	}
	// As resolveIn: the library's defaults, the component root's theme, the instance's over it.
	theme = addDefaults(theme, r.scopes[scope].themes)
	applyTheme(&theme, comp)
	applyTheme(&theme, node)
	return r.subst(comp, scope, theme, 0)
}

// subst replaces `$name` strings with the variable's value for the active theme, looked up in scope.
// Unknown names stay literal (so "$49" prices survive).
func (r *Resolver) subst(v jsonv.Value, scope string, theme []themePair, depth int) jsonv.Value {
	switch x := v.(type) {
	case string:
		if s, ok := strings.CutPrefix(x, settledMark); ok {
			return s
		}
		if strings.HasPrefix(x, "$") && depth < 16 {
			if val, ok := r.VarValue(scope, x[1:], theme); ok {
				return r.subst(val, scope, theme, depth+1)
			}
		}
		return x
	case *jsonv.Array:
		out := make([]jsonv.Value, len(x.Items))
		for i, it := range x.Items {
			out[i] = r.subst(it, scope, theme, depth)
		}
		return &jsonv.Array{Items: out}
	case *jsonv.Object:
		out := jsonv.NewObject()
		x.Each(func(k string, it jsonv.Value) { out.Set(k, r.subst(it, scope, theme, depth)) })
		return out
	}
	return v
}

// VarValue is the value of variable name in scope for the active theme.
func (r *Resolver) VarValue(scope, name string, theme []themePair) (jsonv.Value, bool) {
	sc, ok := r.scopes[scope]
	if !ok {
		return nil, false
	}
	def, ok := sc.vars.Get(name)
	if !ok {
		return nil, false
	}
	val := jsonv.Get(def, "value")
	list := jsonv.Arr(val)
	if list == nil {
		return jsonv.Clone(val), true
	}
	// Scan from the end, first entry whose theme fully matches wins; an un-themed entry is the base and
	// only applies when no themed entry matches, wherever it sits in the list.
	matches := func(e jsonv.Value) bool {
		t := jsonv.Obj(jsonv.Get(e, "theme"))
		if t == nil {
			return true
		}
		all := true
		t.Each(func(a string, v jsonv.Value) {
			s, isStr := jsonv.Str(v)
			found := false
			for _, tp := range theme {
				if tp.axis == a && isStr && tp.val == s {
					found = true
					break
				}
			}
			all = all && found
		})
		return all
	}
	themed := func(e jsonv.Value) bool {
		t := jsonv.Obj(jsonv.Get(e, "theme"))
		return t != nil && t.Len() > 0
	}
	pick := func(e jsonv.Value) (jsonv.Value, bool) {
		if o := jsonv.Obj(e); o != nil && o.Has("value") {
			v, _ := o.Get("value")
			return jsonv.Clone(v), true
		}
		return jsonv.Clone(e), true
	}
	for i := len(list.Items) - 1; i >= 0; i-- {
		if e := list.Items[i]; themed(e) && matches(e) {
			return pick(e)
		}
	}
	for i := len(list.Items) - 1; i >= 0; i-- {
		if e := list.Items[i]; matches(e) {
			return pick(e)
		}
	}
	if len(list.Items) > 0 {
		return pick(list.Items[0])
	}
	return nil, false
}

// Along returns key of each node on chain (a top-level node of root down to one of its plain
// descendants), its variables substituted for the theme active at that node.
func Along(root jsonv.Value, chain []jsonv.Value, key string) []jsonv.Value {
	r := &Resolver{scopes: map[string]scopeInfo{"": {varsOf(root), themeDefaults(root)}}, index: map[string]jsonv.Value{}}
	theme := r.defaults()
	out := make([]jsonv.Value, len(chain))
	for i, n := range chain {
		applyTheme(&theme, n)
		out[i] = r.subst(jsonv.Get(n, key), "", theme, 0)
	}
	return out
}

// settled marks `$name` strings that survived substitution (unknown names, "$49" prices) as settled.
func settled(v jsonv.Value) jsonv.Value {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "$") {
			return settledMark + x
		}
	case *jsonv.Array:
		for i, it := range x.Items {
			x.Items[i] = settled(it)
		}
	case *jsonv.Object:
		for _, k := range x.Keys() {
			it, _ := x.Get(k)
			x.Set(k, settled(it))
		}
	}
	return v
}

// applyOverride applies an override; key is an id path relative to the component root, e.g. `childId`
// or `nestedInstance/childId`.
func applyOverride(root *jsonv.Object, key string, over jsonv.Value) {
	head, rest, hasRest := strings.Cut(key, "/")
	parent, i := findDesc(root, head)
	if parent == nil {
		return
	}
	target := jsonv.Obj(parent.Items[i])
	switch {
	case hasRest && target != nil && jsonv.StrIs(jsonv.Get(target, "type"), "ref"):
		desc := jsonv.Obj(jsonv.Get(target, "descendants"))
		if desc == nil {
			desc = jsonv.NewObject()
			target.Set("descendants", desc)
		}
		slot, ok := desc.Get(rest)
		if !ok {
			slot = jsonv.NewObject()
			desc.Set(rest, slot)
		}
		if s := jsonv.Obj(slot); s != nil {
			mergeInto(s, over)
		}
	case hasRest:
		if target != nil {
			applyOverride(target, rest, over)
		}
	case jsonv.Obj(over) != nil && jsonv.Obj(over).Has("type"):
		id := jsonv.Get(parent.Items[i], "id")
		repl := jsonv.Obj(jsonv.Clone(over))
		repl.Set("id", jsonv.Clone(id))
		parent.Items[i] = repl
	default:
		if target != nil {
			mergeInto(target, over)
		}
	}
}

// findDesc finds the descendant with id: first among node's children, then depth first in each child.
func findDesc(node jsonv.Value, id string) (*jsonv.Array, int) {
	kids := jsonv.Arr(jsonv.Get(node, "children"))
	if kids == nil {
		return nil, 0
	}
	for i, k := range kids.Items {
		if jsonv.StrIs(jsonv.Get(k, "id"), id) {
			return kids, i
		}
	}
	for _, k := range kids.Items {
		if a, i := findDesc(k, id); a != nil {
			return a, i
		}
	}
	return nil, 0
}

func mergeInto(target *jsonv.Object, over jsonv.Value) {
	if o := jsonv.Obj(over); o != nil {
		o.Each(func(k string, v jsonv.Value) { target.Set(k, jsonv.Clone(v)) })
	}
}
