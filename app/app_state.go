package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// ToastMsg represents an active bottom-left status or error toast notification.
type ToastMsg struct {
	Text      string
	Danger    bool
	ExpiresAt time.Time
}

// NalarupaApp holds all persistent state for a Nalarupa window or headless Tester instance.
type NalarupaApp struct {
	Bridge   *Bridge
	Window   *mygo.Window
	Services ui.Services

	State   CoreState
	MCPURL  string
	DataDir string
	Opened  bool

	Screen      string // "home" | "editor" | "present"
	PresentFrom string
	Tool        string // "move" | "frame" | "rectangle" | "ellipse" | "text" | "pen" | "hand"
	RightTab    string // "agent" | "design"

	Cam          Camera
	CanvasW      float64
	CanvasH      float64
	HoverLayerID string

	Toast          *ToastMsg
	UnsavedPending func()
	HomeRecents    []string

	Store     *AgentStore
	Layers    LayersState
	Canvas    CanvasState
	Inspector InspectorState
	AgentUI   AgentPanelState
	Present   PresentState

	CanvasHandle ui.Handle

	lastReducedMotion *bool
	nudgeCoalesce     string
	nudgeLastAt       time.Time

	pollStop   chan struct{}
	closeOnce  sync.Once
	bgWG       sync.WaitGroup
	exporting  atomic.Bool
	exportDone chan exportOutcome
}

type exportOutcome struct {
	files []string
	err   error
}

// NewNalarupaApp constructs a new NalarupaApp backed by an in-process Bridge at dataDir.
func NewNalarupaApp(dataDir string) *NalarupaApp {
	app := &NalarupaApp{
		Bridge:   NewBridge(dataDir),
		Screen:   "home",
		Tool:     "move",
		RightTab: "agent",
		Cam:      Camera{X: -100, Y: -100, Zoom: 0.5},
		CanvasW:  float64(DefaultWinW - LeftW - RightW),
		CanvasH:  float64(DefaultWinH - TopH),
		Store:      NewAgentStore(),
		pollStop:   make(chan struct{}),
		exportDone: make(chan exportOutcome, 4),
	}
	app.initLayersState()
	app.initCanvasState()
	app.initAgentPanelState()
	return app
}

// Init runs core "init", loads settings/recents, and syncs initial reduced-motion state.
func (a *NalarupaApp) Init() {
	var res InitResult
	if err := a.Bridge.Call("init", nil, &res); err == nil {
		a.State = res.State
		a.MCPURL = res.MCPURL
		a.DataDir = res.DataDir
	}
	rm := InitialReducedMotion()
	a.lastReducedMotion = &rm
	a.Bridge.TryCall("reduce_motion", map[string]any{"on": rm}, nil)
	a.reloadAgentSettings()
	a.RefreshHomeRecents()
}

func shouldWakeWindowOnWorkerReady(vsyncActive *atomic.Bool) bool {
	return vsyncActive == nil || !vsyncActive.Load()
}

// AttachWindow binds a live *mygo.Window and starts the background core event poller and async canvas worker.
func (a *NalarupaApp) AttachWindow(win *mygo.Window) {
	a.Window = win
	if os.Getenv("NALARUPA_REDUCED_MOTION") == "" {
		rm := mygo.Theme.Preferences().ReduceMotion
		a.lastReducedMotion = &rm
		a.Bridge.TryCall("reduce_motion", map[string]any{"on": rm}, nil)
	}
	a.Canvas.worker = NewCanvasRenderWorker(a.Bridge, func() {
		if !shouldWakeWindowOnWorkerReady(&a.Canvas.vsyncActive) {
			// Painter.AnimationFrame() is already driving vp.Draw at hardware VSync and will swap
			// TakeReady() on the very next refresh without triggering a full-window View(c) rebuild.
			return
		}
		if a.Window != nil && !a.Window.IsDestroyed() {
			a.Window.Update(func() {})
		}
	})
	a.syncWindowTitle()
	go a.pollLoop()
}

// Close stops the poller, async render/export workers, and flushes agent sessions / subprocesses cleanly.
func (a *NalarupaApp) Close() {
	a.closeOnce.Do(func() {
		close(a.pollStop)
		if a.Canvas.worker != nil {
			a.Canvas.worker.Close()
		}
		a.bgWG.Wait()
		a.Bridge.Shutdown()
	})
}

// TriggerExport runs document/node export on a managed background goroutine so heavy PNG/JPEG/WebP/PDF
// rasterization and encoding never block the main UI thread or cause Windows "(Not Responding)".
func (a *NalarupaApp) TriggerExport(ids []string, format, label string, opts map[string]any) {
	if !a.exporting.CompareAndSwap(false, true) {
		a.ShowToast("Export already in progress…", false)
		return
	}
	idsCopy := append([]string(nil), ids...)
	a.ShowToast("Exporting "+label+"…", false)

	a.bgWG.Add(1)
	go func() {
		defer a.bgWG.Done()
		defer a.exporting.Store(false)
		args := map[string]any{
			"ids":    idsCopy,
			"format": format,
			"path":   "",
		}
		if opts != nil {
			args["opts"] = opts
		}
		var files []string
		err := a.Bridge.Call("export", args, &files)
		select {
		case a.exportDone <- exportOutcome{files: files, err: err}:
		default:
		}
		if a.Window != nil && !a.Window.IsDestroyed() {
			a.Window.Update(func() {
				a.drainExportDone()
			})
		}
	}()
}

func (a *NalarupaApp) drainExportDone() {
	for {
		select {
		case out := <-a.exportDone:
			if out.err != nil {
				a.ShowToast(out.err.Error(), true)
			} else if len(out.files) > 0 {
				a.ShowToast("Exported "+strings.Join(out.files, ", "), false)
			}
		default:
			return
		}
	}
}

func sameCoreState(x, y CoreState) bool {
	if x.Version != y.Version || x.Title != y.Title || x.Dirty != y.Dirty ||
		x.AgentRunning != y.AgentRunning || x.Executing != y.Executing ||
		x.PenDrawing != y.PenDrawing || x.MCPPort != y.MCPPort || x.Animating != y.Animating {
		return false
	}
	px, py := "", ""
	if x.Path != nil {
		px = *x.Path
	}
	if y.Path != nil {
		py = *y.Path
	}
	if px != py {
		return false
	}
	return slices.Equal(x.Selection, y.Selection)
}

