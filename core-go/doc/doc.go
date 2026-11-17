// Package doc is the .pen-compatible document model (core/src/doc.rs). Nodes stay JSON values so
// unknown properties round-trip losslessly between .pen files and Nalarupa.
package doc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// FormatVersion is the .pen version documents are saved as.
const FormatVersion = "2.19"

// Root is the parent id of top-level nodes.
const Root = "document"

var nodeTypes = []string{
	"frame", "group", "rectangle", "ellipse", "polygon", "line", "path", "text", "note", "prompt",
	"context", "icon", "script", "browser", "ref",
}

// Doc is a document: its JSON root object and the file it was loaded from ("" when unsaved).
type Doc struct {
	Root *jsonv.Object
	Path string
	// IDs is the generator new ids are drawn from; nil is the process-wide one. An execute run sets its own.
	IDs *IDSource
}

// New returns an empty document.
func New() *Doc {
	root := jsonv.NewObject()
	root.Set("version", FormatVersion)
	root.Set("children", jsonv.NewArray())
	return &Doc{Root: root}
}

// FromJSON parses a document; a leading BOM is ignored and a missing `children` becomes [].
func FromJSON(text []byte) (*Doc, error) {
	for bytes.HasPrefix(text, []byte{0xEF, 0xBB, 0xBF}) {
		text = text[3:]
	}
	v, err := jsonv.Parse(text)
	if err != nil {
		return nil, fmt.Errorf("invalid document JSON: %v", err)
	}
	root := jsonv.Obj(v)
	if root == nil {
		return nil, errors.New("document must be a JSON object")
	}
	if c, _ := root.Get("children"); jsonv.Arr(c) == nil {
		root.Set("children", jsonv.NewArray())
	}
	return &Doc{Root: root}, nil
}

// Load reads a document from path.
func Load(path string) (*Doc, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %s", path, IOError(err))
	}
	if !utf8.Valid(text) {
		return nil, fmt.Errorf("cannot read %s: stream did not contain valid UTF-8", path) // read_to_string's error
	}
	d, err := FromJSON(text)
	if err != nil {
		return nil, err
	}
	d.Path = path
	return d, nil
}

// Save writes the document atomically: a temp file next to the target, then a rename over it. A
// symlink is followed (the rename would replace the link, leaving the real file stale); a failed save
// leaves no temp file.
func (d *Doc) Save(path string) error {
	text := jsonv.MarshalPretty(d.Root)
	if dir, ok := ParentPath(path); ok {
		if err := CreateDirAll(dir); err != nil {
			return errors.New(IOError(err))
		}
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		if _, lerr := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || lerr == nil {
			return fmt.Errorf("cannot resolve %s: %s", path, IOError(err))
		}
		target = path
	}
	tmp := target + ".tmp-nalarupa"
	if err := os.WriteFile(tmp, text, 0o666); err != nil {
		RemoveFile(tmp)
		return fmt.Errorf("cannot write %s: %s", tmp, IOError(err))
	}
	if err := Rename(tmp, target); err != nil {
		RemoveFile(tmp)
		return fmt.Errorf("cannot save %s: %s", path, IOError(err))
	}
	return nil
}

// EnsureIDs gives every node without a usable id a fresh one and makes repeated ids unique (the first
// node keeps its id). A new id follows from the node's place in the tree, so an imported library
// (repaired on every open, never saved) gets the same ids each time.
func (d *Doc) EnsureIDs() {
	taken := map[string]bool{}
	collectIDs(d.Children(), taken)
	fixIDs(d.Children(), nil, map[string]bool{}, taken)
}

// Dir is the folder of the document file, or "" when unsaved.
func (d *Doc) Dir() string {
	if d.Path == "" {
		return ""
	}
	return filepath.Dir(d.Path)
}

