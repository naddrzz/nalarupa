package main

import (
	"encoding/json"
	"image"
	"log"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/editor"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

func flatToBox(f *render.Flat) Box {
	var parent *string
	if f.HasParent {
		p := f.Parent
		parent = &p
	}
	var m *Matrix6
	if f.M != nil {
		mv := Matrix6(*f.M)
		m = &mv
	}
	return Box{
		ID:          f.ID,
		Name:        f.Name,
		Type:        f.Type,
		X:           f.X,
		Y:           f.Y,
		W:           f.W,
		H:           f.H,
		Parent:      parent,
		Depth:       f.Depth,
		M:           m,
		Placeholder: f.Placeholder,
		Pending:     f.Pending,
	}
}

func coreStateLocked(e *engine.Engine) CoreState {
	var pathPtr *string
	if e.Doc != nil && e.Doc.Path != "" {
		p := e.Doc.Path
		pathPtr = &p
	}
	sel := make([]string, len(e.Selection))
	copy(sel, e.Selection)
	return CoreState{
		Version:      e.Version,
		Title:        e.Title(),
		Path:         pathPtr,
		Dirty:        e.Dirty(),
		Selection:    sel,
		AgentRunning: e.Agent.Running,
		Executing:    e.Executing,
		PenDrawing:   len(e.Agent.Pens) > 0,
		MCPPort:      int(e.MCPPort),
		Animating:    e.IsAnimating(),
	}
}

// EffectiveContextWindowDirect returns the resolved context window limit (in tokens) for the active backend and model.
func (b *Bridge) EffectiveContextWindowDirect() int {
	if b == nil || b.eng == nil {
		return 0
	}
	var lim int
	if b.eng.TryLock() {
		lim = b.eng.EffectiveContextWindow()
		b.eng.Unlock()
	}
	return lim
}

// CoreStateDirect returns engine state without JSON serialization.
func (b *Bridge) CoreStateDirect() CoreState {
	if b == nil || b.eng == nil {
		return CoreState{Selection: []string{}}
	}
	var st CoreState
	b.eng.Locked(func() {
		st = coreStateLocked(b.eng)
	})
	return st
}

// PollFast drains engine events and snapshots state without JSON overhead when no events queued.
func (b *Bridge) PollFast() PollResult {
	if b == nil || b.eng == nil {
		return PollResult{State: CoreState{Selection: []string{}}, Events: []map[string]any{}}
	}
	var st CoreState
	var rawEvents []jsonv.Value
	b.eng.Locked(func() {
		b.eng.ExpireSlides()
		st = coreStateLocked(b.eng)
		evs := b.eng.TakeEvents()
		if len(evs) > 0 {
			rawEvents = append([]jsonv.Value(nil), evs...)
		}
	})
	events := []map[string]any{}
	if len(rawEvents) > 0 {
		enc := jsonv.Marshal(jsonv.NewArray(rawEvents...))
		if err := json.Unmarshal(enc, &events); err != nil {
			log.Printf("[core] decode poll events: %v", err)
			events = []map[string]any{}
		}
	}
	return PollResult{State: st, Events: events}
}

// TryPollFast drains engine events only if the engine lock is immediately uncontended.
func (b *Bridge) TryPollFast() (PollResult, bool) {
	if b == nil || b.eng == nil {
		return PollResult{State: CoreState{Selection: []string{}}, Events: []map[string]any{}}, true
	}
	if !b.eng.TryLock() {
		return PollResult{}, false
	}
	b.eng.ExpireSlides()
	st := coreStateLocked(b.eng)
	var rawEvents []jsonv.Value
	if evs := b.eng.TakeEvents(); len(evs) > 0 {
		rawEvents = append([]jsonv.Value(nil), evs...)
	}
	b.eng.Unlock()

	events := []map[string]any{}
	if len(rawEvents) > 0 {
		enc := jsonv.Marshal(jsonv.NewArray(rawEvents...))
		if err := json.Unmarshal(enc, &events); err != nil {
			log.Printf("[core] decode poll events: %v", err)
			events = []map[string]any{}
		}
	}
	return PollResult{State: st, Events: events}, true
}

func layersRowsLocked(e *engine.Engine) []LayerRow {
	if e == nil || e.Doc == nil {
		return []LayerRow{}
	}
	out := make([]LayerRow, 0, 32)
	var walk func(nodes []jsonv.Value, depth int, parent string, flow bool)
	walk = func(nodes []jsonv.Value, depth int, parent string, flow bool) {
		for k := range nodes {
			index := k
			if !flow {
				index = len(nodes) - 1 - k
			}
			n := nodes[index]
			id := jsonv.StrOr(jsonv.Get(n, "id"))
			tyVal := jsonv.Get(n, "type")
			tyStr := jsonv.StrOr(tyVal)
			layout := tyStr == "frame" && !jsonv.StrIs(jsonv.Get(n, "layout"), "none")
			name, ok := jsonv.Str(jsonv.Get(n, "name"))
			if !ok || strings.TrimSpace(name) == "" {
				name, ok = jsonv.Str(jsonv.Get(n, "content"))
				if ok && tyStr == "text" {
					if line, _, cut := strings.Cut(name, "\n"); cut {
						name = strings.TrimSuffix(line, "\r")
					}
				} else if tyStr != "" {
					name = tyStr
				} else {
					name = "node"
				}
			}
			kids := jsonv.Arr(jsonv.Get(n, "children"))
			count := 0
			if kids != nil {
				count = len(kids.Items)
			}
			out = append(out, LayerRow{
				ID:       id,
				Name:     name,
				Type:     tyStr,
				Depth:    depth,
				Parent:   parent,
				Index:    index,
				Layout:   layout,
				Reusable: jsonv.IsTrue(jsonv.Get(n, "reusable")),
				Hidden:   jsonv.IsFalse(jsonv.Get(n, "enabled")),
				Children: count,
			})
			if kids != nil {
				walk(kids.Items, depth+1, id, layout)
			}
		}
	}
	walk(e.Doc.Children(), 0, doc.Root, false)
	return out
}

// LayersDirect returns flattened layer rows without JSON serialization.
func (b *Bridge) LayersDirect() []LayerRow {
	if b == nil || b.eng == nil {
		return []LayerRow{}
	}
	var out []LayerRow
	b.eng.Locked(func() {
		out = layersRowsLocked(b.eng)
	})
	return out
}

// TryLayersDirect returns flattened layer rows without blocking when the engine lock is busy.
func (b *Bridge) TryLayersDirect() ([]LayerRow, bool) {
	if b == nil || b.eng == nil {
		return []LayerRow{}, true
	}
	if !b.eng.TryLock() {
		return nil, false
	}
	out := layersRowsLocked(b.eng)
	b.eng.Unlock()
	return out, true
}

// FramesDirect returns all laid-out document order boxes without JSON serialization.
func (b *Bridge) FramesDirect() []Box {
	if b == nil || b.eng == nil {
		return []Box{}
	}
	var out []Box
	b.eng.Locked(func() {
		b.eng.Sync()
		out = make([]Box, 0, len(b.eng.Renderer.Order))
		for _, id := range b.eng.Renderer.Order {
			if f := b.eng.Renderer.Find(id); f != nil {
				out = append(out, flatToBox(f))
			}
		}
	})
	return out
}

// TryFramesDirect returns laid-out top-level order boxes without blocking or running SVG Sync on the UI thread.
func (b *Bridge) TryFramesDirect() ([]Box, bool) {
	if b == nil || b.eng == nil {
		return []Box{}, true
	}
	if !b.eng.TryLock() {
		return nil, false
	}
	if b.eng.NeedsSync() {
		b.eng.Unlock()
		return nil, false
	}
	out := make([]Box, 0, len(b.eng.Renderer.Order))
	for _, id := range b.eng.Renderer.Order {
		if f := b.eng.Renderer.Find(id); f != nil {
			out = append(out, flatToBox(f))
		}
	}
	b.eng.Unlock()
	return out, true
}

// BoxesDirect returns laid-out boxes for the requested node IDs without JSON serialization.
func (b *Bridge) BoxesDirect(ids []string) []Box {
	if b == nil || b.eng == nil || len(ids) == 0 {
		return []Box{}
	}
	var out []Box
	b.eng.Locked(func() {
		b.eng.Sync()
		out = make([]Box, 0, len(ids))
		for _, id := range ids {
			if f := b.eng.Renderer.Find(id); f != nil {
				out = append(out, flatToBox(f))
			}
		}
	})
	return out
}

// TryBoxesDirect returns laid-out boxes for ids without blocking or running SVG Sync on the UI thread.
func (b *Bridge) TryBoxesDirect(ids []string) ([]Box, bool) {
	if b == nil || b.eng == nil || len(ids) == 0 {
		return []Box{}, true
	}
	if !b.eng.TryLock() {
		return nil, false
	}
	if !b.eng.NeedsSync() {
		out := make([]Box, 0, len(ids))
		for _, id := range ids {
			if f := b.eng.Renderer.Find(id); f != nil {
				out = append(out, flatToBox(f))
			}
		}
		b.eng.Unlock()
		return out, true
	}
	out := rnodeBoxesLocked(b.eng, ids)
	b.eng.Unlock()
	return out, true
}

// SlideReleaseSeqDirect returns the monotonic counter of cursor-triggered slide releases atomically.
func (b *Bridge) SlideReleaseSeqDirect() uint64 {
	if b == nil || b.eng == nil {
		return 0
	}
	return b.eng.SlideReleaseSeqFast()
}

// SubtreeBoxesDirect returns laid-out boxes in paint order for ids and their descendants (up to maxNodes),
// allowing monolithic container inserts to animate child-by-child under the agent cursor.
func (b *Bridge) SubtreeBoxesDirect(ids []string, maxNodes int) []Box {
	if b == nil || b.eng == nil || len(ids) == 0 || maxNodes <= 0 {
		return []Box{}
	}
	var out []Box
	b.eng.Locked(func() {
		b.eng.Sync()
		out = subtreeBoxesLocked(b.eng, ids, maxNodes)
	})
	return out
}

// TrySubtreeBoxesDirect returns subtree boxes without blocking or running SVG Sync on the UI thread:
// when cached layers are behind Version during fast streaming, it runs pure Go flexbox layout (<0.15ms, zero SVG)
// so cursor choreography never defers or starves while statements stream in.
func (b *Bridge) TrySubtreeBoxesDirect(ids []string, maxNodes int) ([]Box, bool) {
	if b == nil || b.eng == nil || len(ids) == 0 || maxNodes <= 0 {
		return []Box{}, true
	}
	if !b.eng.TryLock() {
		return nil, false
	}
	var out []Box
	if !b.eng.NeedsSync() {
		out = subtreeBoxesLocked(b.eng, ids, maxNodes)
	} else {
		out = rnodeSubtreeBoxesLocked(b.eng, ids, maxNodes)
	}
	b.eng.Unlock()
	return out, true
}

func rnodeToBox(rn *resolve.RNode, parentID string, depth int) Box {
	var pPtr *string
	if parentID != "" && parentID != doc.Root {
		cp := parentID
		pPtr = &cp
	}
	name, _ := rn.S("name")
	return Box{
		ID:          rn.ID,
		Name:        name,
		Type:        rn.Type,
		X:           rn.X,
		Y:           rn.Y,
		W:           rn.W,
		H:           rn.H,
		Parent:      pPtr,
		Depth:       depth,
		Placeholder: rn.B("placeholder"),
	}
}

func rnodeBoxesLocked(e *engine.Engine, ids []string) []Box {
	d := e.ShownDoc()
	if d == nil {
		return nil
	}
	cx := &render.Ctx{Doc: d, Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets}
	laid := map[int]*resolve.RNode{}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
		base := id
		if idx := strings.IndexByte(base, '/'); idx > 0 {
			base = base[:idx]
		}
		if top := d.TopIndex(base); top >= 0 {
			if _, ok := laid[top]; !ok {
				laid[top] = cx.LaidOutLive(top)
			}
		}
	}
	var out []Box
	var walk func(rn *resolve.RNode, parent string, depth int)
	walk = func(rn *resolve.RNode, parent string, depth int) {
		if rn == nil {
			return
		}
		if want[rn.ID] {
			out = append(out, rnodeToBox(rn, parent, depth))
		}
		for _, k := range rn.Kids {
			walk(k, rn.ID, depth+1)
		}
	}
	for i := range d.Children() {
		if root, ok := laid[i]; ok {
			walk(root, doc.Root, 0)
		}
	}
	return out
}