func sameUICoreState(x, y CoreState) bool {
	if x.Title != y.Title || x.Dirty != y.Dirty ||
		x.AgentRunning != y.AgentRunning || x.Executing != y.Executing ||
		x.PenDrawing != y.PenDrawing || x.MCPPort != y.MCPPort {
		return false
	}
	px, py := "", ""
	if x.Path != nil {
		px = *x.Path
	}
	if y.Path != nil {
		py = *y.Path
	}
	if px != py {
		return false
	}
	return slices.Equal(x.Selection, y.Selection)
}

func (a *NalarupaApp) pollLoop() {
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	var last CoreState
	var lastViewSync time.Time
	hasLast := false
	for {
		select {
		case <-a.pollStop:
			return
		case <-ticker.C:
			res, ok := a.Bridge.TryPollFast()
			if !ok {
				continue
			}
			if !hasLast || len(res.Events) > 0 || !sameCoreState(res.State, last) {
				// While VSync AnimationFrame is actively painting the canvas (which reads VersionDirect atomically),
				// coalesce pure Version/Animating-only ticks (with zero events and unchanged UI chrome state) so we
				// don't force full-window View(c) rebuilds every 30ms just for slide expiration ticks.
				if hasLast && len(res.Events) == 0 && sameUICoreState(res.State, last) &&
					a.Canvas.vsyncActive.Load() && time.Since(lastViewSync) < 220*time.Millisecond {
					continue
				}
				last = res.State
				hasLast = true
				lastViewSync = time.Now()
				if a.Window != nil && !a.Window.IsDestroyed() {
					resCopy := res
					a.Window.Update(func() {
						a.ApplyPollResult(resCopy, float64(time.Now().UnixNano())/1e6)
					})
				}
			}
		}
	}
}

// PollNow synchronously polls core events on the UI thread (used in headless tests and immediate actions).
func (a *NalarupaApp) PollNow(nowMS float64) {
	res := a.Bridge.PollFast()
	a.ApplyPollResult(res, nowMS)
}

// applyCoreState updates a.State, syncs window title, and triggers selection reveal/tab updates when selection changed.
func (a *NalarupaApp) applyCoreState(st CoreState) {
	prevSel := a.State.Selection
	a.State = st
	a.syncWindowTitle()
	if !slices.Equal(prevSel, st.Selection) {
		a.onSelectionChanged(st.Selection)
	}
}

// RefreshState refreshes a.State synchronously after a local edit command.
func (a *NalarupaApp) RefreshState() {
	a.applyCoreState(a.Bridge.CoreStateDirect())
}

// ApplyPollResult applies a PollResult onto NalarupaApp on the main thread.
func (a *NalarupaApp) ApplyPollResult(res PollResult, nowMS float64) {
	a.applyCoreState(res.State)
	for _, ev := range res.Events {
		switch ev["type"] {
		case "toast":
			if txt, ok := ev["text"].(string); ok && txt != "" {
				a.ShowToast(txt, false)
			}
		case "document_opened":
			a.onDocumentOpened()
		case "models_fetched":
			a.onModelsFetched(ev)
		case "typesafe_models":
			a.onTypeSafeModelsFetched(ev)
		}
	}
	a.Store.ViewCenter = Pt{
		a.Cam.X + (a.CanvasW/2)/max(0.02, a.Cam.Zoom),
		a.Cam.Y + (a.CanvasH/2)/max(0.02, a.Cam.Zoom),
	}
	a.Store.ViewZoom = a.Cam.Zoom
	a.Store.ApplyEvents(res.Events, nowMS)
}

func (a *NalarupaApp) syncWindowTitle() {
	if a.Window == nil || a.Window.IsDestroyed() {
		return
	}
	title := a.State.Title
	if title == "" {
		title = "Untitled"
	}
	dot := ""
	if a.State.Dirty {
		dot = " •"
	}
	want := fmt.Sprintf("%s%s — Nalarupa", title, dot)
	if a.Window.Title() != want {
		a.Window.SetTitle(want)
	}
}

// Busy reports whether an agent run, script execution, AI Pen, or background export is active.
func (a *NalarupaApp) Busy() bool {
	return a.State.AgentRunning || a.State.Executing || a.State.PenDrawing || a.exporting.Load()
}

func (a *NalarupaApp) busySwitchMessage() string {
	if a.State.AgentRunning || a.State.Executing || a.State.PenDrawing {
		return "Stop the agent or the AI Pen before opening another document."
	}
	return "Wait for the export to finish before opening another document."
}

// ShowToast displays a bottom-left toast notification (3500ms for status, sticky for errors).
func (a *NalarupaApp) ShowToast(text string, danger bool) {
	msg := &ToastMsg{
		Text:   text,
		Danger: danger,
	}
	if !danger {
		msg.ExpiresAt = time.Now().Add(3500 * time.Millisecond)
	}
	a.Toast = msg
}

func (a *NalarupaApp) onDocumentOpened() {
	a.Opened = true
	a.UnsavedPending = nil
	a.AgentUI.Skills = nil
	a.AgentUI.SkillsLoaded = false
	a.AgentUI.SkillsLoading = false
	a.AgentUI.SkillsReqSeq++
	a.AgentUI.DismissedSlash = ""
	a.AgentUI.SlashIndex = 0
	a.resetLayersForNewDoc()
	a.resetCanvasForNewDoc()
	a.Store.ClearPresence()
	a.Screen = "editor"
	a.RefreshState()
	a.RefreshHomeRecents()
	a.FitTo(nil)
	a.Canvas.pendingInitialFit = true
}

func (a *NalarupaApp) onSelectionChanged(ids []string) {
	a.Layers.revealSelection(a, ids)
	if len(ids) > 0 && a.RightTab == "agent" && len(a.Store.Items) == 0 {
		a.RightTab = "design"
	}
}

// Select updates the core selection and triggers reveal/tab side-effects when selection actually changes.
func (a *NalarupaApp) Select(ids []string) {
	if ids == nil {
		ids = []string{}
	}
	changed := !slices.Equal(a.State.Selection, ids)
	st := a.Bridge.SelectDirect(ids)
	a.State = st
	if changed {
		a.onSelectionChanged(st.Selection)
	}
}

