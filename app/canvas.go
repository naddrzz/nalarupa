package main

import (
	"fmt"
	"image"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/naddrzz/nalarupa/core-go/render"
)

const (
	CanvasHandleHitPx = 7.5
	CanvasRotateHitPx = 17.5
	CanvasDragStartPx = 4.0
	CanvasSnapPx      = 5.0
)

type canvasRenderKey struct {
	version    uint64
	releaseSeq uint64
	pw         int
	ph         int
	camX       float64
	camY       float64
	zoom       float64
	dpr        float64
	sharp      bool
}

type camAnimState struct {
	active   bool
	mode     string // "zoom-anchor" | "camera"
	anchorSX float64
	anchorSY float64
	docX     float64
	docY     float64
	targetX  float64
	targetY  float64
	targetZ  float64
	lastAt   time.Time
}

type panGesture struct {
	startSX float64
	startSY float64
	startCX float64
	startCY float64
}

type marqueeGesture struct {
	startDoc Pt
	curDoc   Pt
	deep     bool
	shift    bool
	baseSel  []string
}

type drawGesture struct {
	tool     string
	parentID string
	parentM  *Matrix6
	inFlow   bool
	startDoc Pt
	curDoc   Pt
}

type moveItemStart struct {
	id           string
	origX        float64
	origY        float64
	parent       string
	parentLayout string
	parentM      *Matrix6
	box          Box
}

type moveGesture struct {
	started      bool
	begunDrag    bool
	startSX      float64
	startSY      float64
	startDoc     Pt
	curDoc       Pt
	ids          []string
	origIDs      []string
	items        []moveItemStart
	startUnion   RectD
	snapTargets  []Box
	duplicated   bool
	altLostAt    time.Time
	narrowOnUpID string
	activeDrop   *DropInfo
	floatGhost   *RectD
}

type resizeItemStart struct {
	id          string
	nodeX       float64
	nodeY       float64
	w           float64
	h           float64
	rot         float64
	flipX       bool
	flipY       bool
	bx          float64
	by          float64
	ntype       string
	parentM     *Matrix6
	parentFullM *Matrix6
	canvasPos   Pt
}

type resizeGesture struct {
	handle    string
	frame     Box
	startDoc  Pt
	items     []resizeItemStart
	begunDrag bool
}

type rotateGesture struct {
	id         string
	center     Pt
	startAngle float64
	origRot    float64
	origX      float64
	origY      float64
	w          float64
	h          float64
	flipX      bool
	flipY      bool
	bx         float64
	by         float64
	begunDrag  bool
}

type radiusGesture struct {
	id         string
	corner     int // 0=tl, 1=tr, 2=br, 3=bl
	box        Box
	origRadii  [4]float64
	allEqual   bool
	begunDrag  bool
}

type inlineTextEdit struct {
	id         string
	draft      string
	orig       string
	box        Box
	fontSize   float64
	needSelect bool
	handle     ui.Handle
}

type inlineFrameRename struct {
	id         string
	draft      string
	orig       string
	sx         float32
	sy         float32
	sw         float32
	needSelect bool
	handle     ui.Handle
}

type penOpenEnd struct {
	contour *Contour
	side    PenSide
}

type penDragGesture struct {
	kind       string // "new-handle" | "move-anchors" | "move-handle"
	anchor     *Anchor
	side       PenSide
	mirrorBoth bool
	startDoc   Pt
	origPos    map[*Anchor]Anchor
	begunDrag  bool
}

type penEditState struct {
	pathID    string
	parentID  string
	sub       string // "draw" | "move"
	contours  []*Contour
	sel       map[*Anchor]bool
	activeEnd *penOpenEnd
	hoverPos  Pt
	hasHover  bool
	drag      *penDragGesture
}

// CanvasState holds all persistent canvas rendering, gesture, overlay, and tool state.
type CanvasState struct {
	RGBA             *image.RGBA
	Bitmap           *ui.Bitmap
	PrevRGBA         *image.RGBA
	PrevBitmap       *ui.Bitmap
	PrevKey          canvasRenderKey
	OverviewRGBA     *image.RGBA
	OverviewBitmap   *ui.Bitmap
	OverviewDocRect  RectD
	OverviewVer      uint64
	syncDet          *render.DetachedRenderer
	LastKey          canvasRenderKey
	lastSubmittedKey canvasRenderKey
	SharpDirty       bool
	FastDirty        bool
	PanDirty         bool
	SettleAt         time.Time

	SpaceHeld bool
	PointerSX float64
	PointerSY float64
	Inside    bool
	HoverID   string
	Mods      ui.Modifiers

	LastDownAt time.Time
	LastDownSX float64
	LastDownSY float64

	Pan      *panGesture
	Marquee  *marqueeGesture
	drawDrag *drawGesture
	Move     *moveGesture
	Resize   *resizeGesture
	Rotate   *rotateGesture
	Radius   *radiusGesture

	Guides       []SnapGuide
	Measures     []MeasureLine
	MeasureBox   *RectD
	ActiveCursor ui.Cursor
	lastDrawRect ui.Rect

	textEdit    *inlineTextEdit
	frameRename *inlineFrameRename
	pen         *penEditState

	overlayPath ui.Path
	LastPaintMS float64

	cachedSelVer   uint64
	cachedSelIDs   []string
	cachedSelBoxes []Box
	cachedLiveVer  uint64
	cachedLive     []LiveNode
	cachedFlashVer uint64
	vsyncActive    atomic.Bool

	hasHoverCache  bool
	lastHoverSX    float64
	lastHoverSY    float64
	lastHoverVer   uint64
	lastHoverMods  ui.Modifiers
	lastHoverTool  string
	lastHoverPanX  float64
	lastHoverPanY  float64
	lastHoverZoom  float64
	lastHoverSel   []string
	lastDragStepAt    time.Time
	pendingDragEv     *ui.InputEvent
	pendingDragSlot   ui.InputEvent
	pendingDragArmed  bool
	worker            *CanvasRenderWorker
	lastRenderMS      float64
	pendingInitialFit   bool
	camAnim             camAnimState
	hoverFadeID         string
	hoverFadeStartMS    float64
	createdPulseID      string
	createdPulseStartMS float64
}

func (a *NalarupaApp) initCanvasState() {
	a.Canvas = CanvasState{
		SharpDirty:   true,
		OverviewVer:  ^uint64(0),
		ActiveCursor: ui.CursorDefault,
	}
}

func (a *NalarupaApp) resetCanvasForNewDoc() {
	a.Canvas.Pan = nil
	a.Canvas.Marquee = nil
	a.Canvas.drawDrag = nil
	a.Canvas.Move = nil
	a.Canvas.Resize = nil
	a.Canvas.Rotate = nil
	a.Canvas.Radius = nil
	a.Canvas.Guides = nil
	a.Canvas.Measures = nil
	a.Canvas.MeasureBox = nil
	a.Canvas.textEdit = nil
	a.Canvas.frameRename = nil
	a.Canvas.pen = nil
	a.Canvas.HoverID = ""
	a.Canvas.SharpDirty = true
	a.Canvas.PrevKey = canvasRenderKey{}
	a.Canvas.lastSubmittedKey = canvasRenderKey{}
	a.Canvas.OverviewDocRect = RectD{}
	a.Canvas.OverviewVer = ^uint64(0)
	a.Canvas.cachedSelBoxes = nil
	a.Canvas.cachedSelIDs = nil
	a.Canvas.cachedLive = nil
	a.Canvas.cachedFlashVer = 0
	a.Canvas.hasHoverCache = false
	a.Canvas.lastDragStepAt = time.Time{}
	a.Canvas.pendingDragEv = nil
	a.Canvas.pendingDragArmed = false
	a.Canvas.camAnim = camAnimState{}
	a.Canvas.hoverFadeID = ""
	a.Canvas.createdPulseID = ""
}

func (a *NalarupaApp) stepCameraAnim(now time.Time, reduced bool) bool {
	ca := &a.Canvas.camAnim
	if !ca.active {
		return false
	}
	if reduced {
		if ca.mode == "zoom-anchor" {
			a.Cam.Zoom = ca.targetZ
			a.Cam.X = ca.docX - ca.anchorSX/ca.targetZ
			a.Cam.Y = ca.docY - ca.anchorSY/ca.targetZ
		} else {
			a.Cam = Camera{X: ca.targetX, Y: ca.targetY, Zoom: ca.targetZ}
		}
		*ca = camAnimState{}
		a.Canvas.markSharpDirty()
		a.syncViewport()
		return false
	}

	var dt float64
	if a.Window == nil {
		// Deterministic step size for headless ui.Tester (whose Frame() runs a 20-pass settle loop on CPU).
		dt = 1.0 / 240.0
	} else {
		dt = now.Sub(ca.lastAt).Seconds()
		if dt < 1.0/144.0 {
			dt = 1.0 / 144.0
		} else if dt > 0.05 {
			dt = 0.05
		}
	}
	ca.lastAt = now

	const zoomRate = 24.0
	curLog := math.Log(max(0.02, a.Cam.Zoom))
	tgtLog := math.Log(max(0.02, ca.targetZ))
	nextLog := tgtLog + (curLog-tgtLog)*math.Exp(-zoomRate*dt)

	if ca.mode == "zoom-anchor" {
		if math.Abs(tgtLog-nextLog) < 0.0035 {
			a.Cam.Zoom = ca.targetZ
			a.Cam.X = ca.docX - ca.anchorSX/ca.targetZ
			a.Cam.Y = ca.docY - ca.anchorSY/ca.targetZ
			*ca = camAnimState{}
			a.Canvas.markSharpDirty()
			a.syncViewport()
			return false
		}
		a.Cam.Zoom = math.Exp(nextLog)
		a.Cam.X = ca.docX - ca.anchorSX/a.Cam.Zoom
		a.Cam.Y = ca.docY - ca.anchorSY/a.Cam.Zoom
		a.Canvas.hasHoverCache = false
		a.syncViewport()
		return true
	}

	// "camera" mode (smooth FitToAnimated)
	k := math.Exp(-zoomRate * dt)
	nextX := ca.targetX + (a.Cam.X-ca.targetX)*k
	nextY := ca.targetY + (a.Cam.Y-ca.targetY)*k
	if math.Abs(tgtLog-nextLog) < 0.0035 && math.Hypot(ca.targetX-nextX, ca.targetY-nextY)*ca.targetZ < 0.5 {
		a.Cam = Camera{X: ca.targetX, Y: ca.targetY, Zoom: ca.targetZ}
		*ca = camAnimState{}
		a.Canvas.markSharpDirty()
		a.syncViewport()
		return false
	}
	a.Cam.Zoom = math.Exp(nextLog)
	a.Cam.X = nextX
	a.Cam.Y = nextY
	a.Canvas.hasHoverCache = false
	a.syncViewport()
	return true
}

func (cs *CanvasState) markSharpDirty() {
	cs.SharpDirty = true
	cs.FastDirty = false
	cs.PanDirty = false
	cs.SettleAt = time.Time{}
}

func (cs *CanvasState) markFastDirty() {
	cs.FastDirty = true
	cs.SettleAt = time.Now().Add(45 * time.Millisecond)
}

func (cs *CanvasState) markPanDirty() {
	cs.PanDirty = true
}

func (a *NalarupaApp) screenToDoc(sx, sy float64) Pt {
	z := max(0.02, a.Cam.Zoom)
	return Pt{a.Cam.X + sx/z, a.Cam.Y + sy/z}
}

func (a *NalarupaApp) docToScreen(dx, dy float64) (float32, float32) {
	return float32((dx - a.Cam.X) * a.Cam.Zoom), float32((dy - a.Cam.Y) * a.Cam.Zoom)
}

func (a *NalarupaApp) docRectToScreen(r RectD) ui.Rect {
	x0, y0 := a.docToScreen(r.X, r.Y)
	x1, y1 := a.docToScreen(r.X+r.W, r.Y+r.H)
	lx, rx := min(x0, x1), max(x0, x1)
	ty, by := min(y0, y1), max(y0, y1)
	return ui.Rect{X: lx, Y: ty, W: rx - lx, H: by - ty}
}