func rnodeSubtreeBoxesLocked(e *engine.Engine, ids []string, maxNodes int) []Box {
	d := e.ShownDoc()
	if d == nil {
		return nil
	}
	activeSlides := e.ActiveSlideIDs()
	multiBatch := len(ids) >= 3
	roots := make(map[string]bool, len(ids))
	topNeeded := map[int]bool{}
	for _, id := range ids {
		mapped := mapToCohesiveRoot(d, activeSlides, id, multiBatch)
		roots[mapped] = true
		base := mapped
		if idx := strings.IndexByte(base, '/'); idx > 0 {
			base = base[:idx]
		}
		if top := d.TopIndex(base); top >= 0 {
			topNeeded[top] = true
		}
	}
	cx := &render.Ctx{Doc: d, Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets}
	seenOut := make(map[string]bool, maxNodes)
	out := make([]Box, 0, min(maxNodes, len(ids)*2))
	var walk func(rn *resolve.RNode, parent string, depth int, inside bool)
	walk = func(rn *resolve.RNode, parent string, depth int, inside bool) {
		if rn == nil || len(out) >= maxNodes {
			return
		}
		inSub := inside || roots[rn.ID]
		if inSub {
			isTop := parent == "" || parent == doc.Root
			isCohesive := activeSlides[rn.ID]
			if !isCohesive && !isTop && (rn.Type == "frame" || rn.Type == "group") {
				if n := d.Find(rn.ID); isCohesiveSurfaceLeaf(n) || (nodePaintsOwnSurface(n) && len(rn.Kids) == 0) {
					isCohesive = true
				}
			}
			if !seenOut[rn.ID] {
				seenOut[rn.ID] = true
				out = append(out, rnodeToBox(rn, parent, depth))
			}
			if isCohesive || len(out) >= maxNodes {
				return
			}
		}
		for _, k := range rn.Kids {
			walk(k, rn.ID, depth+1, inSub)
			if len(out) >= maxNodes {
				return
			}
		}
	}
	for i := range d.Children() {
		if topNeeded[i] {
			walk(cx.LaidOutLive(i), doc.Root, 0, false)
		}
	}
	return out
}