// StartTemplate creates a new document from template ("blank","slides","mobile","empty"), guarding unsaved edits.
func (a *NalarupaApp) StartTemplate(tmpl string, force bool) {
	if a.Busy() {
		a.ShowToast(a.busySwitchMessage(), true)
		return
	}
	if !force && a.Opened && a.State.Dirty {
		a.UnsavedPending = func() { a.StartTemplate(tmpl, true) }
		return
	}
	var st CoreState
	if err := a.Bridge.Call("new", map[string]any{"template": tmpl}, &st); err != nil {
		a.ShowToast(err.Error(), true)
		return
	}
	a.State = st
	a.onDocumentOpened()
	a.syncWindowTitle()
}

// OpenDocument opens path on disk, guarding unsaved edits unless force is true.
func (a *NalarupaApp) OpenDocument(path string, force bool) {
	if a.Busy() {
		a.ShowToast(a.busySwitchMessage(), true)
		return
	}
	if !force && a.Opened && a.State.Dirty {
		a.UnsavedPending = func() { a.OpenDocument(path, true) }
		return
	}
	var st CoreState
	if err := a.Bridge.Call("open", map[string]any{"path": path}, &st); err != nil {
		a.ShowToast(err.Error(), true)
		a.RefreshHomeRecents()
		return
	}
	a.State = st
	a.onDocumentOpened()
	a.syncWindowTitle()
}

// SaveDocument writes the open document to its existing path (or DefaultSavePath if untitled), returning true on success.
func (a *NalarupaApp) SaveDocument(targetPath string) bool {
	p := targetPath
	if p == "" {
		if a.State.Path != nil && *a.State.Path != "" {
			p = *a.State.Path
		} else {
			p = DefaultSavePath(a.State.Title)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		a.ShowToast("Unable to save the document: "+err.Error(), true)
		return false
	}
	var res struct {
		Path  string    `json:"path"`
		State CoreState `json:"state"`
	}
	if err := a.Bridge.Call("save", map[string]any{"path": p}, &res); err != nil {
		a.ShowToast("Unable to save the document: "+err.Error(), true)
		return false
	}
	a.applyCoreState(res.State)
	a.RefreshHomeRecents()
	a.ShowToast("Saved "+res.Path, false)
	return true
}

// TriggerOpenDialog opens the native OS file picker on a background goroutine.
func (a *NalarupaApp) TriggerOpenDialog() {
	if a.Window == nil {
		return
	}
	win := a.Window
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win,
			Title:  "Open document",
			Filters: []mygo.FileFilter{
				{Name: "Nalarupa Documents", Extensions: []string{"nala", "pen"}},
			},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		win.Update(func() {
			a.OpenDocument(paths[0], false)
		})
	}()
}

// TriggerSaveAsDialog opens the native OS directory picker and saves via DefaultSavePath.
func (a *NalarupaApp) TriggerSaveAsDialog() {
	if a.Window == nil {
		a.SaveDocument(DefaultSavePath(a.State.Title))
		return
	}
	win := a.Window
	go func() {
		dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:            win,
			Title:             "Save here",
			Directory:         true,
			CreateDirectories: true,
		})
		if err != nil || len(dirs) == 0 {
			return
		}
		win.Update(func() {
			a.SaveDocument(DefaultSavePath(a.State.Title, dirs[0]))
		})
	}()
}

func (a *NalarupaApp) computeFitCamera(ids []string) Camera {
	reqIDs := ids
	if reqIDs == nil {
		reqIDs = []string{}
	}
	var b *Box
	if err := a.Bridge.Call("bounds", map[string]any{"ids": reqIDs}, &b); err != nil || b == nil || b.W <= 0 || b.H <= 0 {
		return Camera{X: -100, Y: -100, Zoom: 0.5}
	}
	return FitCamera(b.RectOf(), a.CanvasW, a.CanvasH, 60, 1.0)
}

// FitTo immediately adjusts Cam to fit ids (or all content if len(ids) == 0) with 60px padding and max zoom 1.
func (a *NalarupaApp) FitTo(ids []string) {
	a.Canvas.camAnim = camAnimState{}
	a.Cam = a.computeFitCamera(ids)
	a.Canvas.markSharpDirty()
	a.syncViewport()
}

// FitToAnimated smoothly glides Cam to fit ids (snapping immediately when Reduced Motion is enabled).
func (a *NalarupaApp) FitToAnimated(ids []string) {
	target := a.computeFitCamera(ids)
	if a.lastReducedMotion != nil && *a.lastReducedMotion {
		a.Canvas.camAnim = camAnimState{}
		a.Cam = target
		a.Canvas.markSharpDirty()
		a.syncViewport()
		return
	}
	if math.Abs(target.Zoom-a.Cam.Zoom) < 1e-4 && math.Hypot(target.X-a.Cam.X, target.Y-a.Cam.Y) < 0.5 {
		a.Canvas.camAnim = camAnimState{}
		return
	}
	a.Canvas.camAnim = camAnimState{
		active:  true,
		mode:    "camera",
		targetX: target.X,
		targetY: target.Y,
		targetZ: target.Zoom,
		lastAt:  time.Now(),
	}
	a.Canvas.markFastDirty()
}

// ZoomBy smoothly multiplies zoom around the center of the canvas viewport.
func (a *NalarupaApp) ZoomBy(factor float64) {
	baseZ := a.Cam.Zoom
	if a.Canvas.camAnim.active && a.Canvas.camAnim.mode == "zoom-anchor" {
		baseZ = a.Canvas.camAnim.targetZ
	}
	a.ZoomAt(a.CanvasW/2, a.CanvasH/2, baseZ*factor, false)
}

// ZoomTo smoothly sets zoom to targetZoom around the center of the canvas viewport.
func (a *NalarupaApp) ZoomTo(targetZoom float64) {
	a.ZoomAt(a.CanvasW/2, a.CanvasH/2, targetZoom, false)
}

// ZoomAtImmediate snaps zoom immediately to nextZoom keeping screen point (sx, sy) anchored on the same document point.
func (a *NalarupaApp) ZoomAtImmediate(sx, sy, nextZoom float64, sharp bool) {
	a.Canvas.camAnim = camAnimState{}
	nextZoom = max(0.02, min(64.0, nextZoom))
	if math.Abs(nextZoom-a.Cam.Zoom) < 1e-9 {
		return
	}
	docX := a.Cam.X + sx/a.Cam.Zoom
	docY := a.Cam.Y + sy/a.Cam.Zoom
	a.Cam.Zoom = nextZoom
	a.Cam.X = docX - sx/nextZoom
	a.Cam.Y = docY - sy/nextZoom
	if sharp {
		a.Canvas.markSharpDirty()
	} else {
		a.Canvas.markFastDirty()
	}
	a.syncViewport()
}

