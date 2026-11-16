package engine

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/history"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// templates are copies of core/templates/*.json (TestTemplatesMatchTheRustCore keeps them equal).
//
//go:embed templates/*.json
var templates embed.FS

// dropPending: a file saved while a background image was loading keeps its pending url, and nothing will ever
// resolve it, so it counts as a failed load (empty url, like ResolvePending) and the node settles. Urls in
// inflight belong to a download of this process, which still resolves them.
func dropPending(v jsonv.Value, inflight map[string]bool) {
	switch x := v.(type) {
	case *jsonv.Array:
		for _, it := range x.Items {
			dropPending(it, inflight)
		}
	case *jsonv.Object:
		for _, k := range x.Keys() {
			it, _ := x.Get(k)
			if s, ok := it.(string); ok {
				if k == "url" && svg.IsPendingURL(s) && !inflight[s] {
					x.Set(k, "")
				}
				continue
			}
			dropPending(it, inflight)
		}
	}
}

// dropPlaceholder: a file saved while an agent was still building a frame keeps its `placeholder` flag, and no
// run will ever clear it, so the frame would show "Generating" forever. Frames in pens are being drawn by an
// AI Pen of this process, which still clears them (only passed when the same file is reopened).
func dropPlaceholder(v jsonv.Value, pens []string) {
	switch x := v.(type) {
	case *jsonv.Array:
		for _, it := range x.Items {
			dropPlaceholder(it, pens)
		}
	case *jsonv.Object:
		if p, _ := x.Get("placeholder"); jsonv.StrIs(jsonv.Get(x, "type"), "frame") && jsonv.IsTrue(p) {
			if id, ok := jsonv.Str(jsonv.Get(x, "id")); !ok || !slices.Contains(pens, id) {
				x.Set("placeholder", false)
			}
		}
		x.Each(func(_ string, it jsonv.Value) { dropPlaceholder(it, pens) })
	}
}

// isFile reports a regular file (Rust's `Path::is_file`).
func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// exists is Rust's `Path::exists`.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// localizeImages copies every image fill whose url is a file directly in from into `<dir>/images` and points
// the url at the copy (`./images/<name>`, relative to the document). Missing files keep their url. Every file
// and folder it creates goes into created, so a failed save can remove them again.
func localizeImages(v jsonv.Value, from, dir string, created *[]string) error {
	switch x := v.(type) {
	case *jsonv.Array:
		for _, it := range x.Items {
			if err := localizeImages(it, from, dir, created); err != nil {
				return err
			}
		}
	case *jsonv.Object:
		if jsonv.StrIs(jsonv.Get(x, "type"), "image") {
			if url, ok := jsonv.Str(jsonv.Get(x, "url")); ok {
				if parent, ok := doc.ParentPath(url); ok && doc.SamePath(parent, from) && isFile(url) {
					name, err := copyImage(url, doc.JoinPath(dir, "images"), created)
					if err != nil {
						return err
					}
					x.Set("url", "./images/"+name)
				}
			}
		}
		for _, k := range x.Keys() {
			it, _ := x.Get(k)
			if err := localizeImages(it, from, dir, created); err != nil {
				return err
			}
		}
	}
	return nil
}

// docDir is the folder of the document file (Rust's `Doc::dir`); false when it is unsaved.
func docDir(d *doc.Doc) (string, bool) {
	if d.Path == "" {
		return "", false
	}
	return doc.ParentPath(d.Path)
}

// stemExt splits a file name as Rust's `file_stem` and `extension` do: a leading dot is part of the stem, and
// ext includes the dot ("" when there is none).
func stemExt(name string) (string, string) {
	if name == ".." {
		return name, ""
	}
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, ""
	}
	return name[:i], name[i:]
}

// copyImage is the name of a copy of src in images: its own name, or `<stem>-2.<ext>`, … when that name holds
// a different file (an identical one is reused, never replaced).
func copyImage(src, images string, created *[]string) (string, error) {
	if !exists(images) {
		if err := doc.CreateDirAll(images); err != nil {
			return "", errors.New(doc.IOError(err))
		}
		*created = append(*created, images)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %s", src, doc.IOError(err))
	}
	stem, ext := stemExt(filepath.Base(src))
	for i := 1; ; i++ {
		name := stem + ext
		if i > 1 {
			name = stem + "-" + strconv.Itoa(i) + ext
		}
		dst := doc.JoinPath(images, name)
		if !exists(dst) {
			if err := os.WriteFile(dst, b, 0o666); err != nil {
				return "", fmt.Errorf("cannot copy %s: %s", src, doc.IOError(err))
			}
			*created = append(*created, dst)
			return name, nil
		}
		if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, b) {
			return name, nil
		}
	}
}

// relocateHistory rewrites a history stack (its last step applies to the document old) as if f had been
// applied to every state, where new is f(old): it replays the steps back from old, applies f to each state they
// reach, and diffs the rewritten neighbours. A step is a patch, not a document, so f cannot be applied to it.
func relocateHistory(stack []history.Patch, old, new jsonv.Value, f func(jsonv.Value)) []history.Patch {
	cur, next := jsonv.Clone(old), jsonv.Clone(new)
	out := make([]history.Patch, len(stack))
	for i := len(stack) - 1; i >= 0; i-- {
		history.Apply(stack[i], &cur)
		prev := jsonv.Clone(cur)
		f(prev)
		out[i] = orNoop(history.Diff(next, prev))
		next = prev
	}
	return out
}