// Title is the file name without its extension, or "Untitled" for an unsaved document.
func (d *Doc) Title() string {
	if d.Path == "" {
		return "Untitled"
	}
	base := filepath.Base(d.Path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// Children is the top-level nodes.
func (d *Doc) Children() []jsonv.Value {
	c, _ := d.Root.Get("children")
	return jsonv.Items(c)
}

// ── lookup ──────────────────────────────────────────────────────────────────

// Find returns the first node with id, or nil.
func (d *Doc) Find(id string) jsonv.Value {
	return FindIn(d.Children(), id)
}

// TopIndex is the index of the top-level node that holds id (an instance path by its head), or -1.
func (d *Doc) TopIndex(id string) int {
	head, _, _ := strings.Cut(id, "/")
	for i, n := range d.Children() {
		if jsonv.StrIs(jsonv.Get(n, "id"), head) || FindIn([]jsonv.Value{n}, head) != nil {
			return i
		}
	}
	return -1
}

func (d *Doc) indexPath(id string) []int {
	var acc []int
	if indexPathIn(d.Children(), id, &acc) {
		return acc
	}
	return nil
}

func (d *Doc) nodeAt(ip []int) jsonv.Value {
	cur := d.Children()[ip[0]]
	for _, i := range ip[1:] {
		cur = jsonv.Items(jsonv.Get(cur, "children"))[i]
	}
	return cur
}

func (d *Doc) childrenAt(parentIP []int) *jsonv.Array {
	parent := d.Root
	if len(parentIP) > 0 {
		parent = jsonv.Obj(d.nodeAt(parentIP))
	}
	c, _ := parent.Get("children")
	a := jsonv.Arr(c)
	if a == nil {
		a = jsonv.NewArray()
		parent.Set("children", a)
	}
	return a
}

// ParentOf returns the parent id (or Root) and the index among siblings of id.
func (d *Doc) ParentOf(id string) (string, int, bool) {
	ip := d.indexPath(id)
	if ip == nil {
		return "", 0, false
	}
	if len(ip) == 1 {
		return Root, ip[0], true
	}
	return jsonv.StrOr(jsonv.Get(d.nodeAt(ip[:len(ip)-1]), "id")), ip[len(ip)-1], true
}

// Outermost returns the ids in ids that sit inside none of the other listed layers, in their given
// order. One walk of the document, whatever the number of ids.
func (d *Doc) Outermost(ids []string) []string {
	listed := map[string]bool{}
	for _, id := range ids {
		listed[id] = true
	}
	nested := map[string]bool{}
	var visit func(nodes []jsonv.Value, under bool)
	visit = func(nodes []jsonv.Value, under bool) {
		for _, n := range nodes {
			id := jsonv.StrOr(jsonv.Get(n, "id"))
			if under {
				nested[id] = true
			}
			if kids := jsonv.Arr(jsonv.Get(n, "children")); kids != nil {
				visit(kids.Items, under || listed[id])
			}
		}
	}
	visit(d.Children(), false)
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		// A component instance's rendered descendants ("inst/child") are not in the tree: judge them by the instance.
		base, _, _ := strings.Cut(id, "/")
		if nested[base] || seen[id] {
			continue
		}
		under := false
		for i := range len(id) {
			if id[i] == '/' && listed[id[:i]] {
				under = true
				break
			}
		}
		if !under {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// indexPaths finds the index path of the first node with each of ids in one walk.
func (d *Doc) indexPaths(ids []string) map[string][]int {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	found := map[string][]int{}
	var visit func(nodes []jsonv.Value, acc []int)
	visit = func(nodes []jsonv.Value, acc []int) {
		for i, n := range nodes {
			if len(want) == 0 {
				return
			}
			acc := append(acc, i)
			if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok && want[id] {
				delete(want, id)
				found[id] = slices.Clone(acc)
			}
			if kids := jsonv.Arr(jsonv.Get(n, "children")); kids != nil {
				visit(kids.Items, acc)
			}
		}
	}
	visit(d.Children(), nil)
	return found
}

// AllIDs lists every node id in tree order.
func (d *Doc) AllIDs() []string {
	out := []string{}
	Walk(d.Children(), func(n jsonv.Value) {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
			out = append(out, id)
		}
	})
	return out
}

// GenID returns a fresh id no node has.
func (d *Doc) GenID() string {
	for {
		if id := d.randomID(); d.Find(id) == nil {
			return id
		}
	}
}

// ── mutations ───────────────────────────────────────────────────────────────

// Insert adds node (with fresh ids) under parent at index (-1 = the end) and returns its id.
func (d *Doc) Insert(parent string, node jsonv.Value, index int) (string, error) {
	if err := validateNode(node); err != nil {
		return "", err
	}
	d.assignIDs(node, &[]remapEntry{})
	id := jsonv.StrOr(jsonv.Get(node, "id"))
	parentIP, err := d.containerPath(parent)
	if err != nil {
		return "", err
	}
	if err := checkDepth(len(parentIP), node); err != nil {
		return "", err
	}
	kids := d.childrenAt(parentIP)
	at := len(kids.Items)
	if index >= 0 && index < at {
		at = index
	}
	kids.Items = slices.Insert(kids.Items, at, node)
	return id, nil
}

// ImportComponent inserts a component pasted from another document at the top level with its ids kept
// (instance overrides are keyed on them).
func (d *Doc) ImportComponent(node jsonv.Value) (string, error) {
	if err := validateNode(node); err != nil {
		return "", err
	}
	var ids []string
	Walk([]jsonv.Value{node}, func(n jsonv.Value) { ids = append(ids, jsonv.StrOr(jsonv.Get(n, "id"))) })
	for i, id := range ids {
		if id == "" || slices.Contains(ids[:i], id) || d.Find(id) != nil {
			return "", fmt.Errorf("component id '%s' is missing or already used", id)
		}
	}
	return d.insertPrepared(Root, node)
}

// Update merges properties into a node; null removes a property. Paths with `/` address descendants of
// component instances and become overrides.
func (d *Doc) Update(path string, data jsonv.Value) error {
	id, _, _ := strings.Cut(path, "/")
	ip := d.indexPath(id)
	if ip == nil {
		return fmt.Errorf("node not found: %s", id)
	}
	return d.updateAt(ip, path, data)
}

func (d *Doc) updateAt(ip []int, path string, data jsonv.Value) error {
	if err := d.checkUpdate(ip, path, data); err != nil {
		return err
	}
	props := jsonv.Obj(data)
	node := jsonv.Obj(d.nodeAt(ip))
	if _, rest, ok := strings.Cut(path, "/"); ok {
		desc := ensureObj(node, "descendants")
		entry, ok := desc.Get(rest)
		if !ok {
			entry = jsonv.NewObject()
			desc.Set(rest, entry)
		}
		mergeProps(desc, rest, entry, props, true)
		return nil
	}
	mergeInto(node, props, false)
	return nil
}

// checkUpdate is everything that can make updateAt fail, checked before it changes anything.
func (d *Doc) checkUpdate(ip []int, path string, data jsonv.Value) error {
	if jsonv.Obj(data) == nil {
		return errors.New("update data must be an object")
	}
	if inst, _, ok := strings.Cut(path, "/"); ok {
		if !jsonv.StrIs(jsonv.Get(d.nodeAt(ip), "type"), "ref") {
			return fmt.Errorf("'%s' is not a component instance; '/' paths only address instance descendants", inst)
		}
		return checkLevels(overrideLevels(len(ip)), data)
	}
	return checkDepth(len(ip)-1, data)
}

// UpdateItem is one node path and its properties for UpdateAll.
type UpdateItem struct {
	Path string
	Data jsonv.Value
}

// UpdateAll is Update for many nodes, found in one walk. All or nothing: every item is checked before
// the first one is applied.
func (d *Doc) UpdateAll(items []UpdateItem) error {
	bases := make([]string, len(items))
	for i, it := range items {
		bases[i], _, _ = strings.Cut(it.Path, "/")
	}
	paths := d.indexPaths(bases)
	found := make([][]int, 0, len(items))
	for i, it := range items {
		ip, ok := paths[bases[i]]
		if !ok {
			return fmt.Errorf("node not found: %s", bases[i])
		}
		if err := d.checkUpdate(ip, it.Path, it.Data); err != nil {
			return err
		}
		found = append(found, ip)
	}
	for i, it := range items {
		if err := d.updateAt(found[i], it.Path, it.Data); err != nil {
			return err
		}
	}
	return nil
}

const noInstanceDelete = "cannot delete descendants of component instances; override them with {enabled:false}"

// Delete removes a node.
func (d *Doc) Delete(path string) error {
	if strings.Contains(path, "/") {
		return errors.New(noInstanceDelete)
	}
	ip := d.indexPath(path)
	if ip == nil {
		return fmt.Errorf("node not found: %s", path)
	}
	d.removeAt(ip)
	return nil
}

func (d *Doc) removeAt(ip []int) jsonv.Value {
	kids := d.childrenAt(ip[:len(ip)-1])
	last := ip[len(ip)-1]
	n := kids.Items[last]
	kids.Items = slices.Delete(kids.Items, last, last+1)
	return n
}

// DeleteAll is Delete for many nodes, found in one walk.
func (d *Doc) DeleteAll(ids []string) error {
	for _, id := range ids {
		if strings.Contains(id, "/") {
			return errors.New(noInstanceDelete)
		}
	}
	found := d.indexPaths(ids)
	paths := make([][]int, 0, len(ids))
	for _, id := range ids {
		ip, ok := found[id]
		if !ok {
			return fmt.Errorf("node not found: %s", id)
		}
		paths = append(paths, ip)
	}
	// Last first, so a removal never moves a node that is still to go.
	slices.SortFunc(paths, func(a, b []int) int { return slices.Compare(b, a) })
	paths = slices.CompactFunc(paths, slices.Equal)
	for _, ip := range paths {
		d.removeAt(ip)
	}
	return nil
}

// MoveAll moves each of ids in turn to the end of parent, finding them all in one walk.
func (d *Doc) MoveAll(ids []string, parent string) error {
	for _, id := range ids {
		if strings.Contains(id, "/") {
			return errors.New("cannot move descendants of component instances")
		}
	}
	tip, err := d.containerPath(parent)
	if err != nil {
		return err
	}
	found := d.indexPaths(ids)
	seen := map[string]bool{}
	var paths [][]int
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		ip, ok := found[id]
		if !ok {
			return fmt.Errorf("node not found: %s", id)
		}
		if len(tip) >= len(ip) && slices.Equal(tip[:len(ip)], ip) {
			return errors.New("cannot move a node into itself")
		}
		if err := checkDepth(len(tip), d.nodeAt(ip)); err != nil {
			return err
		}
		paths = append(paths, ip)
	}
	// Take them out last first (see DeleteAll), then put them back in the order given.
	order := make([]int, len(paths))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return slices.Compare(paths[b], paths[a]) })
	taken := make([]jsonv.Value, len(paths))
	for _, k := range order {
		taken[k] = d.removeAt(paths[k])
	}
	tip, err = d.containerPath(parent)
	if err != nil {
		return err
	}
	kids := d.childrenAt(tip)
	kids.Items = append(kids.Items, taken...)
	return nil
}

