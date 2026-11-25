package main

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	LayerRowH   = 28.0
	SlideThumbW = 196.0
)

type cachedThumb struct {
	version uint64
	bmp     *ui.Bitmap
	failed  bool
}

type layerDragState struct {
	active     bool
	ids        []string
	startY     float32
	curY       float32
	targetID   string
	targetPos  string // "before" | "inside" | "after"
}

// LayersState holds persistent state for the Left Panel (Layers tree & Slides list).
type LayersState struct {
	Tab        string // "layers" | "slides"
	Open       map[string]bool
	Revealed   map[string]bool
	List       ui.ListState
	SlidesList ui.ListState

	FocusID      string
	RenameID     string
	RenameDraft  string
	RenameFocus  bool
	RenameHandle ui.Handle

	LastClickID string
	LastClickAt time.Time

	Drag layerDragState

	Thumbs          map[string]cachedThumb
	CachedVer       uint64
	CachedAll       []LayerRow
	CachedByID      map[string]LayerRow
	CachedFramesVer uint64
	CachedFrames    []Box
}

func (a *NalarupaApp) initLayersState() {
	a.Layers = LayersState{
		Tab:        "layers",
		Open:       map[string]bool{},
		Revealed:   map[string]bool{},
		Thumbs:     map[string]cachedThumb{},
		CachedByID: map[string]LayerRow{},
	}
}

func (a *NalarupaApp) resetLayersForNewDoc() {
	for _, ct := range a.Layers.Thumbs {
		if ct.bmp != nil {
			ct.bmp.ReleasePixels()
		}
	}
	a.Layers.Tab = "layers"
	a.Layers.Open = map[string]bool{}
	a.Layers.Revealed = map[string]bool{}
	a.Layers.FocusID = ""
	a.Layers.RenameID = ""
	a.Layers.Drag = layerDragState{}
	a.Layers.Thumbs = map[string]cachedThumb{}
	a.Layers.CachedVer = 0
	a.Layers.CachedAll = nil
	a.Layers.CachedFramesVer = 0
	a.Layers.CachedFrames = nil
}

// LayerIndent computes Nalarupa's bounded indentation so 20+ deep layer hierarchies stay legible inside 248px.
func LayerIndent(depth int) int {
	if depth <= 0 {
		return 0
	}
	d1 := min(depth, 6) * 14
	d2 := max(depth-6, 0) * 3
	return min(d1+d2, 120)
}

func (a *NalarupaApp) ensureLayersFetched() []LayerRow {
	if a.Layers.CachedVer == a.State.Version && a.Layers.CachedAll != nil {
		return a.Layers.CachedAll
	}
	all := []LayerRow{}
	a.Bridge.TryCall("layers", nil, &all)
	if all == nil {
		all = []LayerRow{}
	}
	byID := make(map[string]LayerRow, len(all))
	for _, l := range all {
		byID[l.ID] = l
	}
	a.Layers.CachedVer = a.State.Version
	a.Layers.CachedAll = all
	a.Layers.CachedByID = byID
	return all
}

func (a *NalarupaApp) ensureFramesFetched() []Box {
	if a.Layers.CachedFramesVer == a.State.Version && a.Layers.CachedFrames != nil {
		return a.Layers.CachedFrames
	}
	all := []Box{}
	a.Bridge.TryCall("frames", nil, &all)
	if all == nil {
		all = []Box{}
	}
	a.Layers.CachedFramesVer = a.State.Version
	a.Layers.CachedFrames = all
	return all
}

func (ls *LayersState) visibleRows(all []LayerRow) []LayerRow {
	var vis []LayerRow
	closedDepth := -1
	for _, l := range all {
		if closedDepth >= 0 {
			if l.Depth > closedDepth {
				continue
			}
			closedDepth = -1
		}
		vis = append(vis, l)
		if l.Children > 0 && !ls.Open[l.ID] {
			closedDepth = l.Depth
		}
	}
	return vis
}