func nodePaintsOwnSurface(n jsonv.Value) bool {
	if n == nil {
		return false
	}
	return jsonv.Get(n, "fill") != nil || jsonv.Get(n, "stroke") != nil || jsonv.Get(n, "effect") != nil
}

func countChildSurfaceCards(items []jsonv.Value, depth int) int {
	if depth > 2 {
		return 0
	}
	count := 0
	for _, ch := range items {
		if !jsonv.StrIs(jsonv.Get(ch, "type"), "frame") && !jsonv.StrIs(jsonv.Get(ch, "type"), "group") {
			continue
		}
		sub := jsonv.Items(jsonv.Get(ch, "children"))
		if nodePaintsOwnSurface(ch) && len(sub) >= 2 {
			count++
		} else if !nodePaintsOwnSurface(ch) && len(sub) > 0 {
			count += countChildSurfaceCards(sub, depth+1)
		}
		if count >= 2 {
			return count
		}
	}
	return count
}

func isCohesiveSurfaceLeaf(n jsonv.Value) bool {
	if n == nil {
		return false
	}
	if !jsonv.StrIs(jsonv.Get(n, "type"), "frame") && !jsonv.StrIs(jsonv.Get(n, "type"), "group") {
		return false
	}
	if !nodePaintsOwnSurface(n) {
		return false
	}
	items := jsonv.Items(jsonv.Get(n, "children"))
	if len(items) < 2 {
		return false
	}
	return countChildSurfaceCards(items, 0) < 2
}