func (a *NalarupaApp) viewCanvas(c *ui.Context) {
	spaceNow := !c.FocusTakesText() && a.Canvas.textEdit == nil && a.Canvas.frameRename == nil && c.KeyDown(ui.KeySpace)
	if a.Canvas.SpaceHeld != spaceNow {
		a.Canvas.SpaceHeld = spaceNow
		a.syncGrabCursorNow()
	}

	vp := ui.Box(c.Key("canvas")).
		FillHeight().
		Grow(1).
		MinWidth(0).
		Clip().
		Background(C.CanvasFill).
		Focusable().
		FocusRingOffset(-2).
		FocusRingColor(C.Focus).
		Label("Design canvas").
		Bind(&a.CanvasHandle).
		Cursor(a.computeCanvasCursor())

	vp.HandleInput(func(ev ui.InputEvent) bool {
		return a.handleCanvasInput(ev)
	})

	// Flush any coalesced pointer-drag move aligned to the VSync frame boundary (~16ms) before snapshot fetches.
	if a.Canvas.pendingDragEv != nil && (a.Canvas.lastDragStepAt.IsZero() || time.Since(a.Canvas.lastDragStepAt) >= 14*time.Millisecond) {
		a.flushPendingDragMove()
	}

	// Advance any smooth zoom / camera glide before building screen-projected overlays & children
	if a.Canvas.camAnim.active {
		if a.stepCameraAnim(time.Now(), EffectiveReducedMotion(c)) && a.Window != nil {
			c.AnimationFrame()
		}
	}

	// Pre-fetch current frames, selection boxes, and live agent nodes with version-keyed caches (zero JSON overhead).
	topFrames := a.ensureFramesFetched()

	if len(a.State.Selection) == 0 {
		a.Canvas.cachedSelVer = a.State.Version
		a.Canvas.cachedSelIDs = nil
		a.Canvas.cachedSelBoxes = nil
	} else if a.Canvas.cachedSelVer != a.State.Version || !slices.Equal(a.Canvas.cachedSelIDs, a.State.Selection) || a.Canvas.cachedSelBoxes == nil {
		if a.Window != nil {
			if got, ok := a.Bridge.TryBoxesDirect(a.State.Selection); ok {
				a.Canvas.cachedSelBoxes = got
				a.Canvas.cachedSelVer = a.State.Version
				a.Canvas.cachedSelIDs = append([]string(nil), a.State.Selection...)
			}
		} else {
			a.Canvas.cachedSelBoxes = a.Bridge.BoxesDirect(a.State.Selection)
			a.Canvas.cachedSelVer = a.State.Version
			a.Canvas.cachedSelIDs = append([]string(nil), a.State.Selection...)
		}
	}
	selBoxes := a.Canvas.cachedSelBoxes
	var selPainted []RectD
	for _, sb := range selBoxes {
		selPainted = append(selPainted, Painted(sb))
	}
	selUnion := UnionRects(selPainted)

	var singleFrame *Box
	if len(selBoxes) == 1 {
		cp := selBoxes[0]
		singleFrame = &cp
	} else if selUnion != nil {
		singleFrame = &Box{X: selUnion.X, Y: selUnion.Y, W: selUnion.W, H: selUnion.H}
	}

	if a.Canvas.cachedLiveVer != a.State.Version || a.Canvas.cachedLive == nil {
		if a.Window != nil {
			if got, ok := a.Bridge.TryLiveDirect(); ok {
				a.Canvas.cachedLive = got
				a.Canvas.cachedLiveVer = a.State.Version
			}
		} else {
			a.Canvas.cachedLive = a.Bridge.LiveDirect()
			a.Canvas.cachedLiveVer = a.State.Version
		}
	}
	liveNodes := a.Canvas.cachedLive

	reduced := EffectiveReducedMotion(c)
	viewNow := c.Now()
	if viewNow.IsZero() {
		viewNow = time.Now()
	}
	viewNowMS := float64(viewNow.UnixNano()) / 1e6
	a.consumeCanvasActivity(viewNowMS)

	// Resolve Alt-hover distance measurement when idle in move tool
	a.Canvas.Measures = nil
	a.Canvas.MeasureBox = nil
	if a.Tool == "move" && a.Canvas.Inside && !a.Canvas.isDraggingAny() && (c.Modifiers()&ui.Alt != 0) && selUnion != nil {
		a.computeAltMeasures(*selUnion)
	}

	vp.Draw(func(p *ui.Painter, r ui.Rect) {
		if r.W > 0 && r.H > 0 {
			a.Canvas.lastDrawRect = r
			SyncNativeCanvasCursor(a.Window, a.nativeGrabCursorMode(), r)
			newW, newH := float64(r.W), float64(r.H)
			sizeChanged := math.Abs(newW-a.CanvasW) > 0.5 || math.Abs(newH-a.CanvasH) > 0.5
			if sizeChanged {
				a.CanvasW = newW
				a.CanvasH = newH
				a.syncViewport()
				a.Store.ViewCenter = Pt{
					a.Cam.X + (a.CanvasW/2)/max(0.02, a.Cam.Zoom),
					a.Cam.Y + (a.CanvasH/2)/max(0.02, a.Cam.Zoom),
				}
				a.Store.ViewZoom = a.Cam.Zoom
			}
			if a.Canvas.pendingInitialFit {
				a.Canvas.pendingInitialFit = false
				if sizeChanged {
					a.FitTo(nil)
				}
			}
		}

		dpr := EffectiveDPR(p.Scale())
		pw := max(1, int(math.Round(float64(r.W)*dpr)))
		ph := max(1, int(math.Round(float64(r.H)*dpr)))

		paintNow := p.Now()
		if paintNow.IsZero() {
			paintNow = time.Now()
		}
		nowMS := float64(paintNow.UnixNano()) / 1e6

		now := time.Now()
		if !a.Canvas.camAnim.active && !a.Canvas.SettleAt.IsZero() && !now.Before(a.Canvas.SettleAt) {
			a.Canvas.SharpDirty = true
			a.Canvas.FastDirty = false
			a.Canvas.SettleAt = time.Time{}
		}

		if a.Canvas.worker != nil {
			oldKey := a.Canvas.LastKey
			if peekKey, hasReady := a.Canvas.worker.PeekReadyKey(); hasReady &&
				oldKey.zoom > 0 && peekKey.zoom > 0 && a.Canvas.RGBA != nil && oldKey.pw > 0 && oldKey.ph > 0 &&
				max(peekKey.zoom/oldKey.zoom, oldKey.zoom/peekKey.zoom) > 1.06 {
				a.Canvas.PrevRGBA, a.Canvas.RGBA = a.Canvas.RGBA, a.Canvas.PrevRGBA
				a.Canvas.PrevKey = oldKey
				if a.Canvas.PrevBitmap == nil {
					a.Canvas.PrevBitmap = ui.NewBitmap(a.Canvas.PrevRGBA)
				} else {
					a.Canvas.PrevBitmap.Update(a.Canvas.PrevRGBA)
				}
			}
			if doneKey, ms, ok := a.Canvas.worker.TakeReady(&a.Canvas.RGBA); ok {
				if a.Canvas.Bitmap == nil {
					a.Canvas.Bitmap = ui.NewBitmap(a.Canvas.RGBA)
				} else {
					a.Canvas.Bitmap.Update(a.Canvas.RGBA)
				}
				a.Canvas.LastKey = doneKey
				a.Canvas.lastRenderMS = ms
			}
			if ovRect, ovVer, hasImg, ok := a.Canvas.worker.TakeOverviewReady(&a.Canvas.OverviewRGBA); ok {
				if hasImg && a.Canvas.OverviewRGBA != nil {
					if a.Canvas.OverviewBitmap == nil {
						a.Canvas.OverviewBitmap = ui.NewBitmap(a.Canvas.OverviewRGBA)
					} else {
						a.Canvas.OverviewBitmap.Update(a.Canvas.OverviewRGBA)
					}
					a.Canvas.OverviewDocRect = ovRect
				} else {
					a.Canvas.OverviewDocRect = RectD{}
				}
				a.Canvas.OverviewVer = ovVer
			}
		}

		engVer := max(a.State.Version, a.Bridge.VersionDirect())
		curFrames := topFrames
		if a.Layers.CachedFramesVer != engVer {
			if got, ok := a.Bridge.TryFramesDirect(); ok {
				a.Layers.CachedFrames = got
				a.Layers.CachedFramesVer = engVer
				curFrames = got
			}
		} else if a.Layers.CachedFrames != nil {
			curFrames = a.Layers.CachedFrames
		}
		curLive := liveNodes
		if a.Canvas.cachedLiveVer != engVer {
			if got, ok := a.Bridge.TryLiveDirect(); ok {
				a.Canvas.cachedLive = got
				a.Canvas.cachedLiveVer = engVer
				curLive = got
			}
		} else if a.Canvas.cachedLive != nil {
			curLive = a.Canvas.cachedLive
		}

		// Step cursor physics & release any newly-arrived slide/edit targets BEFORE snapshotting the canvas scene!
		prevPaintMS := a.stepAgentChoreography(nowMS, reduced, curLive)

		hasSlides := a.Bridge.HasSlidesDirect()
		camMoving := a.Canvas.camAnim.active || a.Canvas.Pan != nil
		interactiveDrag := a.Canvas.Move != nil || a.Canvas.Resize != nil || a.Canvas.Rotate != nil || a.Canvas.Radius != nil
		agentBusy := a.State.AgentRunning || a.State.Executing || a.State.PenDrawing
		sharp := !a.Canvas.FastDirty && !hasSlides && !camMoving && !interactiveDrag && !agentBusy

		// Overscan margin around the visible viewport:
		// 28% per side while idle/navigating so pan & zoom-out immediately show crisp surrounding pixels;
		// 10% while slides/agent stream actively so per-frame raster stays ultra-lightweight (<8ms).
		padRatio := 0.28
		if hasSlides || agentBusy || interactiveDrag {
			padRatio = 0.10
		}
		padX := int(math.Round(float64(pw) * padRatio))
		padY := int(math.Round(float64(ph) * padRatio))
		rw := min(4096, max(1, pw+2*padX))
		rh := min(4096, max(1, ph+2*padY))
		effPadX := float64(rw-pw) * 0.5
		effPadY := float64(rh-ph) * 0.5
		// Pre-rasterize directly at the target zoom during smooth zoom animations so the GPU downscales
		// a supersampled crisp vector bitmap during the glide and arrives at 100% sharpness with zero settle delay.
		renderCam := a.Cam
		if ca := a.Canvas.camAnim; ca.active {
			endZ := max(0.02, ca.targetZ)
			endX, endY := ca.targetX, ca.targetY
			if ca.mode == "zoom-anchor" {
				endX = ca.docX - ca.anchorSX/endZ
				endY = ca.docY - ca.anchorSY/endZ
			}
			if endZ <= a.Cam.Zoom || endZ/max(0.01, a.Cam.Zoom) <= 2.4 {
				renderCam = Camera{X: endX, Y: endY, Zoom: endZ}
			} else {
				// On huge multi-octave zoom-ins (e.g. 15% -> 135%), step crisp intermediate bands on the way in.
				baseZ := max(a.Cam.Zoom, a.Canvas.LastKey.zoom)
				stepZ := min(endZ, max(0.02, baseZ*1.75))
				if ca.mode == "zoom-anchor" {
					renderCam = Camera{X: ca.docX - ca.anchorSX/stepZ, Y: ca.docY - ca.anchorSY/stepZ, Zoom: stepZ}
				} else {
					renderCam = Camera{X: a.Cam.X, Y: a.Cam.Y, Zoom: stepZ}
				}
			}
		}

		devZoom := max(0.01, renderCam.Zoom*dpr)
		rcamX := renderCam.X - effPadX/devZoom
		rcamY := renderCam.Y - effPadY/devZoom

		curRelSeq := a.Bridge.SlideReleaseSeqDirect()
		rk := canvasRenderKey{
			version:    engVer,
			releaseSeq: curRelSeq,
			pw:         rw,
			ph:         rh,
			camX:       rcamX,
			camY:       rcamY,
			zoom:       renderCam.Zoom,
			dpr:        dpr,
			sharp:      sharp,
		}
		wantOverview := !interactiveDrag && !hasSlides && a.Canvas.OverviewVer != engVer

		lk := a.Canvas.LastKey
		workerFree := a.Canvas.worker == nil || !a.Canvas.worker.Busy()
		zoomChanged := lk.zoom <= 0 || math.Abs(lk.zoom-rk.zoom)/max(0.01, rk.zoom) > 0.004
		needRender := false
		switch {
		case a.Canvas.Bitmap == nil || lk.version != engVer || lk.releaseSeq != curRelSeq:
			needRender = true
		case zoomChanged:
			// Always enqueue the latest target zoom immediately (single-slot coalesced on worker) so zoom-in
			// and zoom-out stay crisp under every condition — even while slides or parallel agents are active!
			needRender = a.Window == nil || workerFree || a.Canvas.lastSubmittedKey != rk
		case hasSlides:
			// Submit next slide frame only when worker has capacity so we never pile up redundant rasters.
			needRender = workerFree
		case a.Canvas.camAnim.active:
			panShiftPx := math.Hypot(lk.camX-rk.camX, lk.camY-rk.camY) * devZoom
			needRender = panShiftPx > float64(pw)*0.16 && workerFree
		case a.Canvas.Pan != nil:
			// During interactive pan, re-center the overscan window when the viewport nears its inner 55% margin.
			lkDPR := lk.dpr
			if lkDPR <= 0 {
				lkDPR = dpr
			}
			lkDocW := float64(lk.pw) / max(0.01, lk.zoom*lkDPR)
			lkDocH := float64(lk.ph) / max(0.01, lk.zoom*lkDPR)
			vpDocW := float64(pw) / max(0.01, a.Cam.Zoom*dpr)
			vpDocH := float64(ph) / max(0.01, a.Cam.Zoom*dpr)
			guardX := max(0, (lkDocW-vpDocW)*0.22)
			guardY := max(0, (lkDocH-vpDocH)*0.22)
			outsideGuard := a.Cam.X < lk.camX+guardX || a.Cam.Y < lk.camY+guardY ||
				a.Cam.X+vpDocW > lk.camX+lkDocW-guardX || a.Cam.Y+vpDocH > lk.camY+lkDocH-guardY
			needRender = outsideGuard || math.Abs(lk.zoom-a.Cam.Zoom) > 1e-6
		default:
			needRender = a.Canvas.SharpDirty || a.Canvas.FastDirty || a.Canvas.PanDirty || lk != rk || wantOverview
		}

		if needRender {
			if a.Window != nil && a.Canvas.Bitmap != nil && a.Canvas.worker != nil {
				a.Canvas.lastSubmittedKey = rk
				a.Canvas.worker.Submit(canvasRenderJob{
					key:          rk,
					w:            rw,
					h:            rh,
					camX:         rcamX,
					camY:         rcamY,
					zoom:         devZoom,
					bg:           CanvasBG,
					sharp:        sharp,
					wantOverview: wantOverview,
				})
			} else {
				t0 := time.Now()
				if a.Canvas.syncDet == nil {
					a.Canvas.syncDet = render.NewDetachedRenderer()
				}
				ovRect, ovVer, ovOK := a.Bridge.RenderDetachedWithOverview(
					a.Canvas.syncDet, &a.Canvas.RGBA, rw, rh, rcamX, rcamY, devZoom,
					CanvasBG, "", sharp, wantOverview, &a.Canvas.OverviewRGBA, 1664,
				)
				if a.Canvas.Bitmap == nil {
					a.Canvas.Bitmap = ui.NewBitmap(a.Canvas.RGBA)
				} else {
					a.Canvas.Bitmap.Update(a.Canvas.RGBA)
				}
				if wantOverview && !hasSlides {
					if ovOK && a.Canvas.OverviewRGBA != nil {
						if a.Canvas.OverviewBitmap == nil {
							a.Canvas.OverviewBitmap = ui.NewBitmap(a.Canvas.OverviewRGBA)
						} else {
							a.Canvas.OverviewBitmap.Update(a.Canvas.OverviewRGBA)
						}
						a.Canvas.OverviewDocRect = ovRect
					} else {
						a.Canvas.OverviewDocRect = RectD{}
					}
					a.Canvas.OverviewVer = ovVer
				}
				a.Canvas.LastKey = rk
				a.Canvas.lastSubmittedKey = rk
				a.Canvas.lastRenderMS = float64(time.Since(t0).Microseconds()) / 1000.0
			}
			a.Canvas.SharpDirty = false
			a.Canvas.FastDirty = false
			a.Canvas.PanDirty = false
		}
		if a.Canvas.Bitmap != nil || a.Canvas.PrevBitmap != nil || a.Canvas.OverviewBitmap != nil {
			lk = a.Canvas.LastKey
			lkDPR := lk.dpr
			if lkDPR <= 0 {
				lkDPR = dpr
			}
			p.Clip(r, 0, func() {
				p.Fill(r, ui.RGB(CanvasBG[0], CanvasBG[1], CanvasBG[2]), 0)
				// 1. Whole-document overview backdrop (prevents any blank border pop-in during extreme zoom-out / fast pan)
				if a.Canvas.OverviewBitmap != nil && a.Canvas.OverviewDocRect.W > 0 && a.Canvas.OverviewDocRect.H > 0 {
					ov := a.Canvas.OverviewDocRect
					ovDst := ui.Rect{
						X: r.X + float32((ov.X-a.Cam.X)*a.Cam.Zoom),
						Y: r.Y + float32((ov.Y-a.Cam.Y)*a.Cam.Zoom),
						W: float32(ov.W * a.Cam.Zoom),
						H: float32(ov.H * a.Cam.Zoom),
					}
					p.Image(a.Canvas.OverviewBitmap, ovDst, ui.FillBox)
				}
				// 2. Previous-zoom viewport layer (bridges wide outer periphery seamlessly while target-zoom bitmap glides in)
				pk := a.Canvas.PrevKey
				if a.Canvas.PrevBitmap != nil && pk.version == lk.version && pk.zoom > 0 && pk.pw > 0 && pk.ph > 0 && pk.zoom < lk.zoom {
					pkDPR := pk.dpr
					if pkDPR <= 0 {
						pkDPR = dpr
					}
					pScale := a.Cam.Zoom / pk.zoom
					pbw := float32(float64(pk.pw)/pkDPR) * float32(pScale)
					pbh := float32(float64(pk.ph)/pkDPR) * float32(pScale)
					pDst := ui.Rect{
						X: r.X + float32((pk.camX-a.Cam.X)*a.Cam.Zoom),
						Y: r.Y + float32((pk.camY-a.Cam.Y)*a.Cam.Zoom),
						W: pbw,
						H: pbh,
					}
					p.Image(a.Canvas.PrevBitmap, pDst, ui.FillBox)
				}
				// 3. Overscanned crisp native-zoom viewport detail layer
				if a.Canvas.Bitmap != nil && lk.zoom > 0 && lk.pw > 0 && lk.ph > 0 {
					scale := a.Cam.Zoom / lk.zoom
					bw := float32(float64(lk.pw)/lkDPR) * float32(scale)
					bh := float32(float64(lk.ph)/lkDPR) * float32(scale)
					dstRect := ui.Rect{
						X: r.X + float32((lk.camX-a.Cam.X)*a.Cam.Zoom),
						Y: r.Y + float32((lk.camY-a.Cam.Y)*a.Cam.Zoom),
						W: bw,
						H: bh,
					}
					p.Image(a.Canvas.Bitmap, dstRect, ui.FillBox)
				}
			})
		}

		wantOverlayVSync := false
		wantAgentVSync := false
		p.Clip(r, 0, func() {
			wantOverlayVSync = a.drawCanvasOverlays(p, r, curFrames, selBoxes, singleFrame, reduced, nowMS)
			wantAgentVSync = a.drawAgentLayer(p, r, curLive, reduced, nowMS, prevPaintMS, engVer)
		})

		workerPendingZoom := a.Canvas.worker != nil && (a.Canvas.worker.Busy() || a.Canvas.worker.HasReady() ||
			math.Abs(a.Canvas.LastKey.zoom-a.Cam.Zoom)/max(0.01, a.Cam.Zoom) > 0.004)
		wantVSync := a.Canvas.camAnim.active || a.Canvas.pendingDragEv != nil || hasSlides || wantOverlayVSync || wantAgentVSync || workerPendingZoom
		a.Canvas.vsyncActive.Store(wantVSync)
		if wantVSync {
			p.AnimationFrame()
		}
		if !a.Canvas.camAnim.active && !a.Canvas.SettleAt.IsZero() {
			p.After(time.Until(a.Canvas.SettleAt) + 5*time.Millisecond)
		}
	})

	vp.Children(func() {
		a.buildCanvasChildren(c, topFrames, selBoxes, singleFrame, liveNodes, viewNowMS)
	})
}