func (ls *LayersState) revealSelection(a *NalarupaApp, ids []string) {
	if len(ids) == 0 {
		ls.Revealed = map[string]bool{}
		return
	}
	all := a.ensureLayersFetched()
	for _, id := range ids {
		if ls.Revealed[id] {
			continue
		}
		cur, ok := ls.CachedByID[id]
		for ok && cur.Parent != "" && cur.Parent != "document" {
			ls.Open[cur.Parent] = true
			cur, ok = ls.CachedByID[cur.Parent]
		}
	}
	newRev := make(map[string]bool, len(ids))
	for _, id := range ids {
		newRev[id] = true
	}
	ls.Revealed = newRev

	vis := ls.visibleRows(all)
	for i, r := range vis {
		if slices.Contains(ids, r.ID) {
			ls.List.ScrollIntoView(i)
			break
		}
	}
}

func (a *NalarupaApp) viewLeftPanel(c *ui.Context) {
	panel := ui.Column(c.Key("left-panel")).
		Width(LeftW).
		FillHeight().
		Background(C.Panel).
		BorderWidth(0, 1, 0, 0).
		BorderColor(C.Border)

	panel.Children(func() {
		ui.Box(c.Key("left-tabs-wrap")).
			FillWidth().
			Padding(10).
			BorderWidth(0, 0, 1, 0).
			BorderColor(C.Border).
			Children(func() {
				next, changed := SegmentedTabs(c, "left-tabs", "Document structure", a.Layers.Tab, []SegOption[string]{
					{Key: "tab-layers", Value: "layers", Label: "Layers"},
					{Key: "tab-slides", Value: "slides", Label: "Slides"},
				})
				if changed {
					a.Layers.Tab = next
				}
			})

		if a.Layers.Tab == "slides" {
			a.viewSlidesTab(c)
		} else {
			a.viewLayersTab(c)
		}
	})
}