// ZoomAt smoothly zooms toward nextZoom keeping canvas screen point (sx, sy) anchored on the same document point.
func (a *NalarupaApp) ZoomAt(sx, sy, nextZoom float64, sharp bool) {
	nextZoom = max(0.02, min(64.0, nextZoom))
	if sharp || (a.lastReducedMotion != nil && *a.lastReducedMotion) {
		a.ZoomAtImmediate(sx, sy, nextZoom, sharp)
		return
	}
	curTarget := a.Cam.Zoom
	if a.Canvas.camAnim.active && a.Canvas.camAnim.mode == "zoom-anchor" {
		curTarget = a.Canvas.camAnim.targetZ
	}
	if math.Abs(nextZoom-curTarget) < 1e-9 && math.Abs(nextZoom-a.Cam.Zoom) < 1e-9 {
		return
	}
	if a.Canvas.camAnim.active && a.Canvas.camAnim.mode == "zoom-anchor" &&
		math.Hypot(sx-a.Canvas.camAnim.anchorSX, sy-a.Canvas.camAnim.anchorSY) <= 8.0 {
		a.Canvas.camAnim.targetZ = nextZoom
	} else {
		a.Canvas.camAnim = camAnimState{
			active:   true,
			mode:     "zoom-anchor",
			anchorSX: sx,
			anchorSY: sy,
			docX:     a.Cam.X + sx/max(0.02, a.Cam.Zoom),
			docY:     a.Cam.Y + sy/max(0.02, a.Cam.Zoom),
			targetZ:  nextZoom,
			lastAt:   time.Now(),
		}
	}
	a.Canvas.markFastDirty()
}

func (a *NalarupaApp) syncViewport() {
	a.syncViewportMode(false)
}

func (a *NalarupaApp) syncViewportNonBlocking() {
	a.syncViewportMode(true)
}

func (a *NalarupaApp) syncViewportMode(nonBlocking bool) {
	if a.CanvasW <= 0 || a.CanvasH <= 0 || a.Cam.Zoom <= 0 {
		return
	}
	a.Bridge.ViewportDirect(a.Cam.X, a.Cam.Y, a.CanvasW/a.Cam.Zoom, a.CanvasH/a.Cam.Zoom, nonBlocking)
	a.Store.ViewCenter = Pt{
		a.Cam.X + (a.CanvasW/2)/a.Cam.Zoom,
		a.Cam.Y + (a.CanvasH/2)/a.Cam.Zoom,
	}
	a.Store.ViewZoom = a.Cam.Zoom
}

// EnterPresent enters full-window presentation mode starting from the current selection (or first slide).
func (a *NalarupaApp) EnterPresent() {
	from := ""
	if len(a.State.Selection) > 0 {
		from = a.State.Selection[0]
	}
	a.PresentFrom = from
	a.Present.initForApp(a, from)
	a.Screen = "present"
}

// SetTool switches the active editor tool (entering vector point edit if P is pressed with a single path selected).
func (a *NalarupaApp) SetTool(tool string, returnFocus bool) {
	if a.Canvas.drawDrag != nil {
		if tool == "move" {
			a.flushPendingDragMove()
			a.Canvas.finishDrawDrag(a)
		} else {
			a.Canvas.cancelDrawDrag()
		}
	}
	if tool == "pen" && len(a.State.Selection) == 1 && a.Canvas.pen == nil {
		var nd NodeDetail
		if a.Bridge.TryCall("node", map[string]any{"id": a.State.Selection[0]}, &nd) && nd.Exists {
			if t, _ := nd.Node["type"].(string); t == "path" {
				a.Canvas.enterPenEdit(a, a.State.Selection[0], "draw")
				a.Tool = "pen"
				if returnFocus {
					a.CanvasHandle.Focus()
				}
				return
			}
		}
	}
	if a.Canvas.pen != nil && tool != "pen" && tool != "move" {
		a.Canvas.exitPenEdit(a)
	} else if a.Canvas.pen != nil {
		if tool == "pen" {
			a.Canvas.pen.sub = "draw"
		} else if tool == "move" {
			a.Canvas.pen.sub = "move"
		}
	}
	a.Tool = tool
	a.syncGrabCursorNow()
	if returnFocus {
		a.CanvasHandle.Focus()
	}
}

// Undo performs an editor undo.
func (a *NalarupaApp) Undo() {
	var st CoreState
	if err := a.Bridge.Call("undo", nil, &st); err == nil {
		a.applyCoreState(st)
		if a.Canvas.pen != nil && a.Canvas.pen.pathID != "" {
			a.Canvas.reloadPenContours(a)
		}
	}
}

// Redo performs an editor redo.
func (a *NalarupaApp) Redo() {
	var st CoreState
	if err := a.Bridge.Call("redo", nil, &st); err == nil {
		a.applyCoreState(st)
		if a.Canvas.pen != nil && a.Canvas.pen.pathID != "" {
			a.Canvas.reloadPenContours(a)
		}
	}
}

// DeleteSelection deletes selected anchors (in Pen edit) or selected deletable layers.
func (a *NalarupaApp) DeleteSelection() {
	if a.Canvas.pen != nil {
		if len(a.Canvas.pen.sel) > 0 {
			a.Canvas.deleteSelectedPenPoints(a)
		}
		return
	}
	var deletable []string
	for _, id := range a.State.Selection {
		if !strings.Contains(id, "/") {
			deletable = append(deletable, id)
		}
	}
	if len(deletable) == 0 {
		return
	}
	var st CoreState
	if err := a.Bridge.Call("delete", map[string]any{"ids": deletable}, &st); err != nil {
		a.ShowToast(err.Error(), true)
		return
	}
	a.applyCoreState(st)
	a.CanvasHandle.Focus()
}

// CopySelection copies the current selection to the clipboard.
func (a *NalarupaApp) CopySelection() bool {
	if len(a.State.Selection) == 0 {
		return false
	}
	var cp CopyPayload
	if err := a.Bridge.Call("copy", map[string]any{"ids": a.State.Selection}, &cp); err != nil {
		a.ShowToast(err.Error(), true)
		return false
	}
	if cp.HTML == "" && cp.Text == "" {
		return false
	}
	WriteClipboard(a.Services, ClipboardPayload{HTML: cp.HTML, Text: cp.Text})
	return true
}