func (cs *CanvasState) isDraggingAny() bool {
	return cs.Pan != nil || cs.Marquee != nil || cs.drawDrag != nil || cs.Move != nil || cs.Resize != nil || cs.Rotate != nil || cs.Radius != nil || (cs.pen != nil && cs.pen.drag != nil)
}

func (a *NalarupaApp) nativeGrabCursorMode() int {
	if a.Canvas.Pan != nil || a.Canvas.Rotate != nil {
		return 2
	}
	if a.Tool == "hand" || a.Canvas.SpaceHeld {
		return 1
	}
	return 0
}

func (a *NalarupaApp) syncGrabCursorNow() {
	if a.Screen != "editor" {
		SyncNativeCanvasCursor(a.Window, 0, a.Canvas.lastDrawRect)
		return
	}
	SyncNativeCanvasCursor(a.Window, a.nativeGrabCursorMode(), a.Canvas.lastDrawRect)
}

func (a *NalarupaApp) computeCanvasCursor() ui.Cursor {
	if a.Canvas.Pan != nil {
		return ui.CursorGrabbing
	}
	if a.Tool == "hand" || a.Canvas.SpaceHeld {
		return ui.CursorGrab
	}
	switch a.Tool {
	case "frame", "rectangle", "ellipse", "pen":
		return ui.CursorCrosshair
	case "text":
		return ui.CursorText
	}
	if a.Canvas.Resize != nil {
		return ResizeUICursor(a.Canvas.Resize.handle, a.Canvas.Resize.frame.M)
	}
	if a.Canvas.Rotate != nil {
		return ui.CursorGrabbing
	}
	if a.Canvas.ActiveCursor != ui.CursorDefault {
		return a.Canvas.ActiveCursor
	}
	return ui.CursorDefault
}

func (a *NalarupaApp) computeAltMeasures(selUnion RectD) {
	docPt := a.screenToDoc(a.Canvas.PointerSX, a.Canvas.PointerSY)
	hitChain := a.Bridge.HitDirect(docPt[0], docPt[1])
	targetID := ""
	for i := len(hitChain) - 1; i >= 0; i-- {
		cand := hitChain[i]
		if !slices.Contains(a.State.Selection, cand) {
			targetID = cand
			break
		}
	}
	if targetID == "" && len(a.State.Selection) > 0 {
		if parent := a.Bridge.NodeParentDirect(a.State.Selection[0]); parent != "" && parent != "document" {
			targetID = parent
		}
	}
	if targetID == "" {
		return
	}
	tbs := a.Bridge.BoxesDirect([]string{targetID})
	if len(tbs) == 0 {
		return
	}
	tb := Painted(tbs[0])
	a.Canvas.Measures = Measure(selUnion, tb)
	if len(a.Canvas.Measures) > 0 {
		a.Canvas.MeasureBox = &tb
	}
}

func (a *NalarupaApp) drawCanvasOverlays(p *ui.Painter, vpRect ui.Rect, topFrames, selBoxes []Box, singleFrame *Box, reduced bool, nowMS float64) bool {
	needOverlayAnim := false
	ox, oy := vpRect.X, vpRect.Y
	toWin := func(dx, dy float64) (float32, float32) {
		sx, sy := a.docToScreen(dx, dy)
		return ox + sx, oy + sy
	}
	toWinRect := func(r RectD) ui.Rect {
		sr := a.docRectToScreen(r)
		sr.X += ox
		sr.Y += oy
		return sr
	}
	drawBoxOutline := func(b Box, col ui.Color, width float32) {
		if Turned(b.M) {
			cs := RectCorners(b.RectOf(), b.M)
			a.Canvas.overlayPath.Reset()
			x0, y0 := toWin(cs[0][0], cs[0][1])
			a.Canvas.overlayPath.MoveTo(x0, y0)
			for i := 1; i < 4; i++ {
				xi, yi := toWin(cs[i][0], cs[i][1])
				a.Canvas.overlayPath.LineTo(xi, yi)
			}
			a.Canvas.overlayPath.Close()
			p.StrokePath(&a.Canvas.overlayPath, width, col)
		} else {
			p.Stroke(toWinRect(Painted(b)), col, 0, width)
		}
	}

	// 0. Crisp studio hairline boundary around unselected top-level frames so white artboards stay distinct on clean white canvas
	for _, tf := range topFrames {
		if tf.Type != "frame" || tf.Placeholder || slices.Contains(a.State.Selection, tf.ID) || tf.ID == a.Canvas.HoverID || tf.ID == a.HoverLayerID {
			continue
		}
		pb := Painted(tf)
		sr := a.docRectToScreen(pb)
		if sr.X+sr.W < -2 || sr.X > vpRect.W+2 || sr.Y+sr.H < -2 || sr.Y > vpRect.H+2 {
			continue
		}
		drawBoxOutline(tf, C.CanvasFrameEdge, 1)
	}

	// 1. Canvas hover outline (120ms EaseOutQuart fade-in when sweeping onto a new node)
	if a.Canvas.HoverID != "" && !a.Canvas.isDraggingAny() && !slices.Contains(a.State.Selection, a.Canvas.HoverID) {
		if a.Canvas.hoverFadeID != a.Canvas.HoverID {
			a.Canvas.hoverFadeID = a.Canvas.HoverID
			a.Canvas.hoverFadeStartMS = nowMS
		}
		hoverAlpha := float32(1.0)
		if !reduced {
			el := nowMS - a.Canvas.hoverFadeStartMS
			if el >= 0 && el < 120.0 {
				hoverAlpha = float32(0.30 + 0.70*EaseOutQuart64(el/120.0))
				needOverlayAnim = true
			}
		}
		hoverCol := C.Select
		hoverCol.A = uint8(math.Round(float64(hoverCol.A) * float64(hoverAlpha)))
		if a.Window != nil {
			if hbs, ok := a.Bridge.TryBoxesDirect([]string{a.Canvas.HoverID}); ok && len(hbs) > 0 {
				drawBoxOutline(hbs[0], hoverCol, 1)
			}
		} else if hbs := a.Bridge.BoxesDirect([]string{a.Canvas.HoverID}); len(hbs) > 0 {
			drawBoxOutline(hbs[0], hoverCol, 1)
		}
	} else {
		a.Canvas.hoverFadeID = ""
	}

	// 2. Layers-panel hover outline
	if a.HoverLayerID != "" {
		if a.Window != nil {
			if lbs, ok := a.Bridge.TryBoxesDirect([]string{a.HoverLayerID}); ok && len(lbs) > 0 {
				drawBoxOutline(lbs[0], C.Select, 2)
			}
		} else if lbs := a.Bridge.BoxesDirect([]string{a.HoverLayerID}); len(lbs) > 0 {
			drawBoxOutline(lbs[0], C.Select, 2)
		}
	}

	// 3. Selection outlines & handles (suppressed during pen vector editing)
	if a.Canvas.pen == nil && len(selBoxes) > 0 {
		for _, sb := range selBoxes {
			drawBoxOutline(sb, C.Select, 1.5)
		}
		if len(selBoxes) > 1 && singleFrame != nil {
			p.Stroke(toWinRect(singleFrame.RectOf()), C.Select, 0, 1)
		}

		// 8 Resize handles + 4 Corner-radius handles
		if a.Tool == "move" && a.Canvas.textEdit == nil && singleFrame != nil && (a.Canvas.Move == nil || a.Canvas.Move.floatGhost == nil) {
			paintRect := toWinRect(Painted(*singleFrame))
			if paintRect.W >= 10 || paintRect.H >= 10 {
				for _, hp := range a.resizeHandleScreenPoints(*singleFrame) {
					hr := ui.Rect{X: ox + hp.sx - 4, Y: oy + hp.sy - 4, W: 8, H: 8}
					p.Fill(hr, ui.Hex("#FFFFFF"), 1)
					p.Stroke(hr, C.Select, 1, 1)
				}
				// Corner-radius handles on single non-instance frame/rectangle > 100x100 screen px
				if len(selBoxes) == 1 && !strings.Contains(selBoxes[0].ID, "/") &&
					(selBoxes[0].Type == "rectangle" || selBoxes[0].Type == "frame") &&
					paintRect.W > 100 && paintRect.H > 100 {
					radii, allEq := a.nodeCornerRadii(selBoxes[0].ID)
					for ci, rp := range a.radiusHandleScreenPoints(selBoxes[0], radii) {
						rr := ui.Rect{X: ox + rp[0] - 5.5, Y: oy + rp[1] - 5.5, W: 11, H: 11}
						fillCol := ui.Hex("#FFFFFF")
						if a.Canvas.Radius != nil && a.Canvas.Radius.corner == ci {
							fillCol = C.Select
						}
						p.Fill(rr, fillCol, 5.5)
						p.Stroke(rr, C.Select, 5.5, 1)
						if !allEq {
							p.Fill(ui.Rect{X: ox + rp[0] - 1.5, Y: oy + rp[1] - 1.5, W: 3, H: 3}, C.Select, 1.5)
						}
					}
				}
			}
		}
	}

	// 4. Auto-layout reorder float ghost & external drop target indicator
	if a.Canvas.Move != nil {
		if a.Canvas.Move.floatGhost != nil {
			p.Stroke(toWinRect(*a.Canvas.Move.floatGhost), C.Select, 0, 1)
		}
		if dt := a.Canvas.Move.activeDrop; dt != nil && dt.ID != "" && dt.ID != "document" {
			db := Box{ID: dt.ID, X: dt.X, Y: dt.Y, W: dt.W, H: dt.H, M: dt.M}
			drawBoxOutline(db, C.Select, 3)
			if dt.Line != nil {
				x1, y1 := toWin(dt.Line[0], dt.Line[1])
				x2, y2 := toWin(dt.Line[2], dt.Line[3])
				a.Canvas.overlayPath.Reset()
				a.Canvas.overlayPath.MoveTo(x1, y1).LineTo(x2, y2).Dash(0, 8, 6)
				p.StrokePath(&a.Canvas.overlayPath, 2, C.Select)
			}
		}
	}

	// 5. Snap guides
	for _, g := range a.Canvas.Guides {
		if g.Axis == 0 {
			x, y1 := toWin(g.Pos, g.From)
			_, y2 := toWin(g.Pos, g.To)
			p.Line(x, y1, x, y2, 1, C.Snap)
			for _, m := range g.Marks {
				mx, my := toWin(g.Pos, m)
				if snapCrossSVG != nil {
					p.Icon(snapCrossSVG, ui.Rect{X: mx - 4, Y: my - 4, W: 8, H: 8}, C.Snap)
				}
			}
		} else {
			x1, y := toWin(g.From, g.Pos)
			x2, _ := toWin(g.To, g.Pos)
			p.Line(x1, y, x2, y, 1, C.Snap)
			for _, m := range g.Marks {
				mx, my := toWin(m, g.Pos)
				if snapCrossSVG != nil {
					p.Icon(snapCrossSVG, ui.Rect{X: mx - 4, Y: my - 4, W: 8, H: 8}, C.Snap)
				}
			}
		}
	}

	// 6. Alt-hover distance measurement lines & pills
	if a.Canvas.MeasureBox != nil {
		p.Stroke(toWinRect(*a.Canvas.MeasureBox), C.Snap, 0, 1)
	}
	for _, ml := range a.Canvas.Measures {
		x1, y1 := toWin(ml.X1, ml.Y1)
		x2, y2 := toWin(ml.X2, ml.Y2)
		p.Line(x1, y1, x2, y2, 1, C.Snap)
		if ml.HasExt {
			ex1, ey1 := toWin(ml.Ext[0], ml.Ext[1])
			ex2, ey2 := toWin(ml.Ext[2], ml.Ext[3])
			a.Canvas.overlayPath.Reset()
			a.Canvas.overlayPath.MoveTo(ex1, ey1).LineTo(ex2, ey2).Dash(0, 3, 3)
			p.StrokePath(&a.Canvas.overlayPath, 1, C.Snap)
		}
		lbl := formatNum(ml.Len)
		tw := float32(len(lbl)*7 + 10)
		mx, my := (x1+x2)/2, (y1+y2)/2
		pill := ui.Rect{X: mx - tw/2, Y: my - 8, W: tw, H: 16}
		p.Fill(pill, C.SnapLabel, 4)
		p.Text(pill.X+5, pill.Y+2, lbl, 11, ui.Hex("#FFFFFF"))
	}

	// 7. Marquee selection box
	if mq := a.Canvas.Marquee; mq != nil {
		r := RectD{
			X: min(mq.startDoc[0], mq.curDoc[0]),
			Y: min(mq.startDoc[1], mq.curDoc[1]),
			W: math.Abs(mq.curDoc[0] - mq.startDoc[0]),
			H: math.Abs(mq.curDoc[1] - mq.startDoc[1]),
		}
		wr := toWinRect(r)
		p.Fill(wr, ui.Hex("#3B82F61A"), 0)
		p.Stroke(wr, C.Select, 0, 1)
	}

	// 8. Draw creation ghost (with spatially-continuous corner handles + live W x H pill)
	if dg := a.Canvas.drawDrag; dg != nil {
		docW := math.Abs(dg.curDoc[0] - dg.startDoc[0])
		docH := math.Abs(dg.curDoc[1] - dg.startDoc[1])
		r := RectD{
			X: min(dg.startDoc[0], dg.curDoc[0]),
			Y: min(dg.startDoc[1], dg.curDoc[1]),
			W: docW,
			H: docH,
		}
		wr := toWinRect(r)
		rad := float32(0)
		if dg.tool == "ellipse" {
			rad = min(wr.W, wr.H) / 2
		}
		p.Fill(wr, ui.Hex("#3B82F61E"), rad)
		p.Stroke(wr, C.Select, rad, 1.5)
		if wr.W >= 8 || wr.H >= 8 {
			for _, pt := range [4][2]float32{
				{wr.X, wr.Y},
				{wr.X + wr.W, wr.Y},
				{wr.X + wr.W, wr.Y + wr.H},
				{wr.X, wr.Y + wr.H},
			} {
				hr := ui.Rect{X: pt[0] - 3.5, Y: pt[1] - 3.5, W: 7, H: 7}
				p.Fill(hr, ui.Hex("#FFFFFF"), 1)
				p.Stroke(hr, C.Select, 1, 1)
			}
			sizeStr := fmt.Sprintf("%s × %s", formatNum(math.Round(docW)), formatNum(math.Round(docH)))
			pillW := float32(len(sizeStr)*6 + 14)
			pill := ui.Rect{X: wr.X + wr.W/2 - pillW/2, Y: wr.Y + wr.H + 6, W: pillW, H: 18}
			p.Fill(pill, C.SelectSolid, 4)
			p.Text(pill.X+7, pill.Y+3, sizeStr, 10, ui.Hex("#FFFFFF"))
		}
	}

	// 8b. Newly-created shape 180ms EaseOutQuint settle ring (tactile confirmation on mouse release, driven by VSync nowMS)
	if !reduced && a.Canvas.createdPulseID != "" {
		if a.Canvas.createdPulseStartMS <= 0 {
			a.Canvas.createdPulseStartMS = nowMS
		}
		elMS := nowMS - a.Canvas.createdPulseStartMS
		if elMS >= 0 && elMS < 180.0 {
			needOverlayAnim = true
			if singleFrame != nil && len(a.State.Selection) == 1 && a.State.Selection[0] == a.Canvas.createdPulseID {
				u := elMS / 180.0
				e := EaseOutQuint64(u)
				pad := float32((1.0 - e) * 6.0)
				sr := toWinRect(Painted(*singleFrame))
				pr := ui.Rect{X: sr.X - pad, Y: sr.Y - pad, W: sr.W + pad*2, H: sr.H + pad*2}
				p.Stroke(pr, ui.RGBA(59, 130, 246, float32((1.0-u)*0.65)), 2, 1.5)
			}
		} else {
			a.Canvas.createdPulseID = ""
			a.Canvas.createdPulseStartMS = 0
		}
	}

	// 9. Vector Pen contour & handle overlay
	if ps := a.Canvas.pen; ps != nil {
		a.drawPenOverlay(p, ox, oy, ps)
	}
	return needOverlayAnim
}