func (a *NalarupaApp) viewLayersTab(c *ui.Context) {
	all := a.ensureLayersFetched()
	if len(all) == 0 {
		ui.Box(c.Key("layers-empty")).Fill().Padding(16).Children(func() {
			ui.Text(c, "Empty canvas. Draw a frame (F) or ask the agent.").
				FontSize(12).
				TextColor(C.Ghost).
				Wrap()
		})
		return
	}

	vis := a.Layers.visibleRows(all)
	if a.Layers.FocusID == "" && len(vis) > 0 {
		if len(a.State.Selection) > 0 {
			a.Layers.FocusID = a.State.Selection[0]
		} else {
			a.Layers.FocusID = vis[0].ID
		}
	}

	a.HoverLayerID = ""

	treeWrap := ui.Column(c.Key("layers")).
		Fill().
		Padding(6, 0).
		Role(ui.RoleTree).
		Label("Layers")

	treeWrap.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown && a.Layers.RenameID == "" {
			if a.handleLayersTreeKey(ev, vis) {
				return true
			}
			return a.HandleEditorShortcut(ev.Mods, ev.Key)
		}
		return false
	})

	treeWrap.Children(func() {
		ui.List(c.Key("layers-list"), &a.Layers.List, len(vis)).
			Fill().
			ItemKey(func(i int) any {
				if i >= 0 && i < len(vis) {
					return vis[i].ID
				}
				return i
			}).
			Rows(func(r ui.ListRow) {
				idx := r.Index
				if idx < 0 || idx >= len(vis) {
					return
				}
				l := vis[idx]
				isSelected := slices.Contains(a.State.Selection, l.ID)
				isHovered := r.Hovered()
				if isHovered {
					a.HoverLayerID = l.ID
				}
				isDropInside := a.Layers.Drag.active && a.Layers.Drag.targetID == l.ID && a.Layers.Drag.targetPos == "inside"
				isDropBefore := a.Layers.Drag.active && a.Layers.Drag.targetID == l.ID && a.Layers.Drag.targetPos == "before"
				isDropAfter := a.Layers.Drag.active && a.Layers.Drag.targetID == l.ID && a.Layers.Drag.targetPos == "after"

				rowBg := ui.Color{}
				switch {
				case isDropInside:
					rowBg = C.SelectDrop
				case isSelected:
					rowBg = C.SelectBg
				case isHovered:
					rowBg = C.Panel2
				}

				rowOpacity := float32(1.0)
				if l.Hidden {
					if l.Reusable || l.Type == "ref" {
						if !isSelected {
							rowOpacity = 0.85
						}
					} else {
						rowOpacity = 0.6
					}
				}

				indentPx := float32(6 + LayerIndent(l.Depth))
				row := ui.Row(c.Key("layer-" + l.ID)).
					FillWidth().
					Height(LayerRowH).
					Padding(0, 6, 0, indentPx).
					Gap(4).
					AlignItems(ui.Center).
					Background(rowBg).
					Opacity(rowOpacity).
					Role(ui.RoleTreeItem).
					Selected(isSelected).
					Label(l.Name).
					Cursor(ui.CursorPointer)
				if l.Children > 0 {
					row.Expanded(a.Layers.Open[l.ID])
				}

				// Draw before/after reorder indicator line
				if isDropBefore || isDropAfter {
					pos := a.Layers.Drag.targetPos
					row.DrawOver(func(p *ui.Painter, rect ui.Rect) {
						y := rect.Y
						if pos == "after" {
							y = rect.Y + rect.H - 2
						}
						p.Fill(ui.Rect{X: rect.X + 6, Y: y, W: rect.W - 6, H: 2}, C.Select, 0)
					})
				}

				toggleClicked := false
				eyeClicked := false

				row.Children(func() {
					// Chevron toggle (24x28 hitbox) or 14px leaf spacer
					if l.Children > 0 {
						chevIcon := "chevronRight"
						if a.Layers.Open[l.ID] {
							chevIcon = "chevronDown"
						}
						chevLbl := "Expand " + l.Name
						if a.Layers.Open[l.ID] {
							chevLbl = "Collapse " + l.Name
						}
						tog := ui.ButtonBase(c.Key("layer-toggle-" + l.ID)).
							Size(20, LayerRowH).
							Shrink(0).
							Center().
							Label(chevLbl).
							Cursor(ui.CursorPointer)
						tog.Children(func() {
							ui.Icon(c, IconSVG(chevIcon)).FontSize(12).TextColor(C.Muted)
						})
						if tog.Clicked() {
							toggleClicked = true
							a.Layers.Open[l.ID] = !a.Layers.Open[l.ID]
						}
					} else {
						ui.Box(c).Size(16, LayerRowH).Shrink(0)
					}

					// Type icon
					iconCol := C.Ghost
					if l.Reusable || l.Type == "ref" {
						iconCol = C.Component
					}
					ui.Icon(c, IconSVG(NodeIconName(l.Type, l.Reusable))).
						FontSize(13).
						TextColor(iconCol).
						Shrink(0)

					// Inline rename input OR layer name label
					if a.Layers.RenameID == l.ID {
						inp := ui.TextInput(c.Key("layer-rename"), &a.Layers.RenameDraft).
							Grow(1).
							Basis(20).
							Height(22).
							Radius(4).
							Background(C.Panel2).
							Border(1, C.Select).
							TextColor(C.Text).
							FontSize(12).
							Label("Layer name").
							Bind(&a.Layers.RenameHandle)
						if a.Layers.RenameFocus {
							a.Layers.RenameFocus = false
							a.Layers.RenameHandle.Focus()
							a.Layers.RenameHandle.SelectAll()
						}
						inp.OnTextKey(func(k ui.TextKey) bool {
							if k.Key == ui.KeyEscape {
								a.Layers.RenameID = ""
								return true
							}
							return false
						})
						if inp.Submitted() || inp.FocusLeft() {
							a.commitLayerRename(l)
						}
					} else {
						nameCol := ui.Hex("#C9C9D0")
						if l.Reusable || l.Type == "ref" {
							nameCol = C.Component
						} else if isSelected {
							nameCol = C.Text
						}
						ui.Text(c, l.Name).
							FontSize(12).
							TextColor(nameCol).
							Grow(1).
							Basis(20).
							MaxLines(1)
					}

					// Visibility eye button (shown on hover or while hidden)
					if isHovered || l.Hidden {
						eyeIcon := "eye"
						eyeLbl := "Hide"
						if l.Hidden {
							eyeIcon = "hidden"
							eyeLbl = "Show"
						}
						eye := ui.ButtonBase(c.Key("layer-eye-" + l.ID)).
							Size(22, 22).
							Shrink(0).
							Radius(4).
							Center().
							Label(eyeLbl).
							Tooltip(eyeLbl).
							Cursor(ui.CursorPointer)
						if eye.Hovered() {
							eye.Background(C.Raised)
						}
						eye.Children(func() {
							ui.Icon(c, IconSVG(eyeIcon)).FontSize(12).TextColor(C.Muted)
						})
						if eye.Clicked() {
							eyeClicked = true
							var enabledVal any
							if !l.Hidden {
								enabledVal = false
							}
							var st CoreState
							if err := a.Bridge.Call("update", map[string]any{
								"ids":   []string{l.ID},
								"props": map[string]any{"enabled": enabledVal},
							}, &st); err == nil {
								a.State = st
								a.syncWindowTitle()
							}
						}
					}
				})

				// Middle-click zooms to fit layer
				if b, ok := row.PressedButton(); ok && b == 2 {
					a.FitTo([]string{l.ID})
				}

				// Drag-to-reorder / reparent
				_, dy, held := row.Dragged()
				if held && !toggleClicked && !eyeClicked && a.Layers.RenameID == "" {
					a.updateLayerDrag(idx, l, dy, vis)
				} else if a.Layers.Drag.active && !held {
					a.finishLayerDrag()
				}

				if !toggleClicked && !eyeClicked && a.Layers.RenameID == "" && row.Clicked() {
					a.handleLayerRowClick(l, idx, vis, row.ClickModifiers())
				}
			})
	})
}