// MoveNode moves a node under parent ("" = its current parent) at index (-1 = the end).
func (d *Doc) MoveNode(path, parent string, index int) error {
	if strings.Contains(path, "/") {
		return errors.New("cannot move descendants of component instances")
	}
	ip := d.indexPath(path)
	if ip == nil {
		return fmt.Errorf("node not found: %s", path)
	}
	target := parent
	if target == "" {
		target, _, _ = d.ParentOf(path)
	}
	if target != Root {
		node := d.Find(path)
		if target == path || FindIn(jsonv.Items(jsonv.Get(node, "children")), target) != nil {
			return errors.New("cannot move a node into itself")
		}
	}
	tip, err := d.containerPath(target)
	if err != nil {
		return err
	}
	if err := checkDepth(len(tip), d.Find(path)); err != nil {
		return err
	}
	node := d.removeAt(ip)
	tip, err = d.containerPath(target)
	if err != nil {
		return err
	}
	kids := d.childrenAt(tip)
	at := len(kids.Items)
	if index >= 0 && index < at {
		at = index
	}
	kids.Items = slices.Insert(kids.Items, at, node)
	return nil
}

// Copy copies source (already resolved when it addresses an instance descendant) under parent, with
// data merged in (nil = none). Reusable sources become connected `ref` instances.
func (d *Doc) Copy(source jsonv.Value, parent string, data jsonv.Value) (string, error) {
	reusable := jsonv.IsTrue(jsonv.Get(source, "reusable"))
	var node jsonv.Value
	if reusable {
		o := jsonv.NewObject()
		o.Set("type", "ref")
		o.Set("ref", jsonv.Clone(jsonv.Get(source, "id")))
		node = o
	} else {
		if err := validateNode(source); err != nil {
			return "", fmt.Errorf("source: %v", err)
		}
		node = jsonv.Clone(source)
	}
	var remap []remapEntry
	d.assignIDs(node, &remap)
	obj := jsonv.Obj(node)
	obj.Remove("reusable")
	if !reusable {
		obj.Remove("x")
		obj.Remove("y")
	}
	if dd := jsonv.Obj(data); dd != nil {
		top := jsonv.Obj(jsonv.Clone(dd))
		desc, _ := top.Remove("descendants")
		mergeInto(obj, top, false)
		if desc := jsonv.Obj(desc); desc != nil {
			var bad error
			desc.Each(func(key string, over jsonv.Value) {
				if o := jsonv.Obj(over); bad == nil && o != nil && o.Has("type") {
					if err := validateNode(over); err != nil {
						bad = fmt.Errorf("descendant '%s': %v", key, err)
					}
				}
			})
			if bad != nil {
				return "", bad
			}
			if jsonv.StrIs(jsonv.Get(node, "type"), "ref") {
				obj.Set("descendants", desc)
			} else {
				for _, key := range desc.Keys() {
					over, _ := desc.Get(key)
					target, ok := "", false
					for _, e := range remap {
						if e.old == key {
							target, ok = e.new, true
							break
						}
					}
					if !ok {
						target, ok = findByName(node, key)
					}
					if !ok {
						return "", fmt.Errorf("descendant '%s' not found in copied node", key)
					}
					parentArr, i := findSlot(node, target)
					if o := jsonv.Obj(over); o != nil && o.Has("type") {
						repl := jsonv.Clone(over)
						d.assignIDs(repl, &[]remapEntry{})
						if parentArr == nil {
							node, obj = repl, jsonv.Obj(repl)
						} else {
							parentArr.Items[i] = repl
						}
						continue
					}
					props := jsonv.Obj(over)
					if props == nil {
						return "", errors.New("descendant override must be an object")
					}
					slot := node
					if parentArr != nil {
						slot = parentArr.Items[i]
					}
					mergeInto(jsonv.Obj(slot), props, false)
				}
			}
		}
	}
	return d.insertPrepared(parent, node)
}