// CutSelection copies and deletes the current selection.
func (a *NalarupaApp) CutSelection() {
	if a.CopySelection() {
		a.DeleteSelection()
	}
}

// PasteClipboard pastes from clipboard into the current selection/viewport.
func (a *NalarupaApp) PasteClipboard() {
	clip := ReadClipboard(a.Services)
	if clip.HTML == "" && clip.Text == "" {
		return
	}
	view := map[string]any{
		"x": a.Cam.X,
		"y": a.Cam.Y,
		"w": a.CanvasW / max(0.02, a.Cam.Zoom),
		"h": a.CanvasH / max(0.02, a.Cam.Zoom),
	}
	targets := a.State.Selection
	if targets == nil {
		targets = []string{}
	}
	var res struct {
		IDs   []string  `json:"ids"`
		State CoreState `json:"state"`
	}
	if err := a.Bridge.Call("paste", map[string]any{
		"html":    clip.HTML,
		"text":    clip.Text,
		"targets": targets,
		"view":    view,
	}, &res); err != nil {
		a.ShowToast(err.Error(), true)
		return
	}
	a.State = res.State
	if len(res.IDs) > 0 && !slices.Equal(a.State.Selection, res.IDs) {
		a.Select(res.IDs)
	} else {
		a.onSelectionChanged(a.State.Selection)
	}
	a.syncWindowTitle()
}