func (a *NalarupaApp) commitLayerRename(l LayerRow) {
	if a.Layers.RenameID != l.ID {
		return
	}
	trimmed := strings.TrimSpace(a.Layers.RenameDraft)
	a.Layers.RenameID = ""
	if trimmed != "" && trimmed != l.Name {
		var st CoreState
		if err := a.Bridge.Call("update", map[string]any{
			"ids":   []string{l.ID},
			"props": map[string]any{"name": trimmed},
		}, &st); err == nil {
			a.State = st
			a.syncWindowTitle()
		}
	}
}

func (a *NalarupaApp) handleLayerRowClick(l LayerRow, idx int, vis []LayerRow, mods ui.Modifiers) {
	now := time.Now()
	a.Layers.FocusID = l.ID
	if a.Layers.LastClickID == l.ID && now.Sub(a.Layers.LastClickAt) <= 450*time.Millisecond && !strings.Contains(l.ID, "/") {
		a.Layers.RenameID = l.ID
		a.Layers.RenameDraft = l.Name
		a.Layers.RenameFocus = true
		a.Layers.LastClickID = ""
		return
	}
	a.Layers.LastClickID = l.ID
	a.Layers.LastClickAt = now

	hasCmd := (mods&ui.Cmd != 0) || (mods&ui.Ctrl != 0)
	hasShift := mods&ui.Shift != 0
	switch {
	case hasCmd:
		if slices.Contains(a.State.Selection, l.ID) {
			var next []string
			for _, id := range a.State.Selection {
				if id != l.ID {
					next = append(next, id)
				}
			}
			a.Select(next)
		} else {
			a.Select(append(append([]string{}, a.State.Selection...), l.ID))
		}
	case hasShift && len(a.State.Selection) > 0:
		anchorIdx := idx
		for i, r := range vis {
			if r.ID == a.State.Selection[0] {
				anchorIdx = i
				break
			}
		}
		lo, hi := min(anchorIdx, idx), max(anchorIdx, idx)
		var next []string
		for i := lo; i <= hi; i++ {
			next = append(next, vis[i].ID)
		}
		a.Select(next)
	default:
		a.Select([]string{l.ID})
	}
}