// Replace swaps the node at path for node (fresh ids). An instance descendant path stores node as a
// full-replacement override.
func (d *Doc) Replace(path string, node jsonv.Value) (string, error) {
	if err := validateNode(node); err != nil {
		return "", err
	}
	if inst, rest, ok := strings.Cut(path, "/"); ok {
		instIP := d.indexPath(inst)
		if instIP == nil {
			return "", fmt.Errorf("node not found: %s", inst)
		}
		instNode := jsonv.Obj(d.nodeAt(instIP))
		if !jsonv.StrIs(jsonv.Get(instNode, "type"), "ref") {
			return "", fmt.Errorf("'%s' is not a component instance", inst)
		}
		if err := checkLevels(overrideLevels(len(instIP)), node); err != nil {
			return "", err
		}
		d.assignIDs(node, &[]remapEntry{})
		jsonv.Obj(node).Set("id", rest[strings.LastIndexByte(rest, '/')+1:])
		ensureObj(instNode, "descendants").Set(rest, node)
		return path, nil
	}
	ip := d.indexPath(path)
	if ip == nil {
		return "", fmt.Errorf("node not found: %s", path)
	}
	if err := checkDepth(len(ip)-1, node); err != nil {
		return "", err
	}
	d.assignIDs(node, &[]remapEntry{})
	d.childrenAt(ip[:len(ip)-1]).Items[ip[len(ip)-1]] = node
	return jsonv.StrOr(jsonv.Get(node, "id")), nil
}