// DuplicateSelection duplicates selected non-instance-child layers.
func (a *NalarupaApp) DuplicateSelection() {
	var ids []string
	for _, id := range a.State.Selection {
		if !strings.Contains(id, "/") {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	var res struct {
		IDs   []string  `json:"ids"`
		State CoreState `json:"state"`
	}
	if err := a.Bridge.Call("duplicate", map[string]any{"ids": ids}, &res); err != nil {
		a.ShowToast(err.Error(), true)
		return
	}
	a.State = res.State
	a.syncWindowTitle()
	a.onSelectionChanged(a.State.Selection)
}

// FlipSelection mirrors selected layers horizontally (horiz=true) or vertically (horiz=false) in place.
func (a *NalarupaApp) FlipSelection(horiz bool) {
	if len(a.State.Selection) == 0 {
		return
	}
	var boxes []Box
	a.Bridge.TryCall("boxes", map[string]any{"ids": a.State.Selection}, &boxes)
	if len(boxes) == 0 {
		return
	}
	var rects []RectD
	boxByID := map[string]Box{}
	for _, b := range boxes {
		pb := Painted(b)
		rects = append(rects, pb)
		boxByID[b.ID] = b
	}
	union := UnionRects(rects)
	if union == nil {
		return
	}
	ucx := union.X + union.W/2
	ucy := union.Y + union.H/2

	_, _ = a.Bridge.CallRaw("drag_begin", nil)
	for _, id := range a.State.Selection {
		var nd NodeDetail
		if !a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) || !nd.Exists || nd.Box == nil {
			continue
		}
		rot, fx, fy, bx, by := 0.0, false, false, 0.0, 0.0
		if nd.Local != nil {
			rot = nd.Local.Rotation
			fx = nd.Local.FlipX
			fy = nd.Local.FlipY
			bx = nd.Local.BX
			by = nd.Local.BY
		}
		nfx, nfy := fx, fy
		if horiz {
			nfx = !fx
		} else {
			nfy = !fy
		}
		props := map[string]any{
			"flipX": nfx,
			"flipY": nfy,
		}
		if nd.ParentLayout == "none" || nd.ParentLayout == "" {
			oldOff := CenterOffset(nd.Box.W, nd.Box.H, rot, fx, fy, bx, by)
			newOff := CenterOffset(nd.Box.W, nd.Box.H, rot, nfx, nfy, bx, by)
			nx := asFloat(nd.Node["x"]) + oldOff[0] - newOff[0]
			ny := asFloat(nd.Node["y"]) + oldOff[1] - newOff[1]
			if len(a.State.Selection) > 1 {
				pb := Painted(boxByID[id])
				pcx := pb.X + pb.W/2
				pcy := pb.Y + pb.H/2
				var parentM *Matrix6
				if nd.Parent != "" && nd.Parent != "document" {
					var pbs []Box
					if a.Bridge.TryCall("boxes", map[string]any{"ids": []string{nd.Parent}}, &pbs) && len(pbs) > 0 {
						parentM = pbs[0].M
					}
				}
				var dxDoc, dyDoc float64
				if horiz {
					dxDoc = (2*ucx - pcx) - pcx
				} else {
					dyDoc = (2*ucy - pcy) - pcy
				}
				lp := InParent(parentM, dxDoc, dyDoc)
				nx += lp[0]
				ny += lp[1]
			}
			props["x"] = math.Round(nx*100) / 100
			props["y"] = math.Round(ny*100) / 100
		}
		a.Bridge.TryCall("update", map[string]any{"ids": []string{id}, "props": props}, nil)
	}
	var st CoreState
	if err := a.Bridge.Call("drag_end", nil, &st); err == nil {
		a.State = st
		a.syncWindowTitle()
	}
}

// HandleEditorShortcut processes editor-wide keyboard shortcuts.
func (a *NalarupaApp) HandleEditorShortcut(mods ui.Modifiers, key ui.Key) bool {
	if a.Screen != "editor" {
		return false
	}
	hasCmd := (mods&ui.Cmd != 0) || (mods&ui.Ctrl != 0)
	hasShift := mods&ui.Shift != 0
	hasAlt := mods&ui.Alt != 0

	// Mid-draw Esc / V handling
	if a.Canvas.drawDrag != nil {
		if key == ui.KeyEscape {
			a.Canvas.cancelDrawDrag()
			return true
		}
		if !hasCmd && !hasAlt && !hasShift && key == ui.KeyV {
			a.SetTool("move", true)
			return true
		}
	}

	if hasCmd && key == ui.KeyEnter {
		a.EnterPresent()
		return true
	}
	if hasCmd && key == ui.KeyS {
		if hasShift {
			a.TriggerSaveAsDialog()
		} else {
			a.SaveDocument("")
		}
		return true
	}
	if hasCmd && key == ui.KeyZ {
		if hasShift {
			a.Redo()
		} else {
			a.Undo()
		}
		return true
	}
	if hasCmd && !hasShift && key == ui.KeyY {
		a.Redo()
		return true
	}
	if hasCmd && !hasShift && !hasAlt {
		switch key {
		case ui.KeyC:
			a.CopySelection()
			return true
		case ui.KeyX:
			a.CutSelection()
			return true
		case ui.KeyV:
			a.PasteClipboard()
			return true
		case ui.KeyD:
			a.DuplicateSelection()
			return true
		case ui.KeyA:
			if a.Canvas.pen != nil {
				for _, c := range a.Canvas.pen.contours {
					for _, pt := range c.Anchors {
						a.Canvas.pen.sel[pt] = true
					}
				}
				return true
			}
			a.selectAllSiblings()
			return true
		case ui.KeyG:
			if len(a.State.Selection) > 0 {
				var res struct {
					ID    string    `json:"id"`
					State CoreState `json:"state"`
				}
				if err := a.Bridge.Call("wrap", map[string]any{"ids": a.State.Selection, "kind": "group"}, &res); err == nil {
					a.State = res.State
					a.syncWindowTitle()
				}
			}
			return true
		case ui.Key0:
			a.ZoomTo(1.0)
			return true
		case ui.KeyBracketRight:
			a.reorderSelection("up")
			return true
		case ui.KeyBracketLeft:
			a.reorderSelection("down")
			return true
		case ui.KeyBackspace:
			a.unwrapSelection()
			return true
		}
	}
	if hasCmd && hasAlt && !hasShift && key == ui.KeyG {
		if len(a.State.Selection) > 0 {
			var res struct {
				ID    string    `json:"id"`
				State CoreState `json:"state"`
			}
			if err := a.Bridge.Call("wrap", map[string]any{"ids": a.State.Selection, "kind": "frame"}, &res); err == nil {
				a.State = res.State
				a.syncWindowTitle()
			}
		}
		return true
	}
	if hasCmd && hasShift && !hasAlt && key == ui.KeyG {
		a.unwrapSelection()
		return true
	}

	// Alt alignment shortcuts: Alt+W/A/S/D/H/V
	if hasAlt && !hasCmd && !hasShift {
		alignMap := map[ui.Key]string{
			ui.KeyW: "top",
			ui.KeyA: "left",
			ui.KeyS: "bottom",
			ui.KeyD: "right",
			ui.KeyH: "hcenter",
			ui.KeyV: "vcenter",
		}
		if dir, ok := alignMap[key]; ok {
			if len(a.State.Selection) > 0 {
				var st CoreState
				if err := a.Bridge.Call("align", map[string]any{"ids": a.State.Selection, "dir": dir}, &st); err == nil {
					a.State = st
					a.syncWindowTitle()
				}
			}
			return true
		}
	}

	// Shift+A / Alt+Shift+A auto-layout
	if !hasCmd && hasShift && key == ui.KeyA {
		if hasAlt {
			if len(a.State.Selection) > 0 {
				var st CoreState
				if err := a.Bridge.Call("remove_layout", map[string]any{"ids": a.State.Selection}, &st); err == nil {
					a.State = st
					a.syncWindowTitle()
				}
			}
		} else if len(a.State.Selection) > 0 {
			var res struct {
				ID    string    `json:"id"`
				State CoreState `json:"state"`
			}
			if err := a.Bridge.Call("add_layout", map[string]any{"ids": a.State.Selection}, &res); err == nil {
				a.State = res.State
				a.syncWindowTitle()
			}
		}
		return true
	}

	// Shift+H / Shift+V mirror, Shift+1 / Shift+2 fit
	if !hasCmd && !hasAlt && hasShift {
		switch key {
		case ui.KeyH:
			a.FlipSelection(true)
			return true
		case ui.KeyV:
			a.FlipSelection(false)
			return true
		case ui.Key1:
			a.FitToAnimated(nil)
			return true
		case ui.Key2:
			if len(a.State.Selection) > 0 {
				a.FitToAnimated(a.State.Selection)
			} else {
				a.FitToAnimated(nil)
			}
			return true
		}
	}

	// Unmodified keys
	if !hasCmd && !hasAlt && !hasShift {
		switch key {
		case ui.KeyV:
			a.SetTool("move", true)
			return true
		case ui.KeyF, ui.KeyA:
			a.SetTool("frame", true)
			return true
		case ui.KeyR:
			a.SetTool("rectangle", true)
			return true
		case ui.KeyO:
			a.SetTool("ellipse", true)
			return true
		case ui.KeyT:
			a.SetTool("text", true)
			return true
		case ui.KeyP:
			a.SetTool("pen", true)
			return true
		case ui.KeyH:
			a.SetTool("hand", true)
			return true
		case ui.KeyDelete, ui.KeyBackspace:
			a.DeleteSelection()
			return true
		case ui.KeyBracketRight:
			a.reorderSelection("front")
			return true
		case ui.KeyBracketLeft:
			a.reorderSelection("back")
			return true
		case ui.KeyEqual:
			a.ZoomBy(1.25)
			return true
		case ui.KeyMinus:
			a.ZoomBy(0.8)
			return true
		case ui.KeyEnter:
			return a.handleEnterShortcut(false)
		case ui.KeyEscape:
			return a.handleEscapeShortcut()
		}
	}
	if !hasCmd && !hasAlt && hasShift && (key == ui.KeyEqual) {
		a.ZoomBy(1.25)
		return true
	}
	if !hasCmd && !hasAlt && hasShift && key == ui.KeyEnter {
		return a.handleEnterShortcut(true)
	}

	// Arrow keys (nudge or auto-layout reorder)
	if !hasCmd {
		switch key {
		case ui.KeyLeft, ui.KeyRight, ui.KeyUp, ui.KeyDown:
			return a.handleArrowShortcut(key, hasShift, hasAlt)
		}
	}
	return false
}

func (a *NalarupaApp) selectAllSiblings() {
	if len(a.State.Selection) == 0 {
		var frames []Box
		a.Bridge.TryCall("frames", nil, &frames)
		ids := make([]string, len(frames))
		for i, f := range frames {
			ids[i] = f.ID
		}
		a.Select(ids)
		return
	}
	var nd NodeDetail
	if !a.Bridge.TryCall("node", map[string]any{"id": a.State.Selection[0]}, &nd) || !nd.Exists {
		return
	}
	if nd.Parent == "" || nd.Parent == "document" {
		var frames []Box
		a.Bridge.TryCall("frames", nil, &frames)
		ids := make([]string, len(frames))
		for i, f := range frames {
			ids[i] = f.ID
		}
		a.Select(ids)
		return
	}
	var kids []Box
	a.Bridge.TryCall("children_boxes", map[string]any{"id": nd.Parent}, &kids)
	ids := make([]string, len(kids))
	for i, k := range kids {
		ids[i] = k.ID
	}
	a.Select(ids)
}

func (a *NalarupaApp) unwrapSelection() {
	if len(a.State.Selection) == 0 {
		return
	}
	var res struct {
		IDs   []string  `json:"ids"`
		State CoreState `json:"state"`
	}
	if err := a.Bridge.Call("unwrap", map[string]any{"ids": a.State.Selection}, &res); err == nil {
		a.State = res.State
		a.syncWindowTitle()
	}
}

func (a *NalarupaApp) reorderSelection(dir string) {
	if len(a.State.Selection) == 0 {
		return
	}
	var st CoreState
	if err := a.Bridge.Call("reorder", map[string]any{"ids": a.State.Selection, "dir": dir}, &st); err == nil {
		a.State = st
		a.syncWindowTitle()
	}
}

func (a *NalarupaApp) handleEnterShortcut(selectParents bool) bool {
	if len(a.State.Selection) == 0 {
		return false
	}
	if selectParents {
		var parents []string
		seen := map[string]bool{}
		for _, id := range a.State.Selection {
			var nd NodeDetail
			if a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) && nd.Exists && nd.Parent != "" && nd.Parent != "document" {
				if !seen[nd.Parent] {
					seen[nd.Parent] = true
					parents = append(parents, nd.Parent)
				}
			}
		}
		if len(parents) > 0 {
			a.Select(parents)
		}
		return true
	}
	if len(a.State.Selection) == 1 {
		id := a.State.Selection[0]
		var nd NodeDetail
		if a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) && nd.Exists {
			ntype, _ := nd.Node["type"].(string)
			switch ntype {
			case "text":
				a.Canvas.startInlineTextEdit(a, id)
				return true
			case "path":
				a.Canvas.enterPenEdit(a, id, "move")
				return true
			}
		}
	}
	var kids []string
	for _, id := range a.State.Selection {
		var cb []Box
		if a.Bridge.TryCall("children_boxes", map[string]any{"id": id}, &cb) {
			for _, k := range cb {
				kids = append(kids, k.ID)
			}
		}
	}
	if len(kids) > 0 {
		a.Select(kids)
	}
	return true
}