func (a *NalarupaApp) updateLayerDrag(originIdx int, l LayerRow, dy float32, vis []LayerRow) {
	if !a.Layers.Drag.active {
		ids := []string{l.ID}
		if slices.Contains(a.State.Selection, l.ID) {
			ids = append([]string{}, a.State.Selection...)
		} else {
			a.Select(ids)
		}
		for _, id := range ids {
			if strings.Contains(id, "/") {
				return
			}
		}
		a.Layers.Drag = layerDragState{
			active: true,
			ids:    ids,
			curY:   float32(originIdx)*LayerRowH + LayerRowH/2,
		}
	}
	a.Layers.Drag.curY += dy
	if math.Abs(float64(a.Layers.Drag.curY-(float32(originIdx)*LayerRowH+LayerRowH/2))) < 4 {
		a.Layers.Drag.targetID = ""
		return
	}
	rowIdx := int(math.Floor(float64(a.Layers.Drag.curY) / LayerRowH))
	if rowIdx < 0 {
		rowIdx = 0
	}
	if rowIdx >= len(vis) {
		rowIdx = len(vis) - 1
	}
	target := vis[rowIdx]
	if slices.Contains(a.Layers.Drag.ids, target.ID) {
		a.Layers.Drag.targetID = ""
		return
	}
	off := a.Layers.Drag.curY - float32(rowIdx)*LayerRowH
	canInside := (target.Type == "frame" || target.Type == "group") && !strings.Contains(target.ID, "/")
	pos := "after"
	if canInside {
		switch {
		case off < 7:
			pos = "before"
		case off > 21:
			pos = "after"
		default:
			pos = "inside"
		}
	} else if off < 14 {
		pos = "before"
	}
	a.Layers.Drag.targetID = target.ID
	a.Layers.Drag.targetPos = pos
}

func (a *NalarupaApp) finishLayerDrag() {
	d := a.Layers.Drag
	a.Layers.Drag = layerDragState{}
	if !d.active || d.targetID == "" || len(d.ids) == 0 {
		return
	}
	target, ok := a.Layers.CachedByID[d.targetID]
	if !ok {
		return
	}
	if d.targetPos == "inside" {
		args := map[string]any{
			"ids":    d.ids,
			"parent": target.ID,
		}
		if target.Layout {
			args["index"] = 0
		}
		var st CoreState
		if err := a.Bridge.Call("reparent", args, &st); err != nil {
			a.ShowToast(err.Error(), true)
		} else {
			a.applyCoreState(st)
		}
		return
	}
	parentID := target.Parent
	if parentID == "" {
		parentID = "document"
	}
	flow := false
	if parentRow, ok := a.Layers.CachedByID[parentID]; ok {
		flow = parentRow.Layout
	}
	insertIdx := target.Index + 1
	if flow == (d.targetPos == "before") {
		insertIdx = target.Index
	}
	// Adjust for dragged siblings from the same parent whose index < insertIdx
	for _, id := range d.ids {
		if sib, ok := a.Layers.CachedByID[id]; ok && sib.Parent == target.Parent && sib.Index < insertIdx {
			insertIdx--
		}
	}
	if insertIdx < 0 {
		insertIdx = 0
	}
	var st CoreState
	if err := a.Bridge.Call("reparent", map[string]any{
		"ids":    d.ids,
		"parent": parentID,
		"index":  insertIdx,
	}, &st); err != nil {
		a.ShowToast(err.Error(), true)
	} else {
		a.applyCoreState(st)
	}
}