// Open opens the document at path: migrated, ids repaired, unresolvable pending images and placeholders
// dropped, its libraries loaded, the history reset and the file remembered among the recents.
func (e *Engine) Open(path string) error {
	d, err := doc.Load(path)
	if err != nil {
		return err
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	dropPending(d.Root, e.Inflight)
	var pens []string
	if e.Doc.Path != "" && doc.SamePath(e.Doc.Path, path) {
		pens = slices.Clone(e.Agent.Pens)
	}
	dropPlaceholder(d.Root, pens)
	e.setDoc(d)
	e.loadLibs()
	e.resetHistory()
	e.rememberRecent(path)
	return nil
}

// LoadTemplate opens a fresh document: "slides", "mobile", "empty" or (anything else) "blank".
func (e *Engine) LoadTemplate(name string) {
	var text []byte
	switch name {
	case "slides", "mobile":
		text, _ = templates.ReadFile("templates/" + name + ".json")
	case "empty":
		text = []byte(`{"version":"2.19","children":[]}`)
	default:
		text, _ = templates.ReadFile("templates/blank.json")
	}
	d, err := doc.FromJSON(text)
	if err != nil {
		d = &doc.Doc{Root: jsonv.NewObject()}
	}
	e.setDoc(d)
	e.Libs = nil
	e.resetHistory()
}

// loadLibs loads `imports` (alias → relative .pen path) so `alias:id` refs resolve. One level deep.
func (e *Engine) loadLibs() {
	e.Libs = nil
	imports := jsonv.Obj(jsonv.Get(e.Doc.Root, "imports"))
	if imports == nil {
		return
	}
	base, _ := docDir(e.Doc)
	imports.Each(func(alias string, uri jsonv.Value) {
		rel, ok := jsonv.Str(uri)
		if !ok {
			return
		}
		lib, err := doc.Load(doc.JoinPath(base, strings.ReplaceAll(rel, "%20", " ")))
		if err != nil {
			return
		}
		resolve.Migrate(lib.Root)
		lib.EnsureIDs()
		dropPending(lib.Root, e.Inflight)
		dropPlaceholder(lib.Root, nil)
		e.Libs = append(e.Libs, resolve.Lib{Alias: alias, Root: lib.Root})
	})
}

// Save writes the document to path, or to its own file when path is "", and returns where it went. Save as into
// another folder rewrites relative images, scripts and imports to reach the same files from there (in the undo
// history too); images generated while Untitled are copied next to the file.
func (e *Engine) Save(path string) (string, error) {
	target := path
	if target == "" {
		if e.Doc.Path == "" {
			return "", errors.New("document has no path yet; pass a path to save")
		}
		target = e.Doc.Path
	}
	e.Doc.Root.Set("version", doc.FormatVersion)
	to, _ := doc.ParentPath(target)
	from, saved := docDir(e.Doc)
	moved := saved && !doc.SameDir(from, to)
	var backup *jsonv.Object
	if moved {
		backup = cloneRoot(e.Doc.Root)
		doc.Relocate(e.Doc.Root, from, to)
	}
	// Images generated while Untitled live in the data folder; the file refers to them next to itself.
	file := &doc.Doc{Root: cloneRoot(e.Doc.Root)}
	var created []string
	err := localizeImages(file.Root, doc.JoinPath(e.DataDir, "images"), to, &created)
	if err == nil {
		err = file.Save(target)
	}
	if err != nil {
		for i := len(created) - 1; i >= 0; i-- {
			os.Remove(created[i])
		}
		if backup != nil {
			e.Doc.Root = backup
		}
		return "", err
	}
	e.Doc.Root = file.Root
	if moved {
		relocate := func(v jsonv.Value) { doc.Relocate(v, from, to) }
		// During a drag the history steps lead back from the document as the drag began.
		var old, new jsonv.Value = backup, cloneRoot(e.Doc.Root)
		if start := e.DragBefore; start != nil {
			moved := cloneRoot(start)
			relocate(moved)
			e.DragBefore = cloneRoot(moved)
			old, new = start, moved
		}
		e.undo = relocateHistory(e.undo, old, new, relocate)
		e.redo = relocateHistory(e.redo, old, new, relocate)
	}
	e.Doc.Path = target
	e.markSaved()
	// Phase 8d: a live preview is rebuilt here from the new folder.
	e.rememberRecent(target)
	return target, nil
}

// rememberRecent puts path first among the recent files (at most 20) and saves the settings; a recents list that
// did not save does not fail what remembered it.
func (e *Engine) rememberRecent(path string) {
	var recent []jsonv.Value
	for _, r := range jsonv.Items(jsonv.Get(e.Settings, "recent")) {
		if s, ok := jsonv.Str(r); !ok || s != path {
			recent = append(recent, jsonv.Clone(r))
		}
	}
	recent = slices.Insert(recent, 0, jsonv.Value(path))
	recent = recent[:min(len(recent), 20)]
	e.Settings.Set("recent", jsonv.NewArray(recent...))
	e.SaveSettings()
}