// SetVariables adds (or with replace, replaces) variable definitions and registers their theme axes.
func (d *Doc) SetVariables(vars jsonv.Value, replace bool) error {
	vs := jsonv.Obj(vars)
	if vs == nil {
		return errors.New("variables must be an object")
	}
	for _, name := range vs.Keys() {
		def, _ := vs.Get(name)
		if strings.HasPrefix(name, "$") {
			return fmt.Errorf("variable name '%s' must not start with '$'", name)
		}
		ty, ok := jsonv.Str(jsonv.Get(def, "type"))
		if !ok {
			return fmt.Errorf("variable '%s' needs {type, value}", name)
		}
		if o := jsonv.Obj(def); !slices.Contains([]string{"color", "number", "string", "boolean"}, ty) || !o.Has("value") {
			return fmt.Errorf("variable '%s': type must be color|number|string|boolean and value is required", name)
		}
	}
	root := d.Root
	if cur, _ := root.Get("variables"); replace || jsonv.Obj(cur) == nil {
		root.Set("variables", jsonv.NewObject())
	}
	type axisVal struct{ axis, val string }
	var axes []axisVal
	// Axes where some variable has an un-themed entry: that entry is the default, so the axis needs a base theme.
	var based []string
	cur, _ := root.Get("variables")
	target := jsonv.Obj(cur)
	vs.Each(func(name string, def jsonv.Value) {
		if list := jsonv.Arr(jsonv.Get(def, "value")); list != nil {
			var seen []string
			for _, entry := range list.Items {
				if theme := jsonv.Obj(jsonv.Get(entry, "theme")); theme != nil {
					theme.Each(func(axis string, val jsonv.Value) {
						axes = append(axes, axisVal{axis, jsonv.StrOr(val)})
						seen = append(seen, axis)
					})
				}
			}
			for _, axis := range seen {
				missing := slices.ContainsFunc(list.Items, func(e jsonv.Value) bool {
					return jsonv.Obj(jsonv.Get(e, "theme")) == nil || !jsonv.Obj(jsonv.Get(e, "theme")).Has(axis)
				})
				if missing && !slices.Contains(based, axis) {
					based = append(based, axis)
				}
			}
		}
		target.Set(name, jsonv.Clone(def))
	})
	if len(axes) > 0 {
		if !root.Has("themes") {
			root.Set("themes", jsonv.NewObject())
		}
		themes := ensureObj(root, "themes")
		list := func(axis string) *jsonv.Array {
			v, _ := themes.Get(axis)
			a := jsonv.Arr(v)
			if a == nil {
				a = jsonv.NewArray()
				themes.Set(axis, a)
			}
			return a
		}
		for _, axis := range based {
			if a := list(axis); len(a.Items) == 0 {
				a.Items = append(a.Items, "default")
			}
		}
		for _, av := range axes {
			a := list(av.axis)
			if !slices.ContainsFunc(a.Items, func(v jsonv.Value) bool { return jsonv.StrIs(v, av.val) }) {
				a.Items = append(a.Items, av.val)
			}
		}
	}
	return nil
}