func (a *NalarupaApp) handleEscapeShortcut() bool {
	if a.Canvas.textEdit != nil {
		a.Canvas.commitInlineTextEdit(a)
		return true
	}
	if a.Canvas.frameRename != nil {
		a.Canvas.frameRename = nil
		a.CanvasHandle.Focus()
		return true
	}
	if a.Canvas.pen != nil {
		if a.Canvas.pen.activeEnd != nil {
			a.Canvas.pen.activeEnd = nil
			return true
		}
		if len(a.Canvas.pen.sel) > 0 {
			a.Canvas.pen.sel = map[*Anchor]bool{}
			return true
		}
		if a.Tool == "pen" {
			a.Tool = "move"
			a.Canvas.pen.sub = "move"
			return true
		}
		a.Canvas.exitPenEdit(a)
		return true
	}
	if a.Tool != "move" {
		a.Tool = "move"
		return true
	}
	if len(a.State.Selection) > 0 {
		a.Select([]string{})
		return true
	}
	return false
}

func (a *NalarupaApp) handleArrowShortcut(key ui.Key, shift, alt bool) bool {
	step := 1.0
	if shift {
		step = 10.0
	}
	var dx, dy float64
	switch key {
	case ui.KeyLeft:
		dx = -step
	case ui.KeyRight:
		dx = step
	case ui.KeyUp:
		dy = -step
	case ui.KeyDown:
		dy = step
	}
	if a.Canvas.pen != nil && len(a.Canvas.pen.sel) > 0 {
		for pt := range a.Canvas.pen.sel {
			pt.X += dx
			pt.Y += dy
			if pt.In != nil {
				pt.In[0] += dx
				pt.In[1] += dy
			}
			if pt.Out != nil {
				pt.Out[0] += dx
				pt.Out[1] += dy
			}
		}
		a.Canvas.commitPenContours(a)
		return true
	}
	if len(a.State.Selection) == 0 {
		return false
	}

	// Check if all selected nodes are in-flow children of an auto-layout frame along this arrow's axis
	allFlow := true
	parentLayout := ""
	var parentID string
	for i, id := range a.State.Selection {
		var nd NodeDetail
		if !a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) || !nd.Exists {
			allFlow = false
			break
		}
		pos, _ := nd.Node["layoutPosition"].(string)
		if (nd.ParentLayout != "horizontal" && nd.ParentLayout != "vertical") || pos == "absolute" {
			allFlow = false
			break
		}
		if i == 0 {
			parentLayout = nd.ParentLayout
			parentID = nd.Parent
		} else if nd.Parent != parentID {
			allFlow = false
			break
		}
	}
	if allFlow {
		isHorizKey := key == ui.KeyLeft || key == ui.KeyRight
		if (parentLayout == "horizontal" && isHorizKey) || (parentLayout == "vertical" && !isHorizKey) {
			forward := key == ui.KeyRight || key == ui.KeyDown
			// Note: in auto-layout, index 0 is first (left/top) and index N-1 is last (right/bottom);
			// core's reorder "down" moves towards index 0, "up" moves towards N-1!
			dir := "down"
			if forward {
				dir = "up"
			}
			if alt {
				if forward {
					dir = "front"
				} else {
					dir = "back"
				}
			}
			a.reorderSelection(dir)
			return true
		}
	}

	now := time.Now()
	if a.nudgeCoalesce == "" || now.Sub(a.nudgeLastAt) > 600*time.Millisecond {
		a.nudgeCoalesce = nextCoalesceID("nudge")
	}
	a.nudgeLastAt = now

	// Group selected nodes by parent so screen-space delta maps accurately into each parent's matrix!
	byParent := map[string][]string{}
	var parentOrder []string
	for _, id := range a.State.Selection {
		var nd NodeDetail
		p := "document"
		if a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) && nd.Exists && nd.Parent != "" {
			p = nd.Parent
		}
		if _, seen := byParent[p]; !seen {
			parentOrder = append(parentOrder, p)
		}
		byParent[p] = append(byParent[p], id)
	}
	for _, p := range parentOrder {
		var pm *Matrix6
		if p != "document" {
			var pbs []Box
			if a.Bridge.TryCall("boxes", map[string]any{"ids": []string{p}}, &pbs) && len(pbs) > 0 {
				pm = pbs[0].M
			}
		}
		lp := InParent(pm, dx, dy)
		var st CoreState
		if err := a.Bridge.Call("move_by", map[string]any{
			"ids":      byParent[p],
			"dx":       lp[0],
			"dy":       lp[1],
			"coalesce": a.nudgeCoalesce,
		}, &st); err == nil {
			a.State = st
			a.syncWindowTitle()
		}
	}
	return true
}