func mapToCohesiveRoot(d *doc.Doc, activeSlides map[string]bool, id string, multiBatch bool) string {
	if d == nil || id == "" {
		return id
	}
	if len(activeSlides) > 0 {
		cur := id
		for range 12 {
			p, _, ok := d.ParentOf(cur)
			if !ok || p == "" || p == doc.Root {
				break
			}
			if activeSlides[p] {
				return p
			}
			cur = p
		}
		if activeSlides[id] && !multiBatch {
			return id
		}
	}
	if multiBatch {
		cur := id
		for range 12 {
			p, _, ok := d.ParentOf(cur)
			if !ok || p == "" || p == doc.Root {
				break
			}
			if pp, _, okP := d.ParentOf(p); okP && pp != "" && pp != doc.Root {
				if isCohesiveSurfaceLeaf(d.Find(p)) {
					return p
				}
			}
			cur = p
		}
	}
	return id
}

func subtreeBoxesLocked(e *engine.Engine, ids []string, maxNodes int) []Box {
	d := e.ShownDoc()
	activeSlides := e.ActiveSlideIDs()
	multiBatch := len(ids) >= 3

	roots := make(map[string]bool, len(ids))
	for _, id := range ids {
		roots[mapToCohesiveRoot(d, activeSlides, id, multiBatch)] = true
	}

	inSubtree := make(map[string]bool, maxNodes)
	seenOut := make(map[string]bool, maxNodes)
	out := make([]Box, 0, min(maxNodes, len(ids)*4))
	for _, id := range e.Renderer.Order {
		layer, ok := e.Renderer.Layers[id]
		if !ok {
			continue
		}
		skipBelowDepth := -1
		for i := range layer.Flat {
			f := &layer.Flat[i]
			if skipBelowDepth >= 0 {
				if f.Depth > skipBelowDepth {
					continue
				}
				skipBelowDepth = -1
			}
			if roots[f.ID] || (f.HasParent && inSubtree[f.Parent]) {
				inSubtree[f.ID] = true
				isTopLevel := !f.HasParent || f.Parent == "" || f.Parent == doc.Root
				isCohesiveUnit := activeSlides[f.ID]
				if !isCohesiveUnit && !isTopLevel && d != nil && (f.Type == "frame" || f.Type == "group") {
					if n := d.Find(f.ID); isCohesiveSurfaceLeaf(n) || (nodePaintsOwnSurface(n) && len(jsonv.Items(jsonv.Get(n, "children"))) == 0) {
						isCohesiveUnit = true
					}
				}
				if !seenOut[f.ID] {
					seenOut[f.ID] = true
					out = append(out, flatToBox(f))
				}
				if isCohesiveUnit {
					skipBelowDepth = f.Depth
				}
				if len(out) >= maxNodes {
					return out
				}
			}
		}
	}
	return out
}