func (a *NalarupaApp) handleLayersTreeKey(ev ui.InputEvent, vis []LayerRow) bool {
	if len(vis) == 0 {
		return false
	}
	curIdx := 0
	for i, r := range vis {
		if r.ID == a.Layers.FocusID {
			curIdx = i
			break
		}
	}
	cur := vis[curIdx]
	hasCmd := (ev.Mods&ui.Cmd != 0) || (ev.Mods&ui.Ctrl != 0)
	hasShift := ev.Mods&ui.Shift != 0
	hasAlt := ev.Mods&ui.Alt != 0
	if hasAlt {
		return false
	}

	switch ev.Key {
	case ui.KeyDown:
		if !hasCmd && !hasShift && curIdx+1 < len(vis) {
			a.Layers.FocusID = vis[curIdx+1].ID
			a.Layers.List.ScrollIntoView(curIdx + 1)
			return true
		}
	case ui.KeyUp:
		if !hasCmd && !hasShift && curIdx > 0 {
			a.Layers.FocusID = vis[curIdx-1].ID
			a.Layers.List.ScrollIntoView(curIdx - 1)
			return true
		}
	case ui.KeyHome:
		a.Layers.FocusID = vis[0].ID
		a.Layers.List.ScrollIntoView(0)
		return true
	case ui.KeyEnd:
		last := len(vis) - 1
		a.Layers.FocusID = vis[last].ID
		a.Layers.List.ScrollIntoView(last)
		return true
	case ui.KeyRight:
		if !hasCmd && !hasShift && cur.Children > 0 {
			if !a.Layers.Open[cur.ID] {
				a.Layers.Open[cur.ID] = true
			} else if curIdx+1 < len(vis) {
				a.Layers.FocusID = vis[curIdx+1].ID
				a.Layers.List.ScrollIntoView(curIdx + 1)
			}
			return true
		}
	case ui.KeyLeft:
		if !hasCmd && !hasShift {
			if cur.Children > 0 && a.Layers.Open[cur.ID] {
				a.Layers.Open[cur.ID] = false
				return true
			}
			if cur.Parent != "" && cur.Parent != "document" {
				a.Layers.FocusID = cur.Parent
				for i, r := range vis {
					if r.ID == cur.Parent {
						a.Layers.List.ScrollIntoView(i)
						break
					}
				}
				return true
			}
		}
	case ui.KeySpace, ui.KeyEnter:
		if ev.Key == ui.KeyEnter && hasCmd {
			return false
		}
		if hasCmd {
			if slices.Contains(a.State.Selection, cur.ID) {
				var next []string
				for _, id := range a.State.Selection {
					if id != cur.ID {
						next = append(next, id)
					}
				}
				a.Select(next)
			} else {
				a.Select(append(append([]string{}, a.State.Selection...), cur.ID))
			}
		} else if hasShift && len(a.State.Selection) > 0 {
			anchorIdx := curIdx
			for i, r := range vis {
				if r.ID == a.State.Selection[0] {
					anchorIdx = i
					break
				}
			}
			lo, hi := min(anchorIdx, curIdx), max(anchorIdx, curIdx)
			var next []string
			for i := lo; i <= hi; i++ {
				next = append(next, vis[i].ID)
			}
			a.Select(next)
		} else {
			a.Select([]string{cur.ID})
		}
		return true
	}
	return false
}