func (a *NalarupaApp) drawPenOverlay(p *ui.Painter, ox, oy float32, ps *penEditState) {
	toWin := func(pt Pt) (float32, float32) {
		sx, sy := a.docToScreen(pt[0], pt[1])
		return ox + sx, oy + sy
	}
	// Contours
	a.Canvas.overlayPath.Reset()
	for _, c := range ps.contours {
		if len(c.Anchors) == 0 {
			continue
		}
		x0, y0 := toWin(c.Anchors[0].Pos())
		a.Canvas.overlayPath.MoveTo(x0, y0)
		for _, seg := range ContourSegments([]*Contour{c}) {
			x3, y3 := toWin(seg.To.Pos())
			if segCurved(seg.From, seg.To) {
				bez := segBezier(seg.From, seg.To)
				x1, y1 := toWin(bez[1])
				x2, y2 := toWin(bez[2])
				a.Canvas.overlayPath.CubeTo(x1, y1, x2, y2, x3, y3)
			} else {
				a.Canvas.overlayPath.LineTo(x3, y3)
			}
		}
	}
	p.StrokePath(&a.Canvas.overlayPath, 1, C.CanvasLabel)

	// Rubber-band preview in draw submode
	if ps.sub == "draw" && ps.activeEnd != nil && ps.hasHover && ps.drag == nil {
		endAnchor := EndOfContour(ps.activeEnd.contour, ps.activeEnd.side)
		if endAnchor != nil {
			x0, y0 := toWin(endAnchor.Pos())
			x1, y1 := toWin(ps.hoverPos)
			a.Canvas.overlayPath.Reset()
			a.Canvas.overlayPath.MoveTo(x0, y0)
			if h := endAnchor.Handle(ps.activeEnd.side); h != nil {
				hx, hy := toWin(*h)
				a.Canvas.overlayPath.CubeTo(hx, hy, x1, y1, x1, y1)
			} else {
				a.Canvas.overlayPath.LineTo(x1, y1)
			}
			p.StrokePath(&a.Canvas.overlayPath, 1, C.PenBlue)
		}
	}

	// Handles
	shown := ShownHandles(ps.contours, ps.sel)
	for _, c := range ps.contours {
		for _, anc := range c.Anchors {
			if !shown[anc] {
				continue
			}
			ax, ay := toWin(anc.Pos())
			for _, side := range [2]PenSide{PenIn, PenOut} {
				h := anc.Handle(side)
				if h == nil {
					continue
				}
				hx, hy := toWin(*h)
				p.Line(ax, ay, hx, hy, 1, C.PenBlue)
				// Diamond knob
				const k = float32(PenHandlePx) / 2
				a.Canvas.overlayPath.Reset()
				a.Canvas.overlayPath.MoveTo(hx, hy-k).LineTo(hx+k, hy).LineTo(hx, hy+k).LineTo(hx-k, hy).Close()
				active := ps.drag != nil && ps.drag.kind == "move-handle" && ps.drag.anchor == anc && ps.drag.side == side
				if active {
					p.FillPath(&a.Canvas.overlayPath, C.PenBlue)
					p.StrokePath(&a.Canvas.overlayPath, 1, ui.Hex("#FFFFFF"))
				} else {
					p.FillPath(&a.Canvas.overlayPath, ui.Hex("#FFFFFF"))
					p.StrokePath(&a.Canvas.overlayPath, 1, C.PenBlue)
				}
			}
		}
	}

	// Circular anchors
	for _, c := range ps.contours {
		for _, anc := range c.Anchors {
			ax, ay := toWin(anc.Pos())
			ar := ui.Rect{X: ax - 4, Y: ay - 4, W: 8, H: 8}
			if ps.sel[anc] {
				p.Fill(ar, C.PenBlue, 4)
				p.Stroke(ar, ui.Hex("#FFFFFF"), 4, 1)
			} else {
				p.Fill(ar, ui.Hex("#FFFFFF"), 4)
				p.Stroke(ar, C.PenBlue, 4, 1)
			}
		}
	}
}

func (a *NalarupaApp) buildCanvasChildren(c *ui.Context, topFrames, selBoxes []Box, singleFrame *Box, liveNodes []LiveNode, nowMS float64) {
	// 1. Top-level frame title labels & inline frame rename input
	for _, f := range topFrames {
		if f.Type != "frame" || f.Placeholder {
			continue
		}
		pb := Painted(f)
		sx, sy := a.docToScreen(pb.X, pb.Y)
		sw := float32(pb.W * a.Cam.Zoom)
		if sx+sw < -40 || sx > float32(a.CanvasW)+40 || sy < -20 || sy > float32(a.CanvasH)+40 {
			continue
		}
		if a.Canvas.frameRename != nil && a.Canvas.frameRename.id == f.ID {
			continue
		}
		isSel := slices.Contains(a.State.Selection, f.ID)
		lblCol := C.CanvasLabel
		if isSel {
			lblCol = C.SelectSolid
		}
		maxW := max(float32(40), sw)
		ui.Box(c.Key("frame-label-" + f.ID)).
			Absolute().
			Left(sx).
			Top(sy - 18).
			MaxWidth(maxW).
			Height(16).
			IgnoreInput(true).
			Children(func() {
				ui.Text(c, f.Name).FontSize(11).FontWeight(500).TextColor(lblCol).MaxLines(1)
			})
	}

	// Inline frame rename input
	if fr := a.Canvas.frameRename; fr != nil {
		inp := ui.TextInput(c.Key("frame-rename"), &fr.draft).
			Absolute().
			Left(fr.sx - 4).
			Top(fr.sy - 21).
			Width(fr.sw).
			Height(20).
			Radius(4).
			Background(ui.Hex("#FFFFFF")).
			Border(1.5, C.Select).
			TextColor(ui.Hex("#0F172A")).
			FontSize(11).
			Label("Frame name").
			Bind(&fr.handle)
		if fr.needSelect {
			fr.needSelect = false
			fr.handle.Focus()
			fr.handle.SelectAll()
		}
		inp.OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEscape {
				a.Canvas.frameRename = nil
				a.CanvasHandle.Focus()
				return true
			}
			return false
		})
		if inp.Submitted() || inp.FocusLeft() {
			a.commitFrameRename()
		}
	}

	// 2. Semantic selection / size-label / overlay markers (pointer-transparent so clicks pass to Canvas!)
	if a.Canvas.pen == nil && len(selBoxes) > 0 && singleFrame != nil {
		sr := a.docRectToScreen(Painted(*singleFrame))
		ui.Box(c.Key("selection-box")).
			Absolute().
			Left(sr.X).
			Top(sr.Y).
			Size(max(1, sr.W), max(1, sr.H)).
			IgnoreInput(true)

		if Turned(singleFrame.M) {
			ui.Box(c.Key("rotated-overlay")).
				Absolute().
				Left(sr.X).
				Top(sr.Y).
				Size(max(1, sr.W), max(1, sr.H)).
				IgnoreInput(true)
		}

		// Size pill centered 6px below lowest painted corner
		sizeStr := fmt.Sprintf("%s × %s", formatNum(singleFrame.W), formatNum(singleFrame.H))
		pillW := float32(len(sizeStr)*6 + 14)
		ui.Box(c.Key("size-label")).
			Absolute().
			Left(sr.X + sr.W/2 - pillW/2).
			Top(sr.Y + sr.H + 6).
			Size(pillW, 18).
			Radius(4).
			Background(C.SelectSolid).
			Center().
			IgnoreInput(true).
			Children(func() {
				ui.Text(c, sizeStr).FontSize(10).FontFeatures("tnum").TextColor(ui.Hex("#FFFFFF")).MaxLines(1)
			})
	}

	// Semantic markers for active canvas guides/overlays
	if len(a.Canvas.Guides) > 0 {
		ui.Box(c.Key("snap-guide")).Absolute().Left(0).Top(0).Size(1, 1).IgnoreInput(true)
	}
	if len(a.Canvas.Measures) > 0 {
		ui.Box(c.Key("measure-line")).Absolute().Left(0).Top(0).Size(1, 1).IgnoreInput(true)
	}
	if a.Canvas.Marquee != nil {
		ui.Box(c.Key("marquee")).Absolute().Left(0).Top(0).Size(1, 1).IgnoreInput(true)
	}
	if a.Canvas.drawDrag != nil {
		ui.Box(c.Key("draw-ghost")).Absolute().Left(0).Top(0).Size(1, 1).IgnoreInput(true)
	}
	if a.Canvas.Move != nil && a.Canvas.Move.activeDrop != nil && a.Canvas.Move.activeDrop.ID != "" {
		ui.Box(c.Key("drop-target")).Absolute().Left(0).Top(0).Size(1, 1).IgnoreInput(true)
	}
	if a.Canvas.pen != nil {
		ui.Box(c.Key("pen-overlay")).Absolute().Left(0).Top(0).Size(1, 1).IgnoreInput(true)
	}

	// 3. Inline Canvas Text Editor
	if te := a.Canvas.textEdit; te != nil {
		pb := Painted(te.box)
		sr := a.docRectToScreen(pb)
		cx := sr.X + sr.W/2
		cy := sr.Y + sr.H/2
		edW := max(float32(100), sr.W+40)
		edH := max(float32(32), sr.H+16)
		fs := float32(max(10.0, min(72.0, te.fontSize*a.Cam.Zoom)))

		ta := ui.TextArea(c.Key("text-editor"), &te.draft).
			Absolute().
			Left(cx - edW/2).
			Top(cy - edH/2).
			Width(edW).
			MinHeight(edH).
			Padding(4, 6).
			Radius(4).
			Background(ui.Hex("#FFFFFFEE")).
			Border(1.5, C.Select).
			TextColor(ui.Hex("#111111")).
			CaretColor(C.Select).
			FontSize(fs).
			Lines(1, 12).
			Label("Text content").
			Bind(&te.handle)
		if te.needSelect {
			te.needSelect = false
			te.handle.Focus()
			te.handle.SelectAll()
		}
		ta.OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEscape {
				a.Canvas.commitInlineTextEdit(a)
				return true
			}
			if k.Key == ui.KeyEnter && (k.Mods&ui.Shift == 0) {
				a.Canvas.commitInlineTextEdit(a)
				return true
			}
			return false
		})
		if ta.FocusLeft() {
			a.Canvas.commitInlineTextEdit(a)
		}
	}

	// 4. Pointer-transparent AgentLayer children (cursor labels & streaming message pills)
	a.buildAgentLayerChildren(c, liveNodes, nowMS)
}

func (a *NalarupaApp) commitFrameRename() {
	fr := a.Canvas.frameRename
	if fr == nil {
		return
	}
	a.Canvas.frameRename = nil
	trimmed := strings.TrimSpace(fr.draft)
	if trimmed != "" && trimmed != fr.orig {
		var st CoreState
		if err := a.Bridge.Call("update", map[string]any{
			"ids":   []string{fr.id},
			"props": map[string]any{"name": trimmed},
		}, &st); err == nil {
			a.State = st
			a.syncWindowTitle()
		}
	}
	a.CanvasHandle.Focus()
}

func (cs *CanvasState) startInlineTextEdit(a *NalarupaApp, id string) {
	var nd NodeDetail
	if !a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) || !nd.Exists || nd.Box == nil {
		return
	}
	content, _ := nd.Node["content"].(string)
	fs := asFloat(nd.Node["fontSize"])
	if fs <= 0 {
		fs = 20
	}
	cs.textEdit = &inlineTextEdit{
		id:         id,
		draft:      content,
		orig:       content,
		box:        *nd.Box,
		fontSize:   fs,
		needSelect: true,
	}
}

func (cs *CanvasState) commitInlineTextEdit(a *NalarupaApp) {
	te := cs.textEdit
	if te == nil {
		return
	}
	cs.textEdit = nil
	if te.draft != te.orig {
		var st CoreState
		if err := a.Bridge.Call("update", map[string]any{
			"ids":   []string{te.id},
			"props": map[string]any{"content": te.draft},
		}, &st); err == nil {
			a.State = st
			a.syncWindowTitle()
		}
	}
	a.CanvasHandle.Focus()
}