// ChildrenBoxesDirect returns laid-out boxes of direct children of parentID without JSON serialization.
func (b *Bridge) ChildrenBoxesDirect(parentID string) []Box {
	if b == nil || b.eng == nil {
		return []Box{}
	}
	var out []Box
	b.eng.Locked(func() {
		b.eng.Sync()
		out = []Box{}
		for _, f := range b.eng.Renderer.Flats() {
			if f.HasParent && f.Parent == parentID {
				out = append(out, flatToBox(f))
			}
		}
	})
	return out
}

func liveNodesLocked(e *engine.Engine) []LiveNode {
	live := e.Renderer.Live()
	out := make([]LiveNode, 0, len(live))
	for _, l := range live {
		var clip *RectD
		if l.HasClip {
			clip = &RectD{X: l.Clip.X, Y: l.Clip.Y, W: l.Clip.W, H: l.Clip.H}
		}
		cuts := make([]LiveCut, len(l.Cuts))
		for i, c := range l.Cuts {
			cuts[i] = LiveCut{X: c.Box.X, Y: c.Box.Y, W: c.Box.W, H: c.Box.H, R: c.Radius}
		}
		out = append(out, LiveNode{
			Box:    flatToBox(l.Flat),
			Radius: l.Flat.Radius,
			Clip:   clip,
			Cuts:   cuts,
		})
	}
	return out
}

// LiveDirect returns active animated placeholder/pending nodes without JSON serialization.
func (b *Bridge) LiveDirect() []LiveNode {
	if b == nil || b.eng == nil {
		return []LiveNode{}
	}
	var out []LiveNode
	b.eng.Locked(func() {
		b.eng.Sync()
		out = liveNodesLocked(b.eng)
	})
	return out
}