// Variables returns {variables, themes}, each {} when the document has none.
func (d *Doc) Variables() jsonv.Value {
	out := jsonv.NewObject()
	for _, k := range []string{"variables", "themes"} {
		v, ok := d.Root.Get(k)
		if !ok {
			v = jsonv.NewObject()
		}
		out.Set(k, jsonv.Clone(v))
	}
	return out
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (d *Doc) containerPath(parent string) ([]int, error) {
	if parent == Root || parent == "" {
		return []int{}, nil
	}
	if strings.Contains(parent, "/") {
		return nil, errors.New("cannot insert into a component instance; use Replace or descendants overrides")
	}
	node := d.Find(parent)
	if node == nil {
		return nil, fmt.Errorf("parent not found: %s", parent)
	}
	ty, ok := jsonv.Str(jsonv.Get(node, "type"))
	if ty == "frame" || ty == "group" {
		return d.indexPath(parent), nil
	}
	if !ok {
		ty = "?"
	}
	return nil, fmt.Errorf("only frame and group nodes can have children (parent '%s' is %s)", parent, ty)
}

func (d *Doc) insertPrepared(parent string, node jsonv.Value) (string, error) {
	id := jsonv.StrOr(jsonv.Get(node, "id"))
	ip, err := d.containerPath(parent)
	if err != nil {
		return "", err
	}
	if err := checkDepth(len(ip), node); err != nil {
		return "", err
	}
	kids := d.childrenAt(ip)
	kids.Items = append(kids.Items, node)
	return id, nil
}

type remapEntry struct{ old, new string }

// assignIDs gives a subtree fresh unique ids (input ids are never trusted), recording old→new.
func (d *Doc) assignIDs(node jsonv.Value, remap *[]remapEntry) {
	var fresh string
	for {
		fresh = d.randomID()
		if d.Find(fresh) == nil && !slices.ContainsFunc(*remap, func(e remapEntry) bool { return e.new == fresh }) {
			break
		}
	}
	*remap = append(*remap, remapEntry{jsonv.StrOr(jsonv.Get(node, "id")), fresh})
	obj := jsonv.Obj(node)
	obj.Set("id", fresh)
	if kids := jsonv.Arr(jsonv.Get(node, "children")); kids != nil {
		for _, k := range kids.Items {
			d.assignIDs(k, remap)
		}
	}
}

func collectIDs(nodes []jsonv.Value, out map[string]bool) {
	for _, n := range nodes {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
			out[id] = true
		}
		collectIDs(jsonv.Items(jsonv.Get(n, "children")), out)
	}
}