type handlePoint struct {
	name string
	sx   float32
	sy   float32
}

func (a *NalarupaApp) resizeHandleScreenPoints(b Box) []handlePoint {
	uvs := []struct {
		name string
		u, v float64
	}{
		{"nw", 0, 0}, {"n", 0.5, 0}, {"ne", 1, 0},
		{"e", 1, 0.5}, {"se", 1, 1}, {"s", 0.5, 1},
		{"sw", 0, 1}, {"w", 0, 0.5},
	}
	out := make([]handlePoint, len(uvs))
	for i, h := range uvs {
		pt := PointIn(b, h.u, h.v)
		sx, sy := a.docToScreen(pt[0], pt[1])
		out[i] = handlePoint{name: h.name, sx: sx, sy: sy}
	}
	return out
}

func (a *NalarupaApp) nodeCornerRadii(id string) ([4]float64, bool) {
	return a.Bridge.NodeCornerRadiiDirect(id)
}

func (a *NalarupaApp) radiusHandleScreenPoints(b Box, radii [4]float64) [4][2]float32 {
	pb := Painted(b)
	sr := a.docRectToScreen(pb)
	maxR := float64(min(sr.W, sr.H)) / 2
	flipped := FlipRadii(radii, b.M)
	var out [4][2]float32
	corners := [4][4]float32{
		{sr.X, sr.Y, 1, 1},
		{sr.X + sr.W, sr.Y, -1, 1},
		{sr.X + sr.W, sr.Y + sr.H, -1, -1},
		{sr.X, sr.Y + sr.H, 1, -1},
	}
	for i, c := range corners {
		rPx := min(maxR, flipped[i]*a.Cam.Zoom)
		inset := float32(max(12.0, rPx))
		out[i] = [2]float32{c[0] + c[2]*inset, c[1] + c[3]*inset}
	}
	return out
}

func (a *NalarupaApp) hitRadiusHandle(b Box, sx, sy float64) (int, [4]float64, bool) {
	pb := Painted(b)
	sr := a.docRectToScreen(pb)
	if sr.W <= 100 || sr.H <= 100 || strings.Contains(b.ID, "/") || (b.Type != "rectangle" && b.Type != "frame") {
		return -1, [4]float64{}, false
	}
	radii, allEq := a.nodeCornerRadii(b.ID)
	pts := a.radiusHandleScreenPoints(b, radii)
	for i, pt := range pts {
		if math.Abs(sx-float64(pt[0])) <= 6.5 && math.Abs(sy-float64(pt[1])) <= 6.5 {
			return i, radii, allEq
		}
	}
	return -1, [4]float64{}, false
}

func (a *NalarupaApp) hitResizeOrRotate(b Box, sx, sy float64, allowRotate bool) (resizeHandle string, rotateCorner bool) {
	sr := a.docRectToScreen(Painted(b))
	if sr.W < 10 && sr.H < 10 {
		return "", false
	}
	pts := a.resizeHandleScreenPoints(b)
	// Check 8 resize handles first (±7.5 px)
	for _, hp := range pts {
		if math.Abs(sx-float64(hp.sx)) <= CanvasHandleHitPx && math.Abs(sy-float64(hp.sy)) <= CanvasHandleHitPx {
			// On tiny selections (< 25px), interior clicks move rather than grabbing handles
			if (sr.W < 25 || sr.H < 25) && sx > float64(sr.X)+4 && sx < float64(sr.X+sr.W)-4 && sy > float64(sr.Y)+4 && sy < float64(sr.Y+sr.H)-4 {
				continue
			}
			return hp.name, false
		}
	}
	// Check outside-corner rotation zones (±17.5 px outside nw, ne, se, sw)
	if allowRotate {
		cx, cy := float64(sr.X+sr.W/2), float64(sr.Y+sr.H/2)
		for _, hp := range pts {
			if hp.name != "nw" && hp.name != "ne" && hp.name != "se" && hp.name != "sw" {
				continue
			}
			dx := sx - float64(hp.sx)
			dy := sy - float64(hp.sy)
			if math.Abs(dx) <= CanvasRotateHitPx && math.Abs(dy) <= CanvasRotateHitPx {
				// Must be farther from box center than the corner itself
				if math.Hypot(sx-cx, sy-cy) > math.Hypot(float64(hp.sx)-cx, float64(hp.sy)-cy)+2 {
					return hp.name, true
				}
			}
		}
	}
	return "", false
}

func (a *NalarupaApp) frameLabelAt(sx, sy float64) *Box {
	topFrames := a.ensureFramesFetched()
	for i := len(topFrames) - 1; i >= 0; i-- {
		f := topFrames[i]
		if f.Type != "frame" || f.Placeholder {
			continue
		}
		pb := Painted(f)
		lx, ly := a.docToScreen(pb.X, pb.Y)
		sw := max(40.0, pb.W*a.Cam.Zoom)
		labelW := min(sw, float64(len(f.Name)*7+12))
		if sx >= float64(lx) && sx <= float64(lx)+labelW && sy >= float64(ly)-18 && sy <= float64(ly)-2 {
			cp := f
			return &cp
		}
	}
	return nil
}

func (a *NalarupaApp) handleCanvasInput(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputScroll:
		sx, sy := float64(ev.X), float64(ev.Y)
		if (ev.Mods&ui.Ctrl != 0) || (ev.Mods&ui.Cmd != 0) {
			baseZ := a.Cam.Zoom
			if a.Canvas.camAnim.active && a.Canvas.camAnim.mode == "zoom-anchor" &&
				math.Hypot(sx-a.Canvas.camAnim.anchorSX, sy-a.Canvas.camAnim.anchorSY) <= 8.0 {
				baseZ = a.Canvas.camAnim.targetZ
			}
			factor := math.Exp(float64(-ev.DY) * 0.0032)
			a.ZoomAt(sx, sy, baseZ*factor, false)
			return true
		}
		a.Canvas.camAnim = camAnimState{}
		z := max(0.02, a.Cam.Zoom)
		if (ev.Mods&ui.Shift != 0) && ev.DX == 0 {
			a.Cam.X += float64(ev.DY) / z
		} else {
			a.Cam.X += float64(ev.DX) / z
			a.Cam.Y += float64(ev.DY) / z
		}
		a.Canvas.markPanDirty()
		a.Canvas.hasHoverCache = false
		a.syncViewport()
		return true

	case ui.InputZoom:
		// Trackpad pinch gestures already arrive at 60Hz+ per-frame deltas: apply smoothly with anchor
		a.ZoomAtImmediate(float64(ev.X), float64(ev.Y), a.Cam.Zoom*float64(ev.Scale), false)
		return true

	case ui.InputKeyDown:
		if ev.Key == ui.KeySpace && a.Canvas.textEdit == nil && a.Canvas.frameRename == nil {
			a.Canvas.SpaceHeld = true
			a.syncGrabCursorNow()
			return true
		}
		if a.Canvas.textEdit == nil && a.Canvas.frameRename == nil {
			return a.HandleEditorShortcut(ev.Mods, ev.Key)
		}
		return false

	case ui.InputKeyUp:
		if ev.Key == ui.KeySpace {
			a.Canvas.SpaceHeld = false
			a.syncGrabCursorNow()
			return true
		}
		return false

	case ui.InputCommand:
		switch ev.Text {
		case "undo":
			a.Undo()
			return true
		case "redo":
			a.Redo()
			return true
		case "copy":
			a.CopySelection()
			return true
		case "cut":
			a.CutSelection()
			return true
		case "paste":
			a.PasteClipboard()
			return true
		case "delete":
			a.DeleteSelection()
			return true
		case "selectAll":
			a.selectAllSiblings()
			return true
		}
		return false

	case ui.InputPointerDown:
		return a.onCanvasPointerDown(ev)

	case ui.InputPointerMove:
		return a.onCanvasPointerMove(ev)

	case ui.InputPointerUp:
		return a.onCanvasPointerUp(ev)
	}
	return false
}

func (a *NalarupaApp) onCanvasPointerDown(ev ui.InputEvent) bool {
	a.Canvas.camAnim = camAnimState{}
	sx, sy := float64(ev.X), float64(ev.Y)
	a.Canvas.PointerSX = sx
	a.Canvas.PointerSY = sy
	a.Canvas.Inside = true
	a.Canvas.Mods = ev.Mods

	// Commit open inline text or frame rename if clicking outside
	if a.Canvas.textEdit != nil {
		a.Canvas.commitInlineTextEdit(a)
	}
	if a.Canvas.frameRename != nil {
		a.commitFrameRename()
	}
	a.CanvasHandle.Focus()

	now := time.Now()
	isDouble := ev.Clicks >= 2 || (now.Sub(a.Canvas.LastDownAt) <= 450*time.Millisecond &&
		math.Hypot(sx-a.Canvas.LastDownSX, sy-a.Canvas.LastDownSY) <= 6)
	a.Canvas.LastDownAt = now
	a.Canvas.LastDownSX = sx
	a.Canvas.LastDownSY = sy

	// Middle mouse button (2 in MyGo: 0=left, 1=right, 2=middle), Hand tool, or Space+Left -> Pan
	if ev.Button == 2 || (ev.Button == 0 && (a.Tool == "hand" || a.Canvas.SpaceHeld)) {
		a.Canvas.Pan = &panGesture{
			startSX: sx,
			startSY: sy,
			startCX: a.Cam.X,
			startCY: a.Cam.Y,
		}
		a.syncGrabCursorNow()
		return true
	}
	if ev.Button != 0 {
		return false
	}

	docPt := a.screenToDoc(sx, sy)

	// Pen / Vector editing mode
	if a.Tool == "pen" || a.Canvas.pen != nil {
		return a.onPenPointerDown(docPt, isDouble, ev.Mods)
	}

	// Shape / Frame / Text creation tools
	if a.Tool == "frame" || a.Tool == "rectangle" || a.Tool == "ellipse" || a.Tool == "text" {
		parentID, parentM, inFlow := a.findDrawParent(docPt)
		a.Canvas.drawDrag = &drawGesture{
			tool:     a.Tool,
			parentID: parentID,
			parentM:  parentM,
			inFlow:   inFlow,
			startDoc: docPt,
			curDoc:   docPt,
		}
		return true
	}

	// Move tool: check selection handles (radius, resize, rotate) first
	var selBoxes []Box
	if len(a.State.Selection) > 0 {
		a.Bridge.TryCall("boxes", map[string]any{"ids": a.State.Selection}, &selBoxes)
	}
	var singleFrame *Box
	if len(selBoxes) == 1 {
		cp := selBoxes[0]
		singleFrame = &cp
	} else if len(selBoxes) > 1 {
		var rects []RectD
		for _, sb := range selBoxes {
			rects = append(rects, Painted(sb))
		}
		if u := UnionRects(rects); u != nil {
			singleFrame = &Box{X: u.X, Y: u.Y, W: u.W, H: u.H}
		}
	}

	if singleFrame != nil {
		if len(selBoxes) == 1 {
			if ci, radii, allEq := a.hitRadiusHandle(selBoxes[0], sx, sy); ci >= 0 {
				a.Canvas.Radius = &radiusGesture{
					id:        selBoxes[0].ID,
					corner:    ci,
					box:       selBoxes[0],
					origRadii: radii,
					allEqual:  allEq,
				}
				return true
			}
		}
		allowRot := len(selBoxes) == 1 && !strings.Contains(selBoxes[0].ID, "/")
		if hName, isRot := a.hitResizeOrRotate(*singleFrame, sx, sy, allowRot); hName != "" {
			if isRot {
				a.startRotateGesture(selBoxes[0], docPt)
				return true
			}
			a.startResizeGesture(*singleFrame, hName, docPt)
			return true
		}
	}

	// Check top-level frame title labels
	if lblFrame := a.frameLabelAt(sx, sy); lblFrame != nil {
		if isDouble && !strings.Contains(lblFrame.ID, "/") {
			pb := Painted(*lblFrame)
			lx, ly := a.docToScreen(pb.X, pb.Y)
			sw := float32(max(140.0, min(240.0, pb.W*a.Cam.Zoom)))
			a.Select([]string{lblFrame.ID})
			a.Canvas.frameRename = &inlineFrameRename{
				id:         lblFrame.ID,
				draft:      lblFrame.Name,
				orig:       lblFrame.Name,
				sx:         lx,
				sy:         ly,
				sw:         sw,
				needSelect: true,
			}
			return true
		}
		if !slices.Contains(a.State.Selection, lblFrame.ID) {
			if ev.Mods&ui.Shift != 0 {
				a.Select(append(append([]string{}, a.State.Selection...), lblFrame.ID))
			} else {
				a.Select([]string{lblFrame.ID})
			}
		}
		a.prepareMoveGesture(sx, sy, docPt, "")
		return true
	}

	// Hit-test document nodes at docPt
	var hitChain []string
	a.Bridge.TryCall("hit", map[string]any{"x": docPt[0], "y": docPt[1]}, &hitChain)

	hasCmd := (ev.Mods&ui.Cmd != 0) || (ev.Mods&ui.Ctrl != 0)
	hasShift := ev.Mods&ui.Shift != 0

	picked := a.pickHitTarget(hitChain, hasCmd, isDouble)
	if isDouble && picked != "" {
		var nd NodeDetail
		if a.Bridge.TryCall("node", map[string]any{"id": picked}, &nd) && nd.Exists {
			ntype, _ := nd.Node["type"].(string)
			if ntype == "text" {
				a.Select([]string{picked})
				a.Canvas.startInlineTextEdit(a, picked)
				return true
			}
			if ntype == "path" {
				a.Select([]string{picked})
				a.Canvas.enterPenEdit(a, picked, "move")
				return true
			}
		}
	}

	if picked == "" {
		// Empty space (or empty interior of a freeform artboard frame) -> Marquee!
		baseSel := []string{}
		if hasShift {
			baseSel = append(baseSel, a.State.Selection...)
		} else if len(a.State.Selection) > 0 {
			a.Select([]string{})
		}
		a.Canvas.Marquee = &marqueeGesture{
			startDoc: docPt,
			curDoc:   docPt,
			deep:     hasCmd,
			shift:    hasShift,
			baseSel:  baseSel,
		}
		return true
	}

	narrowID := ""
	if hasShift {
		if slices.Contains(a.State.Selection, picked) {
			var next []string
			for _, id := range a.State.Selection {
				if id != picked {
					next = append(next, id)
				}
			}
			a.Select(next)
			return true
		}
		a.Select(append(append([]string{}, a.State.Selection...), picked))
	} else if slices.Contains(a.State.Selection, picked) {
		if len(a.State.Selection) > 1 {
			narrowID = picked
		}
	} else {
		a.Select([]string{picked})
	}

	a.prepareMoveGesture(sx, sy, docPt, narrowID)
	return true
}