// TryLiveDirect returns active animated placeholder/pending nodes without blocking or running SVG Sync on the UI thread.
func (b *Bridge) TryLiveDirect() ([]LiveNode, bool) {
	if b == nil || b.eng == nil {
		return []LiveNode{}, true
	}
	if !b.eng.TryLock() {
		return nil, false
	}
	if b.eng.NeedsSync() {
		b.eng.Unlock()
		return nil, false
	}
	out := liveNodesLocked(b.eng)
	b.eng.Unlock()
	return out, true
}

// HitDirect returns the topmost-to-root hit chain at document coordinates (x, y) without JSON serialization.
func (b *Bridge) HitDirect(x, y float64) []string {
	if b == nil || b.eng == nil {
		return []string{}
	}
	var chain []string
	b.eng.Locked(func() {
		b.eng.Sync()
		chain = render.HitChain(&b.eng.Renderer, x, y)
	})
	if chain == nil {
		return []string{}
	}
	return chain
}

// NodeCornerRadiiDirect returns resolved corner radii [tl, tr, br, bl] without cloning document subtrees or variables.
func (b *Bridge) NodeCornerRadiiDirect(id string) ([4]float64, bool) {
	if b == nil || b.eng == nil || id == "" {
		return [4]float64{}, true
	}
	var radii [4]float64
	b.eng.Locked(func() {
		b.eng.Sync()
		if f := b.eng.Renderer.Find(id); f != nil {
			radii = f.Radius
		}
	})
	allEq := radii[0] == radii[1] && radii[1] == radii[2] && radii[2] == radii[3]
	return radii, allEq
}

// IsFreeformArtboardDirect checks if id is a non-autolayout frame that contains children without cloning DOM trees.
func (b *Bridge) IsFreeformArtboardDirect(id string) bool {
	if b == nil || b.eng == nil || id == "" {
		return false
	}
	res := false
	b.eng.Locked(func() {
		b.eng.Sync()
		f := b.eng.Renderer.Find(id)
		if f == nil || f.Type != "frame" {
			return
		}
		if b.eng.Doc != nil {
			if raw := b.eng.Doc.Find(id); raw != nil {
				if layout, ok := jsonv.Str(jsonv.Get(raw, "layout")); ok && layout != "" && layout != "none" {
					return
				}
			}
		}
		if layer, ok := b.eng.Renderer.Layers[id]; ok {
			res = len(layer.Flat) > 1
			return
		}
		for _, child := range b.eng.Renderer.Flats() {
			if child.HasParent && child.Parent == id {
				res = true
				return
			}
		}
	})
	return res
}

// NodeTypeDirect returns the laid-out node type of id (or "" if missing) without JSON serialization.
func (b *Bridge) NodeTypeDirect(id string) string {
	if b == nil || b.eng == nil || id == "" {
		return ""
	}
	ty := ""
	b.eng.Locked(func() {
		b.eng.Sync()
		if f := b.eng.Renderer.Find(id); f != nil {
			ty = f.Type
		}
	})
	return ty
}

// HasSlidesDirect reports whether node slide-in animations are currently active without blocking the UI thread.
func (b *Bridge) HasSlidesDirect() bool {
	if b == nil || b.eng == nil {
		return false
	}
	if b.eng.TryLock() {
		res := b.eng.HasSlides()
		b.eng.Unlock()
		return res
	}
	return b.eng.HasSlidesFast()
}

// VersionDirect returns the latest committed engine document/preview version atomically without acquiring the engine lock.
func (b *Bridge) VersionDirect() uint64 {
	if b == nil || b.eng == nil {
		return 0
	}
	return b.eng.VersionFast()
}

// QueueCursorUpdateDirect enqueues cursor-arrived slide releases and queued hold extensions without acquiring Engine.Lock.
func (b *Bridge) QueueCursorUpdateDirect(releaseIDs, holdIDs []string) {
	if b == nil || b.eng == nil {
		return
	}
	b.eng.QueueCursorUpdate(releaseIDs, holdIDs)
}