func fixIDs(nodes []jsonv.Value, at []int, seen, taken map[string]bool) {
	for i, n := range nodes {
		obj := jsonv.Obj(n)
		if obj == nil {
			continue
		}
		at := append(at, i)
		id, ok := jsonv.Str(jsonv.Get(obj, "id"))
		if !ok || id == "" || strings.Contains(id, "/") || seen[id] {
			for attempt := uint64(0); ; attempt++ {
				if id = placeID(at, attempt); !taken[id] {
					taken[id] = true
					break
				}
			}
		}
		obj.Set("id", id)
		seen[id] = true
		if kids := jsonv.Arr(jsonv.Get(obj, "children")); kids != nil {
			fixIDs(kids.Items, at, seen, taken)
		}
	}
}

const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// placeID is a five-character id (like randomID) hashed (FNV-1a) from a node's child indices from the
// root and an attempt number.
func placeID(at []int, attempt uint64) string {
	h := uint64(0xcbf29ce484222325)
	vals := make([]uint64, 0, len(at)+2)
	for _, i := range at {
		vals = append(vals, uint64(i))
	}
	for _, v := range append(vals, ^uint64(0), attempt) {
		for range 8 {
			h = (h ^ (v & 0xff)) * 0x100000001b3
			v >>= 8
		}
	}
	return idFrom(h)
}

func idFrom(r uint64) string {
	var b [5]byte
	for i := range b {
		b[i] = idAlphabet[r%uint64(len(idAlphabet))]
		r /= uint64(len(idAlphabet))
	}
	return string(b[:])
}

// FindIn returns the first node with id in nodes and their descendants, or nil.
func FindIn(nodes []jsonv.Value, id string) jsonv.Value {
	for _, n := range nodes {
		if jsonv.StrIs(jsonv.Get(n, "id"), id) {
			return n
		}
		if f := FindIn(jsonv.Items(jsonv.Get(n, "children")), id); f != nil {
			return f
		}
	}
	return nil
}

func indexPathIn(nodes []jsonv.Value, id string, acc *[]int) bool {
	for i, n := range nodes {
		*acc = append(*acc, i)
		if jsonv.StrIs(jsonv.Get(n, "id"), id) {
			return true
		}
		if indexPathIn(jsonv.Items(jsonv.Get(n, "children")), id, acc) {
			return true
		}
		*acc = (*acc)[:len(*acc)-1]
	}
	return false
}

// findSlot locates the node with id inside node: the array holding it and its index, or a nil array
// when it is node itself (not found is ruled out by the caller).
func findSlot(node jsonv.Value, id string) (*jsonv.Array, int) {
	if jsonv.StrIs(jsonv.Get(node, "id"), id) {
		return nil, 0
	}
	var walk func(n jsonv.Value) (*jsonv.Array, int, bool)
	walk = func(n jsonv.Value) (*jsonv.Array, int, bool) {
		kids := jsonv.Arr(jsonv.Get(n, "children"))
		if kids == nil {
			return nil, 0, false
		}
		for i, k := range kids.Items {
			if jsonv.StrIs(jsonv.Get(k, "id"), id) {
				return kids, i, true
			}
			if a, j, ok := walk(k); ok {
				return a, j, true
			}
		}
		return nil, 0, false
	}
	a, i, _ := walk(node)
	return a, i
}

func findByName(node jsonv.Value, name string) (string, bool) {
	var hits []string
	Walk([]jsonv.Value{node}, func(n jsonv.Value) {
		if jsonv.StrIs(jsonv.Get(n, "name"), name) {
			hits = append(hits, jsonv.StrOr(jsonv.Get(n, "id")))
		}
	})
	if len(hits) == 1 {
		return hits[0], true
	}
	return "", false
}