// View builds the complete Nalarupa UI frame on the main thread.
func (a *NalarupaApp) View(c *ui.Context) {
	c.SetTheme(NalarupaTheme())
	c.SetMenuPresentation(ui.PopoverMenus)
	a.Services = c.Services()
	a.drainExportDone()

	rm := EffectiveReducedMotion(c)
	if a.lastReducedMotion == nil || *a.lastReducedMotion != rm {
		a.lastReducedMotion = &rm
		a.Bridge.TryCall("reduce_motion", map[string]any{"on": rm}, nil)
	}

	root := ui.Box(c.Key("root")).Fill().Background(C.BG)
	root.Children(func() {
		switch a.Screen {
		case "present":
			a.viewPresent(c)
		case "editor":
			a.viewEditor(c)
		default:
			a.viewHome(c)
		}
		a.viewToastOverlay(c)
	})
}

func (a *NalarupaApp) viewEditor(c *ui.Context) {
	takesText := c.FocusTakesText()
	c.OnWindowShortcut(ui.Cmd, ui.KeyS, func() {
		a.SaveDocument("")
	})
	c.OnWindowShortcut(ui.Cmd|ui.Shift, ui.KeyS, func() {
		a.TriggerSaveAsDialog()
	})
	c.OnWindowShortcut(ui.Cmd, ui.KeyEnter, func() {
		a.EnterPresent()
	})

	if takesText && a.Canvas.SpaceHeld {
		a.Canvas.SpaceHeld = false
	}
	shell := ui.Column(c.Key("editor-shell")).Fill().Background(C.BG)
	shell.HandleInput(func(ev ui.InputEvent) bool {
		if !takesText && a.Canvas.textEdit == nil && a.Canvas.frameRename == nil {
			if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeySpace {
				a.Canvas.SpaceHeld = true
				a.syncGrabCursorNow()
				return true
			}
			if ev.Kind == ui.InputKeyUp && ev.Key == ui.KeySpace {
				a.Canvas.SpaceHeld = false
				a.syncGrabCursorNow()
				return true
			}
		}
		if ev.Kind == ui.InputKeyUp && ev.Key == ui.KeySpace && a.Canvas.SpaceHeld {
			a.Canvas.SpaceHeld = false
			a.syncGrabCursorNow()
			return true
		}
		if ev.Kind == ui.InputKeyDown && !takesText {
			return a.HandleEditorShortcut(ev.Mods, ev.Key)
		}
		return false
	})
	shell.Children(func() {
		a.viewToolbar(c)
		ui.Row(c.Key("editor-body")).FillWidth().Grow(1).MinHeight(0).Children(func() {
			a.viewLeftPanel(c)
			a.viewCanvas(c)
			a.viewRightPanel(c)
		})
	})
}

func (a *NalarupaApp) viewRightPanel(c *ui.Context) {
	ui.Column(c.Key("right-panel")).
		Width(RightW).
		FillHeight().
		Shrink(0).
		MinHeight(0).
		Background(C.Panel).
		BorderWidth(0, 0, 0, 1).
		BorderColor(C.Border).
		Children(func() {
			ui.Box(c.Key("right-tabs-wrap")).
				FillWidth().
				Shrink(0).
				Padding(10).
				BorderWidth(0, 0, 1, 0).
				BorderColor(C.Border).
				Children(func() {
					agentLbl := "Agent"
					if a.State.AgentRunning {
						agentLbl = "Agent •"
					}
					next, changed := SegmentedTabs(c, "right-tabs", "Workspace panels", a.RightTab, []SegOption[string]{
						{Key: "tab-design", Value: "design", Label: "Design"},
						{Key: "tab-agent", Value: "agent", Label: agentLbl},
					})
					if changed {
						a.RightTab = next
					}
				})
			if a.RightTab == "design" {
				a.viewInspector(c)
			} else {
				a.viewAgentPanel(c)
			}
		})
}

func (a *NalarupaApp) viewToastOverlay(c *ui.Context) {
	if a.Toast == nil {
		return
	}
	now := time.Now()
	if !a.Toast.Danger && !a.Toast.ExpiresAt.IsZero() && !now.Before(a.Toast.ExpiresAt) {
		a.Toast = nil
		return
	}
	leftOff := float32(16)
	if a.Screen == "editor" {
		leftOff = LeftW + 16
	}
	borderCol := C.Border
	role := ui.RoleStatus
	if a.Toast.Danger {
		borderCol = C.Danger
		role = ui.RoleAlert
	}
	toastBox := ui.Row(c.Key("toast")).
		Absolute().
		Left(leftOff).
		Bottom(16).
		MaxWidth(560).
		Padding(8, 12).
		Gap(8).
		AlignItems(ui.Center).
		Radius(8).
		Background(C.Raised).
		Border(1, borderCol).
		Shadow(0, 8, 24, 0, ui.Hex("#00000080")).
		ZIndex(200).
		Role(role).
		Transition(ToastTransition(EffectiveReducedMotion(c)))
	if !a.Toast.Danger {
		rem := time.Until(a.Toast.ExpiresAt)
		toastBox.Draw(func(p *ui.Painter, _ ui.Rect) {
			if rem > 0 {
				p.After(rem + 10*time.Millisecond)
			}
		})
	}
	toastBox.Children(func() {
		if a.Toast.Danger {
			ui.Icon(c, IconSVG("alert")).FontSize(15).TextColor(C.Danger)
		}
		ui.Text(c, a.Toast.Text).FontSize(12).TextColor(C.Text).Wrap().MaxWidth(480)
		if a.Toast.Danger {
			dismiss := IconBtn(c, "toast-dismiss", "x", "Dismiss message", 24, false)
			if dismiss.Clicked() {
				a.Toast = nil
			}
		}
	})
}
