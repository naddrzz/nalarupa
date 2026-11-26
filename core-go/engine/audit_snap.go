package engine

import (
	"slices"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// AuditSnapshot is an immutable snapshot of the document root and laid-out screen boxes captured quickly under
// the engine lock (reusing already-synced Renderer.Layers without running usvg SVG parsing), so design auditing
// executes completely off-lock and stays bound to the exact execute call that produced it.
type AuditSnapshot struct {
	Root               *jsonv.Object
	FlatsByTopID       map[string][]render.Flat
	StyleVariationMode bool
}

func flattenRNodeForAudit(rn *resolve.RNode, depth int, parentClippedFaded bool, out *[]render.Flat) {
	if rn == nil {
		return
	}
	faded := parentClippedFaded
	if op, ok := rn.F("opacity"); ok && op <= 0 {
		faded = true
	}
	clip := rn.Type == "frame" && rn.B("clip")
	*out = append(*out, render.Flat{
		ID:    rn.ID,
		Name:  rn.Name(),
		Type:  rn.Type,
		X:     rn.X,
		Y:     rn.Y,
		W:     rn.W,
		H:     rn.H,
		Depth: depth,
		Clip:  clip,
		Faded: faded,
	})
	for _, k := range rn.Kids {
		flattenRNodeForAudit(k, depth+1, faded, out)
	}
}

// CaptureAuditSnapshot captures the lightweight state needed by AppendDesignAudit without invoking e.Sync() or usvg.
// Must be called while holding Engine.Lock.
func (e *Engine) CaptureAuditSnapshot() *AuditSnapshot {
	if e.Doc == nil || e.Doc.Root == nil {
		return nil
	}
	snap := &AuditSnapshot{
		Root:               cloneRoot(e.Doc.Root),
		FlatsByTopID:       map[string][]render.Flat{},
		StyleVariationMode: e.Agent.Mode == "side" || (e.Agent.Cook > 1 && e.Agent.Iterate == "style"),
	}
	synced := !e.NeedsSync()
	var resolver *resolve.Resolver
	for _, ch := range e.Doc.Children() {
		if !jsonv.StrIs(jsonv.Get(ch, "type"), "frame") || jsonv.IsTrue(jsonv.Get(ch, "reusable")) {
			continue
		}
		topID := jsonv.StrOr(jsonv.Get(ch, "id"))
		if topID == "" {
			continue
		}
		if synced {
			if l, ok := e.Renderer.Layers[topID]; ok && len(l.Flat) > 0 {
				snap.FlatsByTopID[topID] = slices.Clone(l.Flat)
				continue
			}
		}
		if resolver == nil {
			resolver = resolve.New(e.Doc.Root, e.Libs)
		}
		rn := resolver.Resolve(ch)
		if rn == nil {
			continue
		}
		layout.Layout(rn, e.Fonts)
		var flats []render.Flat
		flattenRNodeForAudit(rn, 0, false, &flats)
		if len(flats) > 0 {
			snap.FlatsByTopID[topID] = flats
		}
	}
	return snap
}