// Walk calls f on every node of nodes and their descendants, parents first.
func Walk(nodes []jsonv.Value, f func(jsonv.Value)) {
	for _, n := range nodes {
		f(n)
		Walk(jsonv.Items(jsonv.Get(n, "children")), f)
	}
}

// MaxLevels is the deepest JSON nesting a document accepts. serde_json refuses documents more than 128
// levels deep, so anything deeper would save but never reopen. A node costs two levels (its object and
// the `children` array it sits in); 122 fits 60 nested frames and leaves a few levels of headroom.
const MaxLevels = 122

func jsonDepth(v jsonv.Value) int {
	deepest := 0
	switch x := v.(type) {
	case *jsonv.Object:
		x.Each(func(_ string, it jsonv.Value) { deepest = max(deepest, jsonDepth(it)) })
	case *jsonv.Array:
		for _, it := range x.Items {
			deepest = max(deepest, jsonDepth(it))
		}
	default:
		return 0
	}
	return 1 + deepest
}

// checkLevels refuses a value that would sit `above` levels down and push the document past MaxLevels.
func checkLevels(above int, v jsonv.Value) error {
	if above+jsonDepth(v) > MaxLevels {
		return errors.New("nesting too deep: reduce how deeply frames, groups and property values are nested")
	}
	return nil
}

// checkDepth refuses a node (with everything in it) that would sit under parentDepth ancestor nodes.
func checkDepth(parentDepth int, node jsonv.Value) error {
	return checkLevels(2+2*parentDepth, node)
}

// overrideLevels is the levels above an override of an instance that is instDepth nodes deep: the
// instance's object and its `descendants` object.
func overrideLevels(instDepth int) int {
	return 2*instDepth + 2
}

// validateNode checks a node and, recursively, every child: each must be an object with a valid `type`.
func validateNode(node jsonv.Value) error {
	obj := jsonv.Obj(node)
	if obj == nil {
		s := []rune(string(jsonv.Marshal(node)))
		return fmt.Errorf("node must be an object, got %s", string(s[:min(len(s), 40)]))
	}
	ty, ok := jsonv.Str(jsonv.Get(node, "type"))
	if !ok {
		return errors.New("node needs a string `type`")
	}
	if !slices.Contains(nodeTypes, ty) {
		return fmt.Errorf("unknown node type '%s' (valid: %s)", ty, strings.Join(nodeTypes, ", "))
	}
	if _, isStr := jsonv.Get(node, "ref").(string); ty == "ref" && !isStr {
		return errors.New("ref nodes need a `ref` id")
	}
	children, has := obj.Get("children")
	if has && children != nil && ty != "frame" && ty != "group" {
		return fmt.Errorf("only frame and group nodes can have children (%s cannot)", ty)
	}
	switch c := children.(type) {
	case nil:
	case *jsonv.Array:
		for i, k := range c.Items {
			if err := validateNode(k); err != nil {
				return fmt.Errorf("children[%d] of '%s': %v", i, ty, err)
			}
		}
	default:
		return fmt.Errorf("`children` of '%s' must be an array", ty)
	}
	return nil
}

// ensureObj returns parent[k] as an object, putting {} there (in place, or at the end) when it is not one.
func ensureObj(parent *jsonv.Object, k string) *jsonv.Object {
	v, _ := parent.Get(k)
	o := jsonv.Obj(v)
	if o == nil {
		o = jsonv.NewObject()
		parent.Set(k, o)
	}
	return o
}

// mergeProps merges data into parent[k] (made an object first).
func mergeProps(parent *jsonv.Object, k string, cur jsonv.Value, data *jsonv.Object, keepType bool) {
	o := jsonv.Obj(cur)
	if o == nil {
		o = jsonv.NewObject()
		parent.Set(k, o)
	}
	mergeInto(o, data, keepType)
}

// mergeInto merges data into target; null removes a property. keepType allows `type` inside instance
// overrides (replacement nodes).
func mergeInto(target, data *jsonv.Object, keepType bool) {
	data.Each(func(k string, v jsonv.Value) {
		if k == "id" || k == "ref" || k == "children" || (k == "type" && !keepType) {
			return
		}
		if v == nil {
			target.Remove(k)
		} else {
			target.Set(k, jsonv.Clone(v))
		}
	})
}