func (a *NalarupaApp) pickHitTarget(chain []string, deep, doubleClick bool) string {
	if len(chain) == 0 {
		return ""
	}
	if deep {
		return chain[len(chain)-1]
	}
	// If only a top-level frame is hit, check whether it's an artboard (has children and layout == "none")
	if len(chain) == 1 {
		if a.Bridge.IsFreeformArtboardDirect(chain[0]) && !slices.Contains(a.State.Selection, chain[0]) {
			return ""
		}
		return chain[0]
	}
	// If double-clicking, drill one level deeper inside the currently selected ancestor
	if doubleClick {
		for i, id := range chain {
			if slices.Contains(a.State.Selection, id) && i+1 < len(chain) {
				return chain[i+1]
			}
		}
	}
	// If any ancestor in chain (above root artboard) is already selected, keep or pick direct child of entered scope
	for i := len(chain) - 1; i >= 1; i-- {
		if slices.Contains(a.State.Selection, chain[i]) {
			return chain[i]
		}
	}
	// Default: if chain[0] is a top-level frame, select its direct child chain[1]
	if len(chain) >= 2 && a.Bridge.NodeTypeDirect(chain[0]) == "frame" {
		return chain[1]
	}
	return chain[0]
}

func (a *NalarupaApp) findDrawParent(docPt Pt) (string, *Matrix6, bool) {
	var chain []string
	a.Bridge.TryCall("hit", map[string]any{"x": docPt[0], "y": docPt[1]}, &chain)
	for i := len(chain) - 1; i >= 0; i-- {
		id := chain[i]
		if strings.Contains(id, "/") {
			continue
		}
		var nd NodeDetail
		if a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) && nd.Exists && nd.Box != nil {
			if t, _ := nd.Node["type"].(string); t == "frame" {
				layout, _ := nd.Node["layout"].(string)
				inFlow := layout == "horizontal" || layout == "vertical"
				pm := nd.Box.M
				if pm == nil {
					m := Matrix6{1, 0, 0, 1, nd.Box.X, nd.Box.Y}
					pm = &m
				}
				return id, pm, inFlow
			}
		}
	}
	return "document", nil, false
}

func (a *NalarupaApp) prepareMoveGesture(sx, sy float64, docPt Pt, narrowOnUp string) {
	ids := append([]string{}, a.State.Selection...)
	if len(ids) == 0 {
		return
	}
	var items []moveItemStart
	var rects []RectD
	for _, id := range ids {
		var nd NodeDetail
		if !a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) || !nd.Exists || nd.Box == nil {
			continue
		}
		var pm *Matrix6
		if nd.Parent != "" && nd.Parent != "document" {
			var pbs []Box
			if a.Bridge.TryCall("boxes", map[string]any{"ids": []string{nd.Parent}}, &pbs) && len(pbs) > 0 {
				pm = pbs[0].M
			}
		}
		items = append(items, moveItemStart{
			id:           id,
			origX:        asFloat(nd.Node["x"]),
			origY:        asFloat(nd.Node["y"]),
			parent:       nd.Parent,
			parentLayout: nd.ParentLayout,
			parentM:      pm,
			box:          *nd.Box,
		})
		rects = append(rects, Painted(*nd.Box))
	}
	u := UnionRects(rects)
	if u == nil {
		return
	}
	a.Canvas.Move = &moveGesture{
		startSX:      sx,
		startSY:      sy,
		startDoc:     docPt,
		curDoc:       docPt,
		ids:          ids,
		origIDs:      append([]string{}, ids...),
		items:        items,
		startUnion:   *u,
		narrowOnUpID: narrowOnUp,
	}
}

func (a *NalarupaApp) startResizeGesture(frame Box, handle string, docPt Pt) {
	var items []resizeItemStart
	for _, id := range a.State.Selection {
		var nd NodeDetail
		if !a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) || !nd.Exists || nd.Box == nil {
			continue
		}
		var pm *Matrix6
		var fullM *Matrix6
		if nd.Parent != "" && nd.Parent != "document" {
			var pbs []Box
			if a.Bridge.TryCall("boxes", map[string]any{"ids": []string{nd.Parent}}, &pbs) && len(pbs) > 0 {
				pm = pbs[0].M
				if pbs[0].M != nil {
					fullM = pbs[0].M
				} else {
					m := Matrix6{1, 0, 0, 1, pbs[0].X, pbs[0].Y}
					fullM = &m
				}
			}
		}
		rot, fx, fy, bx, by := 0.0, false, false, 0.0, 0.0
		if nd.Local != nil {
			rot = nd.Local.Rotation
			fx = nd.Local.FlipX
			fy = nd.Local.FlipY
			bx = nd.Local.BX
			by = nd.Local.BY
		}
		ntype, _ := nd.Node["type"].(string)
		nx := asFloat(nd.Node["x"])
		ny := asFloat(nd.Node["y"])
		items = append(items, resizeItemStart{
			id:          id,
			nodeX:       nx,
			nodeY:       ny,
			w:           nd.Box.W,
			h:           nd.Box.H,
			rot:         rot,
			flipX:       fx,
			flipY:       fy,
			bx:          bx,
			by:          by,
			ntype:       ntype,
			parentM:     pm,
			parentFullM: fullM,
			canvasPos:   ApplyMatrix(fullM, nx, ny),
		})
	}
	if len(items) == 0 {
		return
	}
	a.Canvas.Resize = &resizeGesture{
		handle:   handle,
		frame:    frame,
		startDoc: docPt,
		items:    items,
	}
}

func (a *NalarupaApp) startRotateGesture(b Box, docPt Pt) {
	var nd NodeDetail
	if !a.Bridge.TryCall("node", map[string]any{"id": b.ID}, &nd) || !nd.Exists || nd.Box == nil {
		return
	}
	rot, fx, fy, bx, by := 0.0, false, false, 0.0, 0.0
	if nd.Local != nil {
		rot = nd.Local.Rotation
		fx = nd.Local.FlipX
		fy = nd.Local.FlipY
		bx = nd.Local.BX
		by = nd.Local.BY
	}
	center := PointIn(b, 0.5, 0.5)
	ang := math.Atan2(docPt[1]-center[1], docPt[0]-center[0]) * 180 / math.Pi
	a.Canvas.Rotate = &rotateGesture{
		id:         b.ID,
		center:     center,
		startAngle: ang,
		origRot:    rot,
		origX:      asFloat(nd.Node["x"]),
		origY:      asFloat(nd.Node["y"]),
		w:          nd.Box.W,
		h:          nd.Box.H,
		flipX:      fx,
		flipY:      fy,
		bx:         bx,
		by:         by,
	}
}

func (a *NalarupaApp) flushPendingDragMove() {
	if ev := a.Canvas.pendingDragEv; ev != nil {
		evVal := *ev
		a.Canvas.pendingDragEv = nil
		a.Canvas.pendingDragArmed = false
		a.Canvas.lastDragStepAt = time.Now()
		a.applyCanvasDragMove(evVal)
	}
}

func (a *NalarupaApp) applyCanvasDragMove(ev ui.InputEvent) bool {
	sx, sy := float64(ev.X), float64(ev.Y)
	docPt := a.screenToDoc(sx, sy)

	if pg := a.Canvas.Pan; pg != nil {
		z := max(0.02, a.Cam.Zoom)
		a.Cam.X = pg.startCX - (sx-pg.startSX)/z
		a.Cam.Y = pg.startCY - (sy-pg.startSY)/z
		a.Canvas.markPanDirty()
		a.Canvas.hasHoverCache = false
		a.syncViewportNonBlocking()
		return true
	}

	if dg := a.Canvas.drawDrag; dg != nil {
		cur := docPt
		if ev.Mods&ui.Shift != 0 {
			dx := cur[0] - dg.startDoc[0]
			dy := cur[1] - dg.startDoc[1]
			side := max(math.Abs(dx), math.Abs(dy))
			cur[0] = dg.startDoc[0] + math.Copysign(side, dx)
			cur[1] = dg.startDoc[1] + math.Copysign(side, dy)
		}
		dg.curDoc = cur
		return true
	}

	if mq := a.Canvas.Marquee; mq != nil {
		mq.curDoc = docPt
		r := RectD{
			X: min(mq.startDoc[0], mq.curDoc[0]),
			Y: min(mq.startDoc[1], mq.curDoc[1]),
			W: math.Abs(mq.curDoc[0] - mq.startDoc[0]),
			H: math.Abs(mq.curDoc[1] - mq.startDoc[1]),
		}
		hits := a.Bridge.NodesInRectDirect(r.X, r.Y, r.W, r.H, mq.deep)
		if mq.shift {
			combined := append([]string{}, mq.baseSel...)
			for _, id := range hits {
				if idx := slices.Index(combined, id); idx >= 0 {
					combined = slices.Delete(combined, idx, idx+1)
				} else {
					combined = append(combined, id)
				}
			}
			a.Select(combined)
		} else {
			a.Select(hits)
		}
		return true
	}

	if rg := a.Canvas.Radius; rg != nil {
		return a.updateRadiusGesture(rg, sx, sy, ev.Mods)
	}
	if rot := a.Canvas.Rotate; rot != nil {
		return a.updateRotateGesture(rot, docPt, ev.Mods)
	}
	if rsz := a.Canvas.Resize; rsz != nil {
		return a.updateResizeGesture(rsz, docPt, ev.Mods)
	}
	if mv := a.Canvas.Move; mv != nil {
		return a.updateMoveGesture(mv, sx, sy, docPt, ev.Mods)
	}
	return false
}

func (a *NalarupaApp) onCanvasPointerMove(ev ui.InputEvent) bool {
	sx, sy := float64(ev.X), float64(ev.Y)
	a.Canvas.PointerSX = sx
	a.Canvas.PointerSY = sy
	a.Canvas.Inside = true
	a.Canvas.Mods = ev.Mods
	docPt := a.screenToDoc(sx, sy)

	if a.Tool == "pen" || a.Canvas.pen != nil {
		return a.onPenPointerMove(docPt, ev.Mods)
	}

	// Decouple high-frequency pointer motion (500Hz-1000Hz) from rendering: coalescePan/Draw/Marquee/Move/Resize/Rotate/Radius
	// to VSync cadence (~16ms) with zero heap allocations for intermediate events.
	if a.Canvas.Pan != nil || a.Canvas.drawDrag != nil || a.Canvas.Marquee != nil ||
		a.Canvas.Radius != nil || a.Canvas.Rotate != nil || a.Canvas.Resize != nil || a.Canvas.Move != nil {
		now := time.Now()
		if !a.Canvas.lastDragStepAt.IsZero() && now.Sub(a.Canvas.lastDragStepAt) < 15*time.Millisecond {
			a.Canvas.pendingDragSlot = ev
			a.Canvas.pendingDragEv = &a.Canvas.pendingDragSlot
			if !a.Canvas.pendingDragArmed {
				a.Canvas.pendingDragArmed = true
				return true
			}
			return false
		}
		a.Canvas.pendingDragEv = nil
		a.Canvas.pendingDragArmed = false
		a.Canvas.lastDragStepAt = now
		return a.applyCanvasDragMove(ev)
	}

	// Idle hover update: only request a UI frame when hovered node, cursor, or Alt-measurement actually changes.
	prevHover := a.Canvas.HoverID
	prevCursor := a.Canvas.ActiveCursor
	a.updateIdleHover(sx, sy, docPt)
	if a.Canvas.HoverID != prevHover || a.Canvas.ActiveCursor != prevCursor {
		return true
	}
	if (ev.Mods&ui.Alt != 0) && len(a.State.Selection) > 0 {
		now := time.Now()
		if a.Canvas.lastDragStepAt.IsZero() || now.Sub(a.Canvas.lastDragStepAt) >= 16*time.Millisecond {
			a.Canvas.lastDragStepAt = now
			return true
		}
	}
	return false
}

func (a *NalarupaApp) updateIdleHover(sx, sy float64, docPt Pt) {
	if a.Tool != "move" {
		a.Canvas.ActiveCursor = ui.CursorDefault
		a.Canvas.HoverID = ""
		a.Canvas.hasHoverCache = false
		return
	}
	if a.Canvas.hasHoverCache &&
		math.Abs(a.Canvas.lastHoverSX-sx) < 0.25 &&
		math.Abs(a.Canvas.lastHoverSY-sy) < 0.25 &&
		a.Canvas.lastHoverVer == a.State.Version &&
		a.Canvas.lastHoverMods == a.Canvas.Mods &&
		a.Canvas.lastHoverTool == a.Tool &&
		a.Canvas.lastHoverPanX == a.Cam.X &&
		a.Canvas.lastHoverPanY == a.Cam.Y &&
		a.Canvas.lastHoverZoom == a.Cam.Zoom &&
		slices.Equal(a.Canvas.lastHoverSel, a.State.Selection) {
		return
	}
	a.Canvas.hasHoverCache = true
	a.Canvas.lastHoverSX = sx
	a.Canvas.lastHoverSY = sy
	a.Canvas.lastHoverVer = a.State.Version
	a.Canvas.lastHoverMods = a.Canvas.Mods
	a.Canvas.lastHoverTool = a.Tool
	a.Canvas.lastHoverPanX = a.Cam.X
	a.Canvas.lastHoverPanY = a.Cam.Y
	a.Canvas.lastHoverZoom = a.Cam.Zoom
	a.Canvas.lastHoverSel = append(a.Canvas.lastHoverSel[:0], a.State.Selection...)

	a.Canvas.ActiveCursor = ui.CursorDefault
	var selBoxes []Box
	if len(a.State.Selection) > 0 {
		if a.Canvas.cachedSelVer == a.State.Version && slices.Equal(a.Canvas.cachedSelIDs, a.State.Selection) && a.Canvas.cachedSelBoxes != nil {
			selBoxes = a.Canvas.cachedSelBoxes
		} else if a.Window != nil {
			if got, ok := a.Bridge.TryBoxesDirect(a.State.Selection); ok {
				selBoxes = got
			}
		} else {
			selBoxes = a.Bridge.BoxesDirect(a.State.Selection)
		}
	}
	if len(selBoxes) == 1 {
		if ci, _, _ := a.hitRadiusHandle(selBoxes[0], sx, sy); ci >= 0 {
			a.Canvas.ActiveCursor = ui.CursorPointer
			a.Canvas.HoverID = ""
			return
		}
		allowRot := !strings.Contains(selBoxes[0].ID, "/")
		if h, isRot := a.hitResizeOrRotate(selBoxes[0], sx, sy, allowRot); h != "" {
			if isRot {
				a.Canvas.ActiveCursor = ui.CursorGrab
			} else {
				a.Canvas.ActiveCursor = ResizeUICursor(h, selBoxes[0].M)
			}
			a.Canvas.HoverID = ""
			return
		}
	} else if len(selBoxes) > 1 {
		var rects []RectD
		for _, sb := range selBoxes {
			rects = append(rects, Painted(sb))
		}
		if u := UnionRects(rects); u != nil {
			if h, _ := a.hitResizeOrRotate(Box{X: u.X, Y: u.Y, W: u.W, H: u.H}, sx, sy, false); h != "" {
				a.Canvas.ActiveCursor = ResizeUICursor(h, nil)
				a.Canvas.HoverID = ""
				return
			}
		}
	}
	if lbl := a.frameLabelAt(sx, sy); lbl != nil {
		a.Canvas.HoverID = lbl.ID
		return
	}
	chain := a.Bridge.HitDirect(docPt[0], docPt[1])
	a.Canvas.HoverID = a.pickHitTarget(chain, (a.Canvas.Mods&ui.Cmd != 0)||(a.Canvas.Mods&ui.Ctrl != 0), false)
}