// RenderDetachedInto snapshots synced scene state briefly under Engine.Lock, then executes pixel rasterization
// and slide compositing completely off-lock using det so UI/poll/agent threads never block on CPU rasterization.
func (b *Bridge) RenderDetachedInto(det *render.DetachedRenderer, dst **image.RGBA, w, h int, camX, camY, zoom float64, bg [3]uint8, solo string, sharp bool) *image.RGBA {
	if b == nil || b.eng == nil || det == nil {
		return nil
	}
	cw := max(1, min(8192, w))
	ch := max(1, min(8192, h))
	if *dst == nil || (*dst).Bounds().Dx() != cw || (*dst).Bounds().Dy() != ch {
		*dst = image.NewRGBA(image.Rect(0, 0, cw, ch))
	}
	snap := b.eng.SnapshotForRender(solo)
	px := det.Render(snap, uint32(cw), uint32(ch), camX, camY, zoom, bg, solo, sharp)
	copy((*dst).Pix, px)
	return *dst
}

// RenderDetachedWithOverview renders the viewport (with optional whole-document overview when wantOverview is true)
// from a single SceneSnapshot off-lock so both layers stay consistent with minimal lock acquisition.
func (b *Bridge) RenderDetachedWithOverview(
	det *render.DetachedRenderer,
	dst **image.RGBA,
	w, h int,
	camX, camY, zoom float64,
	bg [3]uint8,
	solo string,
	sharp bool,
	wantOverview bool,
	ovDst **image.RGBA,
	ovMaxSide int,
) (ovRect RectD, ovVer uint64, ovOK bool) {
	var snap render.SceneSnapshot
	hasSnap := false
	_, ovRect, ovVer, ovOK = b.RenderDetachedCachedWithOverview(
		det, &snap, &hasSnap, false, dst, w, h, camX, camY, zoom, bg, solo, sharp, wantOverview, ovDst, ovMaxSide,
	)
	return ovRect, ovVer, ovOK
}

// RenderDetachedCachedWithOverview renders at (camX, camY, zoom) while reusing cachedSnap completely lock-free
// on pure camera zoom/pan (or when Engine.Lock is temporarily held by a running agent script), eliminating zoom blur delays.
func (b *Bridge) RenderDetachedCachedWithOverview(
	det *render.DetachedRenderer,
	cachedSnap *render.SceneSnapshot,
	hasCached *bool,
	preferLockFree bool,
	dst **image.RGBA,
	w, h int,
	camX, camY, zoom float64,
	bg [3]uint8,
	solo string,
	sharp bool,
	wantOverview bool,
	ovDst **image.RGBA,
	ovMaxSide int,
) (renderedVer uint64, ovRect RectD, ovVer uint64, ovOK bool) {
	if b == nil || b.eng == nil || det == nil {
		return 0, RectD{}, 0, false
	}
	cw := max(1, min(8192, w))
	ch := max(1, min(8192, h))
	if *dst == nil || (*dst).Bounds().Dx() != cw || (*dst).Bounds().Dy() != ch {
		*dst = image.NewRGBA(image.Rect(0, 0, cw, ch))
	}

	var snap render.SceneSnapshot
	curVer := b.VersionDirect()
	hasSlides := b.HasSlidesDirect()
	switch {
	case cachedSnap != nil && hasCached != nil && *hasCached && !hasSlides && len(cachedSnap.Slides) == 0 && cachedSnap.Version == curVer:
		snap = *cachedSnap
	case cachedSnap != nil && hasCached != nil && *hasCached && preferLockFree:
		if got, ok := b.eng.TrySnapshotForRender(solo); ok {
			snap = got
			*cachedSnap = got
		} else {
			snap = *cachedSnap
		}
	default:
		snap = b.eng.SnapshotForRender(solo)
		if cachedSnap != nil && hasCached != nil {
			*cachedSnap = snap
			*hasCached = true
		}
	}

	px := det.Render(snap, uint32(cw), uint32(ch), camX, camY, zoom, bg, solo, sharp)
	copy((*dst).Pix, px)

	if wantOverview && ovDst != nil && len(snap.Slides) == 0 && snap.Version == b.VersionDirect() {
		if ovPx, ow, oh, dr, ok := det.RenderOverview(snap, uint32(max(256, ovMaxSide)), bg, solo); ok {
			iw, ih := int(ow), int(oh)
			if *ovDst == nil || (*ovDst).Bounds().Dx() != iw || (*ovDst).Bounds().Dy() != ih {
				*ovDst = image.NewRGBA(image.Rect(0, 0, iw, ih))
			}
			copy((*ovDst).Pix, ovPx)
			return snap.Version, RectD{X: dr.X, Y: dr.Y, W: dr.W, H: dr.H}, snap.Version, true
		}
	}
	return snap.Version, RectD{}, snap.Version, false
}