func (a *NalarupaApp) viewSlidesTab(c *ui.Context) {
	allFrames := a.ensureFramesFetched()
	var slides []Box
	liveSlideIDs := make(map[string]bool, len(allFrames))
	for _, f := range allFrames {
		if f.Type == "frame" {
			slides = append(slides, f)
			liveSlideIDs[f.ID] = true
		}
	}
	for id, ct := range a.Layers.Thumbs {
		if !liveSlideIDs[id] {
			if ct.bmp != nil {
				ct.bmp.ReleasePixels()
			}
			delete(a.Layers.Thumbs, id)
		}
	}

	ui.Column(c.Key("slides-panel")).Fill().Children(func() {
		// Header bar
		ui.Row(c.Key("slides-header")).
			FillWidth().
			Height(36).
			Padding(0, 10).
			Gap(4).
			AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).
			BorderColor(C.Border).
			Children(func() {
				word := "slides"
				if len(slides) == 1 {
					word = "slide"
				}
				ui.Text(c, fmt.Sprintf("%d %s · top-level frames", len(slides), word)).
					FontSize(11).
					TextColor(C.Muted).
					Grow(1).
					MaxLines(1)
				expBtn := IconBtn(c, "export-deck-pdf", "download", "Export deck to PDF", 26, false)
				if expBtn.Clicked() {
					a.exportDeckPDF()
				}
				presBtn := IconBtn(c, "present-from-slides", "play", "Present", 26, false)
				if presBtn.Clicked() {
					a.EnterPresent()
				}
			})

		if len(slides) == 0 {
			ui.Box(c.Key("slides-empty")).Fill().Padding(16).Children(func() {
				ui.Text(c, "No slides yet. Add top-level frames or choose the Slide deck template.").
					FontSize(12).
					TextColor(C.Ghost).
					Wrap()
			})
			return
		}

		// Render up to 8 uncached thumbnails per frame pass so scrolling 60+ slides stays responsive
		renderedThisFrame := 0
		ui.List(c.Key("slides-list"), &a.Layers.SlidesList, len(slides)).
			Fill().
			Padding(8, 10).
			ItemKey(func(i int) any {
				if i >= 0 && i < len(slides) {
					return slides[i].ID
				}
				return i
			}).
			Rows(func(r ui.ListRow) {
				idx := r.Index
				if idx < 0 || idx >= len(slides) {
					return
				}
				s := slides[idx]
				slideNum := idx + 1
				isSelected := slices.Contains(a.State.Selection, s.ID)

				th := max(20, int(math.Round((s.H/max(s.W, 1))*SlideThumbW)))
				ct, hasThumb := a.Layers.Thumbs[s.ID]
				if (!hasThumb || ct.version != a.State.Version) && renderedThisFrame < 8 {
					renderedThisFrame++
					scale := min(1.0, SlideThumbW/max(s.W, 1.0))
					var b64 string
					var decoded *ui.Bitmap
					if a.Bridge.TryCall("png", map[string]any{"id": s.ID, "scale": scale}, &b64) && b64 != "" {
						if raw, err := DecodeBase64Image(b64); err == nil {
							if bmp, err := ui.DecodeBitmap(raw); err == nil {
								decoded = bmp
							}
						}
					}
					if decoded != nil {
						if hasThumb && ct.bmp != nil {
							ct.bmp.ReleasePixels()
						}
						ct = cachedThumb{version: a.State.Version, bmp: decoded}
					} else {
						ct = cachedThumb{version: a.State.Version, bmp: ct.bmp, failed: true}
					}
					a.Layers.Thumbs[s.ID] = ct
					hasThumb = true
				}

				card := ui.ButtonBase(c.Key(fmt.Sprintf("slide-%d", slideNum))).
					FillWidth().
					Padding(6, 4).
					Radius(8).
					Column().
					Gap(6).
					Label(fmt.Sprintf("Slide %d: %s", slideNum, s.Name)).
					Cursor(ui.CursorPointer)
				if card.Hovered() {
					card.Background(C.Panel2)
				}
				borderCol := C.Border
				if isSelected {
					borderCol = C.Select
				}
				card.Children(func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
						ui.Text(c, fmt.Sprintf("%d", slideNum)).
							Width(18).
							FontSize(11).
							FontFeatures("tnum").
							TextColor(C.Ghost)
						thumbBox := ui.Box(c.Key(fmt.Sprintf("slide-thumb-%d", slideNum))).
							Size(SlideThumbW, float32(th)).
							Radius(6).
							Clip().
							Background(C.Raised).
							Border(2, borderCol)
						thumbBox.Children(func() {
							if hasThumb && ct.bmp != nil {
								ui.Image(c, ct.bmp).Fill().Fit(ui.FillBox)
							} else {
								ui.Box(c.Key(fmt.Sprintf("slide-thumb-pending-%d", slideNum))).
									Fill().
									Background(C.Raised)
							}
						})
					})
					ui.Row(c).FillWidth().Padding(0, 0, 0, 26).Children(func() {
						ui.Text(c, s.Name).
							FontSize(11).
							TextColor(C.Text).
							Grow(1).
							MaxLines(1)
					})
				})
				if card.Clicked() {
					a.Select([]string{s.ID})
					a.FitTo([]string{s.ID})
				}
			})
	})
}

func (a *NalarupaApp) exportDeckPDF() {
	var files []string
	if err := a.Bridge.Call("export", map[string]any{
		"ids":    []string{},
		"format": "pdf",
		"path":   "",
	}, &files); err != nil {
		a.ShowToast(err.Error(), true)
		return
	}
	if len(files) > 0 {
		a.ShowToast("Exported "+files[0], false)
	}
}