func (a *NalarupaApp) updateRadiusGesture(rg *radiusGesture, sx, sy float64, mods ui.Modifiers) bool {
	if !rg.begunDrag {
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		rg.begunDrag = true
	}
	pb := Painted(rg.box)
	sr := a.docRectToScreen(pb)
	maxDocR := min(rg.box.W, rg.box.H) / 2
	var cx, cy float64
	switch rg.corner {
	case 0:
		cx, cy = float64(sr.X), float64(sr.Y)
	case 1:
		cx, cy = float64(sr.X+sr.W), float64(sr.Y)
	case 2:
		cx, cy = float64(sr.X+sr.W), float64(sr.Y+sr.H)
	case 3:
		cx, cy = float64(sr.X), float64(sr.Y+sr.H)
	}
	insetPx := max(math.Abs(sx-cx), math.Abs(sy-cy))
	docR := math.Round(max(0.0, min(maxDocR, insetPx/max(0.02, a.Cam.Zoom))))
	singleCorner := (mods&ui.Alt != 0) == rg.allEqual
	var prop any
	if !singleCorner {
		prop = docR
	} else {
		next := FlipRadii(rg.origRadii, rg.box.M)
		next[rg.corner] = docR
		unflipped := FlipRadii(next, rg.box.M)
		prop = []float64{unflipped[0], unflipped[1], unflipped[2], unflipped[3]}
	}
	var st CoreState
	if err := a.Bridge.Call("update", map[string]any{
		"ids":   []string{rg.id},
		"props": map[string]any{"cornerRadius": prop},
	}, &st); err == nil {
		a.State = st
	}
	return true
}

func (a *NalarupaApp) updateRotateGesture(rot *rotateGesture, docPt Pt, mods ui.Modifiers) bool {
	if !rot.begunDrag {
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		rot.begunDrag = true
	}
	curAng := math.Atan2(docPt[1]-rot.center[1], docPt[0]-rot.center[0]) * 180 / math.Pi
	// Screen Y grows downward, while core rotation is CCW degrees:
	deltaCCW := -(curAng - rot.startAngle)
	nextRot := rot.origRot + deltaCCW
	if mods&ui.Shift != 0 {
		nextRot = math.Round(nextRot/15.0) * 15.0
	} else {
		nextRot = math.Round(nextRot*100) / 100
	}
	origin := TurnAroundCenter(rot.origX, rot.origY, rot.w, rot.h, rot.origRot, nextRot, rot.flipX, rot.flipY, rot.bx, rot.by)
	var st CoreState
	if err := a.Bridge.Call("update", map[string]any{
		"ids": []string{rot.id},
		"props": map[string]any{
			"rotation": nextRot,
			"x":        origin[0],
			"y":        origin[1],
		},
	}, &st); err == nil {
		a.State = st
	}
	return true
}

func (a *NalarupaApp) updateResizeGesture(rsz *resizeGesture, docPt Pt, mods ui.Modifiers) bool {
	if !rsz.begunDrag {
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		rsz.begunDrag = true
	}
	shift := mods&ui.Shift != 0
	alt := mods&ui.Alt != 0

	if len(rsz.items) == 1 {
		it := rsz.items[0]
		localDelta := UnapplyMatrix(rsz.frame.M, docPt[0]-rsz.startDoc[0], docPt[1]-rsz.startDoc[1], true)
		nb := ResizeBox(RectD{X: 0, Y: 0, W: it.w, H: it.h}, rsz.handle, localDelta[0], localDelta[1], shift, alt)
		newW := math.Round(math.Abs(nb.W)*100) / 100
		newH := math.Round(math.Abs(nb.H)*100) / 100
		flipX := it.flipX != (nb.W < 0)
		flipY := it.flipY != (nb.H < 0)
		// Compute local origin shift in parent coordinates
		lx := nb.X
		ly := nb.Y
		if nb.W < 0 {
			lx = nb.X + nb.W
		}
		if nb.H < 0 {
			ly = nb.Y + nb.H
		}
		ownM := OwnMatrix(it.rot, it.flipX, it.flipY)
		pShift := LinMatrix(&ownM, lx, ly)
		if flipX != it.flipX {
			newOwn := OwnMatrix(it.rot, flipX, flipY)
			off := LinMatrix(&newOwn, newW, 0)
			pShift[0] -= off[0]
			pShift[1] -= off[1]
		}
		if flipY != it.flipY {
			newOwn := OwnMatrix(it.rot, flipX, flipY)
			off := LinMatrix(&newOwn, 0, newH)
			pShift[0] -= off[0]
			pShift[1] -= off[1]
		}
		props := map[string]any{
			"x":      math.Round((it.nodeX+pShift[0])*100) / 100,
			"y":      math.Round((it.nodeY+pShift[1])*100) / 100,
			"width":  newW,
			"height": newH,
			"flipX":  flipX,
			"flipY":  flipY,
		}
		if it.ntype == "text" {
			props["textGrowth"] = "fixed-width-height"
		}
		var st CoreState
		if err := a.Bridge.Call("update", map[string]any{"ids": []string{it.id}, "props": props}, &st); err == nil {
			a.State = st
		}
		return true
	}

	// Multi-selection proportional resize
	dx := docPt[0] - rsz.startDoc[0]
	dy := docPt[1] - rsz.startDoc[1]
	nb := ResizeBox(rsz.frame.RectOf(), rsz.handle, dx, dy, shift, alt)
	sx := math.Abs(nb.W) / max(1.0, rsz.frame.W)
	sy := math.Abs(nb.H) / max(1.0, rsz.frame.H)
	ox := nb.X
	if nb.W < 0 {
		ox = nb.X + nb.W
	}
	oy := nb.Y
	if nb.H < 0 {
		oy = nb.Y + nb.H
	}
	for _, it := range rsz.items {
		targetCanvasX := ox + (it.canvasPos[0]-rsz.frame.X)*sx
		targetCanvasY := oy + (it.canvasPos[1]-rsz.frame.Y)*sy
		localPos := UnapplyMatrix(it.parentFullM, targetCanvasX, targetCanvasY, false)
		props := map[string]any{
			"x":      math.Round(localPos[0]*100) / 100,
			"y":      math.Round(localPos[1]*100) / 100,
			"width":  math.Round(max(1.0, it.w*sx)*100) / 100,
			"height": math.Round(max(1.0, it.h*sy)*100) / 100,
		}
		a.Bridge.TryCall("update", map[string]any{"ids": []string{it.id}, "props": props}, nil)
	}
	a.RefreshState()
	return true
}

func (a *NalarupaApp) updateMoveGesture(mv *moveGesture, sx, sy float64, docPt Pt, mods ui.Modifiers) bool {
	if !mv.started {
		if math.Hypot(sx-mv.startSX, sy-mv.startSY) < CanvasDragStartPx {
			return true
		}
		mv.started = true
		mv.narrowOnUpID = ""
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		mv.begunDrag = true
		view := map[string]any{
			"x": a.Cam.X,
			"y": a.Cam.Y,
			"w": a.CanvasW / max(0.02, a.Cam.Zoom),
			"h": a.CanvasH / max(0.02, a.Cam.Zoom),
		}
		a.Bridge.TryCall("snap_targets", map[string]any{"ids": mv.ids, "view": view}, &mv.snapTargets)
	}

	// Alt-drag duplicate (with 700ms Alt-release grace period)
	altHeld := mods&ui.Alt != 0
	if altHeld && !mv.duplicated {
		canDup := true
		for _, id := range mv.origIDs {
			if strings.Contains(id, "/") {
				canDup = false
				break
			}
		}
		if canDup {
			var res struct {
				IDs   []string  `json:"ids"`
				State CoreState `json:"state"`
			}
			if err := a.Bridge.Call("duplicate", map[string]any{"ids": mv.origIDs, "inPlace": true}, &res); err == nil && len(res.IDs) == len(mv.items) {
				// Reset originals back to starting positions and drag the new duplicates!
				for _, it := range mv.items {
					if it.parentLayout == "none" || it.parentLayout == "" {
						a.Bridge.TryCall("update", map[string]any{
							"ids":   []string{it.id},
							"props": map[string]any{"x": it.origX, "y": it.origY},
						}, nil)
					}
				}
				mv.ids = res.IDs
				for i := range mv.items {
					mv.items[i].id = res.IDs[i]
				}
				mv.duplicated = true
				mv.altLostAt = time.Time{}
				a.State = res.State
				// Include originals in snap targets!
				for _, it := range mv.items {
					mv.snapTargets = append(mv.snapTargets, it.box)
				}
			}
		}
	} else if !altHeld && mv.duplicated {
		if mv.altLostAt.IsZero() {
			mv.altLostAt = time.Now()
		} else if time.Since(mv.altLostAt) > 700*time.Millisecond {
			// Revert accidental Alt duplicate after 700ms grace
			a.Bridge.TryCall("delete", map[string]any{"ids": mv.ids}, nil)
			mv.ids = append([]string{}, mv.origIDs...)
			for i := range mv.items {
				mv.items[i].id = mv.origIDs[i]
			}
			a.Select(mv.ids)
			mv.duplicated = false
			mv.altLostAt = time.Time{}
		}
	} else if altHeld {
		mv.altLostAt = time.Time{}
	}

	dx := docPt[0] - mv.startDoc[0]
	dy := docPt[1] - mv.startDoc[1]
	axes := [2]bool{true, true}
	if mods&ui.Shift != 0 {
		if math.Abs(dx) >= math.Abs(dy) {
			dy = 0
			axes[1] = false
		} else {
			dx = 0
			axes[0] = false
		}
	}

	// Snapping (disabled when Ctrl/Cmd is held)
	a.Canvas.Guides = nil
	hasCtrl := (mods&ui.Ctrl != 0) || (mods&ui.Cmd != 0)
	if !hasCtrl && len(mv.snapTargets) > 0 {
		movedRect := RectD{
			X: mv.startUnion.X + dx,
			Y: mv.startUnion.Y + dy,
			W: mv.startUnion.W,
			H: mv.startUnion.H,
		}
		var anchors []Pt
		for _, t := range mv.snapTargets {
			if slices.Contains(mv.ids, t.ID) {
				continue
			}
			anchors = append(anchors, AnchorPoints(Painted(t))...)
		}
		off, guides := Snap(DragPoints(movedRect), anchors, CanvasSnapPx/max(0.02, a.Cam.Zoom), axes)
		dx += off[0]
		dy += off[1]
		a.Canvas.Guides = guides
	}

	// Check if single item is reordering inside an auto-layout frame or hovering a drop target
	mv.activeDrop = nil
	mv.floatGhost = nil
	canReparent := true
	for _, id := range mv.ids {
		if strings.Contains(id, "/") {
			canReparent = false
			break
		}
	}

	if len(mv.items) == 1 && (mv.items[0].parentLayout == "horizontal" || mv.items[0].parentLayout == "vertical") {
		it := mv.items[0]
		ghost := RectD{X: it.box.X + dx, Y: it.box.Y + dy, W: it.box.W, H: it.box.H}
		mv.floatGhost = &ghost
		var dt DropInfo
		if a.Bridge.TryCall("drop_target", map[string]any{
			"x":      docPt[0],
			"y":      docPt[1],
			"w":      it.box.W,
			"h":      it.box.H,
			"ignore": mv.ids,
			"any":    hasCtrl,
		}, &dt) {
			if dt.ID == it.parent && dt.Index != nil {
				var st CoreState
				if err := a.Bridge.Call("reparent", map[string]any{
					"ids":    mv.ids,
					"parent": it.parent,
					"index":  *dt.Index,
				}, &st); err == nil {
					a.State = st
				}
				return true
			}
			if canReparent {
				mv.activeDrop = &dt
			}
		}
		return true
	}

	// Freeform move: apply delta in each item's parent coordinate space in one zero-JSON batch
	var batchIDs []string
	var batchXs, batchYs []float64
	for _, it := range mv.items {
		if it.parentLayout == "horizontal" || it.parentLayout == "vertical" {
			continue
		}
		lp := InParent(it.parentM, dx, dy)
		batchIDs = append(batchIDs, it.id)
		batchXs = append(batchXs, math.Round((it.origX+lp[0])*100)/100)
		batchYs = append(batchYs, math.Round((it.origY+lp[1])*100)/100)
	}
	if len(batchIDs) > 0 {
		if st, ok := a.Bridge.UpdateXYBatchDirect(batchIDs, batchXs, batchYs); ok {
			a.State = st
		}
	}

	// Probe drop target for reparenting into/out of frames
	if canReparent {
		var dt DropInfo
		if a.Bridge.TryCall("drop_target", map[string]any{
			"x":      docPt[0],
			"y":      docPt[1],
			"w":      mv.startUnion.W,
			"h":      mv.startUnion.H,
			"ignore": mv.ids,
			"any":    hasCtrl,
		}, &dt) {
			sameParent := len(mv.items) > 0 && dt.ID == mv.items[0].parent
			if !sameParent {
				cp := dt
				mv.activeDrop = &cp
			}
		}
	}
	return true
}

func (a *NalarupaApp) onCanvasPointerUp(ev ui.InputEvent) bool {
	defer func() {
		a.Canvas.lastDragStepAt = time.Time{}
		a.Canvas.pendingDragEv = nil
		a.Canvas.pendingDragArmed = false
		a.Canvas.hasHoverCache = false
	}()
	a.flushPendingDragMove()
	if a.Canvas.Pan != nil {
		a.Canvas.Pan = nil
		a.syncGrabCursorNow()
		a.Canvas.markSharpDirty()
		a.syncViewport()
		return true
	}
	if a.Tool == "pen" || a.Canvas.pen != nil {
		docPt := a.screenToDoc(float64(ev.X), float64(ev.Y))
		return a.onPenPointerUp(docPt, ev.Canceled)
	}
	if a.Canvas.drawDrag != nil {
		if ev.Canceled {
			a.Canvas.cancelDrawDrag()
		} else {
			a.Canvas.finishDrawDrag(a)
		}
		return true
	}
	if a.Canvas.Marquee != nil {
		a.Canvas.Marquee = nil
		return true
	}
	if rg := a.Canvas.Radius; rg != nil {
		a.Canvas.Radius = nil
		if rg.begunDrag {
			var st CoreState
			if err := a.Bridge.Call("drag_end", nil, &st); err == nil {
				a.State = st
				a.syncWindowTitle()
			}
		}
		return true
	}
	if rot := a.Canvas.Rotate; rot != nil {
		a.Canvas.Rotate = nil
		if rot.begunDrag {
			var st CoreState
			if err := a.Bridge.Call("drag_end", nil, &st); err == nil {
				a.State = st
				a.syncWindowTitle()
			}
		}
		return true
	}
	if rsz := a.Canvas.Resize; rsz != nil {
		a.Canvas.Resize = nil
		if rsz.begunDrag {
			var st CoreState
			if err := a.Bridge.Call("drag_end", nil, &st); err == nil {
				a.State = st
				a.syncWindowTitle()
			}
		}
		return true
	}
	if mv := a.Canvas.Move; mv != nil {
		a.Canvas.Move = nil
		a.Canvas.Guides = nil
		if !mv.started && mv.narrowOnUpID != "" {
			a.Select([]string{mv.narrowOnUpID})
			return true
		}
		if mv.activeDrop != nil && !ev.Canceled {
			targetParent := mv.activeDrop.ID
			if targetParent == "" {
				targetParent = "document"
			}
			args := map[string]any{
				"ids":    mv.ids,
				"parent": targetParent,
			}
			if mv.activeDrop.Index != nil {
				args["index"] = *mv.activeDrop.Index
			}
			a.Bridge.TryCall("reparent", args, nil)
		}
		if mv.begunDrag {
			var st CoreState
			if err := a.Bridge.Call("drag_end", nil, &st); err == nil {
				a.State = st
				a.syncWindowTitle()
			}
		}
		return true
	}
	return false
}