// TryRenderInto renders a frame only if the engine lock is immediately uncontended; returns (nil, false)
// without blocking when a background agent or worker currently holds the lock.
func (b *Bridge) TryRenderInto(dst **image.RGBA, w, h int, camX, camY, zoom float64, bg [3]uint8, solo string, sharp bool) (*image.RGBA, bool) {
	if b == nil || b.eng == nil {
		return nil, false
	}
	if !b.eng.TryLock() {
		return nil, false
	}
	cw := max(1, min(8192, w))
	ch := max(1, min(8192, h))
	if *dst == nil || (*dst).Bounds().Dx() != cw || (*dst).Bounds().Dy() != ch {
		*dst = image.NewRGBA(image.Rect(0, 0, cw, ch))
	}
	px := b.eng.Render(uint32(cw), uint32(ch), camX, camY, zoom, bg, solo, sharp)
	copy((*dst).Pix, px)
	b.eng.Unlock()
	return *dst, true
}

// ViewportDirect updates the engine's active viewport without JSON serialization.
// When nonBlocking is true (during interactive pan/zoom animations), it uses TryLock so a busy agent never stalls UI frames.
func (b *Bridge) ViewportDirect(x, y, w, h float64, nonBlocking bool) {
	if b == nil || b.eng == nil {
		return
	}
	if nonBlocking {
		if !b.eng.TryLock() {
			return
		}
		b.eng.Viewport = &[4]float64{x, y, w, h}
		b.eng.Unlock()
		return
	}
	b.eng.Locked(func() {
		b.eng.Viewport = &[4]float64{x, y, w, h}
	})
}

// SelectDirect updates engine selection without JSON serialization.
func (b *Bridge) SelectDirect(ids []string) CoreState {
	if b == nil || b.eng == nil {
		return CoreState{Selection: []string{}}
	}
	cp := make([]string, len(ids))
	copy(cp, ids)
	var st CoreState
	b.eng.Locked(func() {
		b.eng.Selection = cp
		st = coreStateLocked(b.eng)
	})
	return st
}

// NodesInRectDirect finds hit nodes inside document rect (x, y, w, h) without JSON serialization.
func (b *Bridge) NodesInRectDirect(x, y, w, h float64, deep bool) []string {
	if b == nil || b.eng == nil {
		return []string{}
	}
	var out []string
	b.eng.Locked(func() {
		b.eng.Sync()
		out = editor.NodesInRect(b.eng, editor.Rect{X: x, Y: y, W: w, H: h}, deep)
	})
	if out == nil {
		return []string{}
	}
	return out
}

// NodeParentDirect returns the parent node ID of id (or "" if root/missing) without cloning node properties.
func (b *Bridge) NodeParentDirect(id string) string {
	if b == nil || b.eng == nil || id == "" {
		return ""
	}
	parent := ""
	b.eng.Locked(func() {
		b.eng.Sync()
		if f := b.eng.Renderer.Find(id); f != nil && f.HasParent {
			parent = f.Parent
		}
	})
	return parent
}

// UpdateXYBatchDirect updates (x, y) coordinates of one or more nodes in a single atomic batch without JSON serialization.
func (b *Bridge) UpdateXYBatchDirect(ids []string, xs, ys []float64) (CoreState, bool) {
	if b == nil || b.eng == nil || len(ids) == 0 || len(ids) != len(xs) || len(ids) != len(ys) {
		return CoreState{}, false
	}
	defer b.eng.EditGate()()
	var st CoreState
	ok := false
	b.eng.Locked(func() {
		err := b.eng.EditOrDrag(func(d *doc.Doc) error {
			items := make([]doc.UpdateItem, len(ids))
			for i, id := range ids {
				props := jsonv.NewObject()
				props.Set("x", editor.Num(xs[i]))
				props.Set("y", editor.Num(ys[i]))
				items[i] = doc.UpdateItem{Path: id, Data: props}
			}
			return d.UpdateAll(items)
		})
		if err == nil {
			st = coreStateLocked(b.eng)
			ok = true
		}
	})
	return st, ok
}