func (cs *CanvasState) cancelDrawDrag() {
	cs.drawDrag = nil
}

func (cs *CanvasState) finishDrawDrag(a *NalarupaApp) {
	dg := cs.drawDrag
	cs.drawDrag = nil
	if dg == nil {
		return
	}
	x0 := min(dg.startDoc[0], dg.curDoc[0])
	y0 := min(dg.startDoc[1], dg.curDoc[1])
	w := math.Abs(dg.curDoc[0] - dg.startDoc[0])
	h := math.Abs(dg.curDoc[1] - dg.startDoc[1])
	clicked := w < 3 && h < 3
	if clicked {
		x0, y0 = dg.startDoc[0], dg.startDoc[1]
		switch dg.tool {
		case "frame":
			w, h = 400, 300
		case "rectangle", "ellipse":
			w, h = 100, 100
		}
	}

	// Transform center into parent's local space so rotated/flipped parents place drawn nodes where dragged!
	localX, localY := math.Round(x0), math.Round(y0)
	if dg.parentM != nil {
		cx, cy := x0+w/2, y0+h/2
		if dg.tool == "text" && clicked {
			lp := UnapplyMatrix(dg.parentM, x0, y0, false)
			localX, localY = math.Round(lp[0]*100)/100, math.Round(lp[1]*100)/100
		} else {
			lp := UnapplyMatrix(dg.parentM, cx, cy, false)
			localX = math.Round((lp[0]-w/2)*100) / 100
			localY = math.Round((lp[1]-h/2)*100) / 100
		}
	}

	node := map[string]any{
		"type": dg.tool,
		"x":    localX,
		"y":    localY,
	}
	if dg.inFlow {
		node["layoutPosition"] = "absolute"
	}
	switch dg.tool {
	case "frame":
		node["name"] = "Frame"
		node["width"] = math.Round(w)
		node["height"] = math.Round(h)
		node["fill"] = "#FFFFFF"
		node["clip"] = dg.parentID == "document"
	case "rectangle":
		node["name"] = "Rectangle"
		node["width"] = math.Round(w)
		node["height"] = math.Round(h)
		node["fill"] = "#D9D9D9"
	case "ellipse":
		node["name"] = "Ellipse"
		node["width"] = math.Round(w)
		node["height"] = math.Round(h)
		node["fill"] = "#D9D9D9"
	case "text":
		node["name"] = "Text"
		node["content"] = "Text"
		node["fontFamily"] = "Inter"
		node["fontSize"] = 24
		node["fill"] = "#111111"
		if !clicked {
			node["textGrowth"] = "fixed-width"
			node["width"] = math.Round(w)
		}
	}

	a.Tool = "move"
	var res struct {
		ID    string    `json:"id"`
		State CoreState `json:"state"`
	}
	if err := a.Bridge.Call("insert", map[string]any{
		"parent": dg.parentID,
		"node":   node,
	}, &res); err != nil {
		a.ShowToast(err.Error(), true)
		return
	}
	a.State = res.State
	a.syncWindowTitle()
	a.onSelectionChanged(a.State.Selection)
	cs.createdPulseID = res.ID
	cs.createdPulseStartMS = 0
	if dg.tool == "text" {
		cs.startInlineTextEdit(a, res.ID)
	}
}

// --- Vector Pen & Point Editing ---

func (cs *CanvasState) enterPenEdit(a *NalarupaApp, pathID, sub string) {
	var contours []*Contour
	if !a.Bridge.TryCall("path_edit", map[string]any{"id": pathID}, &contours) {
		return
	}
	parentID := "document"
	var nd NodeDetail
	if a.Bridge.TryCall("node", map[string]any{"id": pathID}, &nd) && nd.Exists && nd.Parent != "" {
		parentID = nd.Parent
	}
	cs.pen = &penEditState{
		pathID:   pathID,
		parentID: parentID,
		sub:      sub,
		contours: contours,
		sel:      map[*Anchor]bool{},
	}
}

func (cs *CanvasState) reloadPenContours(a *NalarupaApp) {
	if cs.pen == nil || cs.pen.pathID == "" {
		return
	}
	var contours []*Contour
	if a.Bridge.TryCall("path_edit", map[string]any{"id": cs.pen.pathID}, &contours) {
		cs.pen.contours = contours
		cs.pen.sel = map[*Anchor]bool{}
		cs.pen.activeEnd = nil
	}
}

func (cs *CanvasState) exitPenEdit(a *NalarupaApp) {
	cs.pen = nil
	a.Tool = "move"
}

func (cs *CanvasState) commitPenContours(a *NalarupaApp) {
	ps := cs.pen
	if ps == nil {
		return
	}
	var drawable []*Contour
	for _, c := range ps.contours {
		if ContourDrawable(c) {
			drawable = append(drawable, c)
		}
	}
	if len(drawable) == 0 {
		if ps.pathID != "" {
			var st CoreState
			if err := a.Bridge.Call("delete", map[string]any{"ids": []string{ps.pathID}}, &st); err == nil {
				a.State = st
				a.syncWindowTitle()
			}
			ps.pathID = ""
		}
		return
	}
	args := map[string]any{
		"parent":   ps.parentID,
		"contours": drawable,
	}
	if ps.pathID != "" {
		args["id"] = ps.pathID
	}
	var res struct {
		ID    string    `json:"id"`
		State CoreState `json:"state"`
	}
	if err := a.Bridge.Call("path_write", args, &res); err == nil {
		ps.pathID = res.ID
		a.State = res.State
		a.syncWindowTitle()
	}
}

func (cs *CanvasState) deleteSelectedPenPoints(a *NalarupaApp) {
	ps := cs.pen
	if ps == nil || len(ps.sel) == 0 {
		return
	}
	contours, next := DeletePenPoints(ps.contours, ps.sel)
	ps.contours = contours
	ps.sel = map[*Anchor]bool{}
	for _, n := range next {
		ps.sel[n] = true
	}
	ps.activeEnd = nil
	cs.commitPenContours(a)
	if len(ps.contours) == 0 {
		cs.exitPenEdit(a)
	}
}

func (a *NalarupaApp) onPenPointerDown(docPt Pt, isDouble bool, mods ui.Modifiers) bool {
	if a.Canvas.pen == nil {
		parentID, _, _ := a.findDrawParent(docPt)
		a.Canvas.pen = &penEditState{
			parentID: parentID,
			sub:      "draw",
			sel:      map[*Anchor]bool{},
		}
	}
	ps := a.Canvas.pen
	if a.Tool == "pen" {
		ps.sub = "draw"
	} else {
		ps.sub = "move"
	}
	z := max(0.02, a.Cam.Zoom)

	if ps.sub == "draw" {
		hit := HitAnchor(ps.contours, docPt, z)
		if ps.activeEnd != nil {
			otherSide := OtherPenSide(ps.activeEnd.side)
			startAnchor := EndOfContour(ps.activeEnd.contour, otherSide)
			if hit != nil && hit == startAnchor && len(ps.activeEnd.contour.Anchors) >= 2 {
				// Close contour!
				ps.activeEnd.contour.Closed = true
				ps.activeEnd = nil
				ps.sel = map[*Anchor]bool{hit: true}
				_, _ = a.Bridge.CallRaw("drag_begin", nil)
				a.Canvas.commitPenContours(a)
				ps.drag = &penDragGesture{
					kind:       "new-handle",
					anchor:     hit,
					side:       PenOut,
					mirrorBoth: true,
					startDoc:   hit.Pos(),
					begunDrag:  true,
				}
				return true
			}
		}
		if hit != nil {
			if c, side, ok := FindOpenEndpoint(ps.contours, hit); ok && ps.activeEnd == nil {
				ps.activeEnd = &penOpenEnd{contour: c, side: side}
				ps.sel = map[*Anchor]bool{hit: true}
				return true
			}
		}
		snapped := Pt{PenRound(docPt[0], PenGrid), PenRound(docPt[1], PenGrid)}
		if ps.activeEnd != nil && (mods&ui.Shift != 0) {
			prev := EndOfContour(ps.activeEnd.contour, ps.activeEnd.side)
			if prev != nil {
				snapped = Constrain45(prev.Pos(), snapped)
			}
		}
		newAnc := &Anchor{X: snapped[0], Y: snapped[1]}
		if ps.activeEnd == nil {
			newC := &Contour{Anchors: []*Anchor{newAnc}, Closed: false}
			ps.contours = append(ps.contours, newC)
			ps.activeEnd = &penOpenEnd{contour: newC, side: PenOut}
		} else {
			if ps.activeEnd.side == PenOut {
				ps.activeEnd.contour.Anchors = append(ps.activeEnd.contour.Anchors, newAnc)
			} else {
				ps.activeEnd.contour.Anchors = append([]*Anchor{newAnc}, ps.activeEnd.contour.Anchors...)
			}
		}
		ps.sel = map[*Anchor]bool{newAnc: true}
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		if ContourDrawable(ps.activeEnd.contour) {
			a.Canvas.commitPenContours(a)
		}
		ps.drag = &penDragGesture{
			kind:       "new-handle",
			anchor:     newAnc,
			side:       ps.activeEnd.side,
			mirrorBoth: true,
			startDoc:   newAnc.Pos(),
			begunDrag:  true,
		}
		return true
	}

	// Point-edit ("move") submode
	shown := ShownHandles(ps.contours, ps.sel)
	if href := HitPenHandle(shown, ps.contours, docPt, z); href != nil {
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		ps.drag = &penDragGesture{
			kind:       "move-handle",
			anchor:     href.Anchor,
			side:       href.Side,
			mirrorBoth: (mods&ui.Alt == 0) && HandlesMirrored(href.Anchor),
			startDoc:   docPt,
			begunDrag:  true,
		}
		return true
	}

	if hit := HitAnchor(ps.contours, docPt, z); hit != nil {
		if isDouble {
			for _, c := range ps.contours {
				if slices.Contains(c.Anchors, hit) {
					_, _ = a.Bridge.CallRaw("drag_begin", nil)
					ToggleSmooth(c, hit)
					a.Canvas.commitPenContours(a)
					var st CoreState
					if err := a.Bridge.Call("drag_end", nil, &st); err == nil {
						a.State = st
						a.syncWindowTitle()
					}
					break
				}
			}
			return true
		}
		if mods&ui.Shift != 0 {
			if ps.sel[hit] {
				delete(ps.sel, hit)
			} else {
				ps.sel[hit] = true
			}
		} else if !ps.sel[hit] {
			ps.sel = map[*Anchor]bool{hit: true}
		}
		orig := map[*Anchor]Anchor{}
		for aPtr := range ps.sel {
			orig[aPtr] = *aPtr.Clone()
		}
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		ps.drag = &penDragGesture{
			kind:      "move-anchors",
			anchor:    hit,
			startDoc:  docPt,
			origPos:   orig,
			begunDrag: true,
		}
		return true
	}

	if segHit := HitPenSegment(ps.contours, []Pt{docPt}, z, nil, PenSegHitPx); segHit != nil {
		_, _ = a.Bridge.CallRaw("drag_begin", nil)
		inserted := PenSplit(segHit.PenSeg, segHit.T)
		ps.sel = map[*Anchor]bool{inserted: true}
		a.Canvas.commitPenContours(a)
		orig := map[*Anchor]Anchor{inserted: *inserted.Clone()}
		ps.drag = &penDragGesture{
			kind:      "move-anchors",
			anchor:    inserted,
			startDoc:  docPt,
			origPos:   orig,
			begunDrag: true,
		}
		return true
	}

	if isDouble {
		a.Canvas.exitPenEdit(a)
		return true
	}
	ps.sel = map[*Anchor]bool{}
	return true
}

func (a *NalarupaApp) onPenPointerMove(docPt Pt, mods ui.Modifiers) bool {
	ps := a.Canvas.pen
	if ps == nil {
		return false
	}
	ps.hoverPos = docPt
	ps.hasHover = true
	dg := ps.drag
	if dg == nil {
		return false
	}
	z := max(0.02, a.Cam.Zoom)
	switch dg.kind {
	case "new-handle":
		if math.Hypot((docPt[0]-dg.startDoc[0])*z, (docPt[1]-dg.startDoc[1])*z) < PenDragPx {
			return true
		}
		to := docPt
		if mods&ui.Shift != 0 {
			to = Constrain45(dg.anchor.Pos(), to)
		}
		dg.anchor.SetHandle(dg.side, &to)
		if dg.mirrorBoth {
			mir := MirrorHandle(dg.anchor, to)
			dg.anchor.SetHandle(OtherPenSide(dg.side), &mir)
		}
		a.Canvas.commitPenContours(a)
		return true

	case "move-handle":
		to := docPt
		if mods&ui.Shift != 0 {
			to = Constrain45(dg.anchor.Pos(), to)
		}
		dg.anchor.SetHandle(dg.side, &to)
		if dg.mirrorBoth && (mods&ui.Alt == 0) {
			mir := MirrorHandle(dg.anchor, to)
			dg.anchor.SetHandle(OtherPenSide(dg.side), &mir)
		}
		a.Canvas.commitPenContours(a)
		return true

	case "move-anchors":
		dx := docPt[0] - dg.startDoc[0]
		dy := docPt[1] - dg.startDoc[1]
		if mods&ui.Shift != 0 {
			if math.Abs(dx) >= math.Abs(dy) {
				dy = 0
			} else {
				dx = 0
			}
		}
		var rawPts []Pt
		for ptr, orig := range dg.origPos {
			_ = ptr
			rawPts = append(rawPts, Pt{orig.X + dx, orig.Y + dy})
		}
		snapOn := (mods&ui.Ctrl == 0) && (mods&ui.Cmd == 0)
		off, guides := SnapPenPoints(rawPts, ps.contours, ps.sel, z, snapOn, ps.sel)
		a.Canvas.Guides = guides
		fdx, fdy := dx+off[0], dy+off[1]
		for ptr, orig := range dg.origPos {
			ptr.X = orig.X + fdx
			ptr.Y = orig.Y + fdy
			if orig.In != nil {
				inPt := Pt{orig.In[0] + fdx, orig.In[1] + fdy}
				ptr.In = &inPt
			}
			if orig.Out != nil {
				outPt := Pt{orig.Out[0] + fdx, orig.Out[1] + fdy}
				ptr.Out = &outPt
			}
		}
		a.Canvas.commitPenContours(a)
		return true
	}
	return false
}

func (a *NalarupaApp) onPenPointerUp(_ Pt, _ bool) bool {
	ps := a.Canvas.pen
	if ps == nil || ps.drag == nil {
		return false
	}
	begun := ps.drag.begunDrag
	ps.drag = nil
	a.Canvas.Guides = nil
	if begun {
		var st CoreState
		if err := a.Bridge.Call("drag_end", nil, &st); err == nil {
			a.State = st
			a.syncWindowTitle()
		}
	}
	return true
}
