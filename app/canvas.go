package main

import (
	"fmt"
	"image"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	CanvasHandleHitPx = 7.5
	CanvasRotateHitPx = 17.5
	CanvasDragStartPx = 4.0
	CanvasSnapPx      = 5.0
)

type canvasRenderKey struct {
	version uint64
	pw      int
	ph      int
	camX    float64
	camY    float64
	zoom    float64
	sharp   bool
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
	id      string
	nodeX   float64
	nodeY   float64
	w       float64
	h       float64
	rot     float64
	flipX   bool
	flipY   bool
	bx      float64
	by      float64
	ntype   string
	parentM *Matrix6
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
	RGBA       *image.RGBA
	Bitmap     *ui.Bitmap
	LastKey    canvasRenderKey
	SharpDirty bool
	FastDirty  bool
	SettleAt   time.Time

	SpaceHeld bool
	PointerSX float64
	PointerSY float64
	Inside    bool
	HoverID   string
	Mods      ui.Modifiers

	LastDownAt time.Time
	LastDownSX float64
	LastDownSY float64

	Pan     *panGesture
	Marquee *marqueeGesture
	 drawDrag *drawGesture
	Move    *moveGesture
	Resize  *resizeGesture
	Rotate  *rotateGesture
	Radius  *radiusGesture

	Guides       []SnapGuide
	Measures     []MeasureLine
	MeasureBox   *RectD
	ActiveCursor ui.Cursor

	textEdit    *inlineTextEdit
	frameRename *inlineFrameRename
	pen         *penEditState

	overlayPath ui.Path
	LastPaintMS float64
}

func (a *NalarupaApp) initCanvasState() {
	a.Canvas = CanvasState{
		SharpDirty:   true,
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
}

func (cs *CanvasState) markSharpDirty() {
	cs.SharpDirty = true
	cs.FastDirty = false
	cs.SettleAt = time.Time{}
}

func (cs *CanvasState) markFastDirty() {
	cs.FastDirty = true
	cs.SettleAt = time.Now().Add(200 * time.Millisecond)
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
	vp := ui.Box(c.Key("canvas")).
		Fill().
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

	// Pre-fetch current frames, selection boxes, and live agent nodes once per frame
	var topFrames []Box
	a.Bridge.TryCall("frames", nil, &topFrames)

	var selBoxes []Box
	if len(a.State.Selection) > 0 {
		a.Bridge.TryCall("boxes", map[string]any{"ids": a.State.Selection}, &selBoxes)
	}
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

	var liveNodes []LiveNode
	a.Bridge.TryCall("live", nil, &liveNodes)

	reduced := EffectiveReducedMotion(c)
	nowMS := float64(time.Now().UnixMilli())

	// Resolve Alt-hover distance measurement when idle in move tool
	a.Canvas.Measures = nil
	a.Canvas.MeasureBox = nil
	if a.Tool == "move" && a.Canvas.Inside && !a.Canvas.isDraggingAny() && (c.Modifiers()&ui.Alt != 0) && selUnion != nil {
		a.computeAltMeasures(*selUnion)
	}

	vp.Draw(func(p *ui.Painter, r ui.Rect) {
		if r.W > 0 && r.H > 0 {
			newW, newH := float64(r.W), float64(r.H)
			if math.Abs(newW-a.CanvasW) > 0.5 || math.Abs(newH-a.CanvasH) > 0.5 {
				a.CanvasW = newW
				a.CanvasH = newH
				a.syncViewport()
			}
		}

		dpr := EffectiveDPR(p.Scale())
		pw := max(1, int(math.Round(float64(r.W)*dpr)))
		ph := max(1, int(math.Round(float64(r.H)*dpr)))

		now := time.Now()
		if !a.Canvas.SettleAt.IsZero() && !now.Before(a.Canvas.SettleAt) {
			a.Canvas.SharpDirty = true
			a.Canvas.FastDirty = false
			a.Canvas.SettleAt = time.Time{}
		}

		sharp := !a.Canvas.FastDirty && !a.State.Animating && a.Canvas.Pan == nil
		rk := canvasRenderKey{
			version: a.State.Version,
			pw:      pw,
			ph:      ph,
			camX:    a.Cam.X,
			camY:    a.Cam.Y,
			zoom:    a.Cam.Zoom,
			sharp:   sharp,
		}
		if a.State.Animating || a.Canvas.Bitmap == nil || a.Canvas.SharpDirty || a.Canvas.FastDirty || a.Canvas.LastKey != rk {
			rgba := a.Bridge.RenderInto(&a.Canvas.RGBA, pw, ph, a.Cam.X, a.Cam.Y, a.Cam.Zoom*dpr, CanvasBG, "", sharp)
			if a.Canvas.Bitmap == nil {
				a.Canvas.Bitmap = ui.NewBitmap(rgba)
			} else {
				a.Canvas.Bitmap.Update(rgba)
			}
			a.Canvas.LastKey = rk
			a.Canvas.SharpDirty = false
			a.Canvas.FastDirty = false
		}
		p.Image(a.Canvas.Bitmap, r, ui.FillBox)

		if !a.Canvas.SettleAt.IsZero() {
			p.After(time.Until(a.Canvas.SettleAt) + 5*time.Millisecond)
		}
		if a.State.Animating {
			p.AnimationFrame()
		}

		p.Clip(r, 0, func() {
			a.drawCanvasOverlays(p, r, topFrames, selBoxes, singleFrame)
			a.drawAgentLayer(p, r, liveNodes, reduced, nowMS)
		})
	})

	vp.Children(func() {
		a.buildCanvasChildren(c, topFrames, selBoxes, singleFrame, liveNodes, nowMS)
	})
}

func (cs *CanvasState) isDraggingAny() bool {
	return cs.Pan != nil || cs.Marquee != nil || cs.drawDrag != nil || cs.Move != nil || cs.Resize != nil || cs.Rotate != nil || cs.Radius != nil || (cs.pen != nil && cs.pen.drag != nil)
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
	var hitChain []string
	a.Bridge.TryCall("hit", map[string]any{"x": docPt[0], "y": docPt[1]}, &hitChain)
	targetID := ""
	for i := len(hitChain) - 1; i >= 0; i-- {
		cand := hitChain[i]
		if !slices.Contains(a.State.Selection, cand) {
			targetID = cand
			break
		}
	}
	if targetID == "" && len(a.State.Selection) > 0 {
		var nd NodeDetail
		if a.Bridge.TryCall("node", map[string]any{"id": a.State.Selection[0]}, &nd) && nd.Exists && nd.Parent != "" && nd.Parent != "document" {
			targetID = nd.Parent
		}
	}
	if targetID == "" {
		return
	}
	var tbs []Box
	if !a.Bridge.TryCall("boxes", map[string]any{"ids": []string{targetID}}, &tbs) || len(tbs) == 0 {
		return
	}
	tb := Painted(tbs[0])
	a.Canvas.Measures = Measure(selUnion, tb)
	if len(a.Canvas.Measures) > 0 {
		a.Canvas.MeasureBox = &tb
	}
}

func (a *NalarupaApp) drawCanvasOverlays(p *ui.Painter, vpRect ui.Rect, topFrames, selBoxes []Box, singleFrame *Box) {
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

	// 1. Canvas hover outline (when idle and not already selected)
	if a.Canvas.HoverID != "" && !a.Canvas.isDraggingAny() && !slices.Contains(a.State.Selection, a.Canvas.HoverID) {
		var hbs []Box
		if a.Bridge.TryCall("boxes", map[string]any{"ids": []string{a.Canvas.HoverID}}, &hbs) && len(hbs) > 0 {
			drawBoxOutline(hbs[0], C.Select, 1)
		}
	}

	// 2. Layers-panel hover outline
	if a.HoverLayerID != "" {
		var lbs []Box
		if a.Bridge.TryCall("boxes", map[string]any{"ids": []string{a.HoverLayerID}}, &lbs) && len(lbs) > 0 {
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

	// 8. Draw creation ghost
	if dg := a.Canvas.drawDrag; dg != nil {
		r := RectD{
			X: min(dg.startDoc[0], dg.curDoc[0]),
			Y: min(dg.startDoc[1], dg.curDoc[1]),
			W: math.Abs(dg.curDoc[0] - dg.startDoc[0]),
			H: math.Abs(dg.curDoc[1] - dg.startDoc[1]),
		}
		wr := toWinRect(r)
		rad := float32(0)
		if dg.tool == "ellipse" {
			rad = min(wr.W, wr.H) / 2
		}
		p.Fill(wr, ui.Hex("#3B82F622"), rad)
		p.Stroke(wr, C.Select, rad, 1)
	}

	// 9. Vector Pen contour & handle overlay
	if ps := a.Canvas.pen; ps != nil {
		a.drawPenOverlay(p, ox, oy, ps)
	}
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
	p.StrokePath(&a.Canvas.overlayPath, 1, ui.Hex("#AAAAAA"))

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
		lblCol := C.Muted
		if isSel {
			lblCol = C.Select
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
				ui.Text(c, f.Name).FontSize(11).TextColor(lblCol).MaxLines(1)
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
			Background(C.Panel2).
			Border(1, C.Select).
			TextColor(C.Text).
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
	var nd NodeDetail
	if !a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) || !nd.Exists {
		return [4]float64{}, true
	}
	raw, ok := nd.Node["cornerRadius"]
	if !ok || raw == nil {
		return [4]float64{}, true
	}
	if arr, ok := raw.([]any); ok && len(arr) == 4 {
		r := [4]float64{asFloat(arr[0]), asFloat(arr[1]), asFloat(arr[2]), asFloat(arr[3])}
		allEq := r[0] == r[1] && r[1] == r[2] && r[2] == r[3]
		return r, allEq
	}
	v := max(0.0, asFloat(raw))
	return [4]float64{v, v, v, v}, true
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
	var topFrames []Box
	a.Bridge.TryCall("frames", nil, &topFrames)
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
			factor := math.Exp(float64(-ev.DY) * 0.0025)
			a.ZoomAt(sx, sy, a.Cam.Zoom*factor, false)
			return true
		}
		z := max(0.02, a.Cam.Zoom)
		if (ev.Mods&ui.Shift != 0) && ev.DX == 0 {
			a.Cam.X += float64(ev.DY) / z
		} else {
			a.Cam.X += float64(ev.DX) / z
			a.Cam.Y += float64(ev.DY) / z
		}
		a.Canvas.markFastDirty()
		a.syncViewport()
		return true

	case ui.InputZoom:
		a.ZoomAt(float64(ev.X), float64(ev.Y), a.Cam.Zoom*float64(ev.Scale), false)
		return true

	case ui.InputKeyDown:
		if ev.Key == ui.KeySpace && a.Canvas.textEdit == nil && a.Canvas.frameRename == nil {
			a.Canvas.SpaceHeld = true
			return true
		}
		if a.Canvas.textEdit == nil && a.Canvas.frameRename == nil {
			return a.HandleEditorShortcut(ev.Mods, ev.Key)
		}
		return false

	case ui.InputKeyUp:
		if ev.Key == ui.KeySpace {
			a.Canvas.SpaceHeld = false
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
		var nd NodeDetail
		if a.Bridge.TryCall("node", map[string]any{"id": chain[0]}, &nd) && nd.Exists {
			ntype, _ := nd.Node["type"].(string)
			layout, _ := nd.Node["layout"].(string)
			var kids []Box
			a.Bridge.TryCall("children_boxes", map[string]any{"id": chain[0]}, &kids)
			if ntype == "frame" && len(kids) > 0 && (layout == "" || layout == "none") {
				// Clicking empty interior of a freeform artboard starts marquee unless already selected
				if !slices.Contains(a.State.Selection, chain[0]) {
					return ""
				}
			}
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
	var rootND NodeDetail
	if a.Bridge.TryCall("node", map[string]any{"id": chain[0]}, &rootND) && rootND.Exists {
		if t, _ := rootND.Node["type"].(string); t == "frame" && len(chain) >= 2 {
			return chain[1]
		}
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
				return id, nd.Box.M, inFlow
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
		if nd.Parent != "" && nd.Parent != "document" {
			var pbs []Box
			if a.Bridge.TryCall("boxes", map[string]any{"ids": []string{nd.Parent}}, &pbs) && len(pbs) > 0 {
				pm = pbs[0].M
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
		items = append(items, resizeItemStart{
			id:      id,
			nodeX:   asFloat(nd.Node["x"]),
			nodeY:   asFloat(nd.Node["y"]),
			w:       nd.Box.W,
			h:       nd.Box.H,
			rot:     rot,
			flipX:   fx,
			flipY:   fy,
			bx:      bx,
			by:      by,
			ntype:   ntype,
			parentM: pm,
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

func (a *NalarupaApp) onCanvasPointerMove(ev ui.InputEvent) bool {
	sx, sy := float64(ev.X), float64(ev.Y)
	a.Canvas.PointerSX = sx
	a.Canvas.PointerSY = sy
	a.Canvas.Inside = true
	a.Canvas.Mods = ev.Mods
	docPt := a.screenToDoc(sx, sy)

	if pg := a.Canvas.Pan; pg != nil {
		z := max(0.02, a.Cam.Zoom)
		a.Cam.X = pg.startCX - (sx-pg.startSX)/z
		a.Cam.Y = pg.startCY - (sy-pg.startSY)/z
		a.Canvas.markFastDirty()
		a.syncViewport()
		return true
	}

	if a.Tool == "pen" || a.Canvas.pen != nil {
		return a.onPenPointerMove(docPt, ev.Mods)
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
		var hits []string
		a.Bridge.TryCall("nodes_in_rect", map[string]any{
			"x":    r.X,
			"y":    r.Y,
			"w":    r.W,
			"h":    r.H,
			"deep": mq.deep,
		}, &hits)
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

	// Idle hover update (cursor + hovered node)
	a.updateIdleHover(sx, sy, docPt)
	return false
}

func (a *NalarupaApp) updateIdleHover(sx, sy float64, docPt Pt) {
	a.Canvas.ActiveCursor = ui.CursorDefault
	if a.Tool != "move" {
		a.Canvas.HoverID = ""
		return
	}
	var selBoxes []Box
	if len(a.State.Selection) > 0 {
		a.Bridge.TryCall("boxes", map[string]any{"ids": a.State.Selection}, &selBoxes)
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
	var chain []string
	a.Bridge.TryCall("hit", map[string]any{"x": docPt[0], "y": docPt[1]}, &chain)
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
		props := map[string]any{
			"x":      math.Round((ox+(it.nodeX-rsz.frame.X)*sx)*100) / 100,
			"y":      math.Round((oy+(it.nodeY-rsz.frame.Y)*sy)*100) / 100,
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

	// Freeform move: apply delta in each item's parent coordinate space
	for _, it := range mv.items {
		if it.parentLayout == "horizontal" || it.parentLayout == "vertical" {
			continue
		}
		lp := InParent(it.parentM, dx, dy)
		var st CoreState
		if err := a.Bridge.Call("update", map[string]any{
			"ids": []string{it.id},
			"props": map[string]any{
				"x": math.Round((it.origX+lp[0])*100) / 100,
				"y": math.Round((it.origY+lp[1])*100) / 100,
			},
		}, &st); err == nil {
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
	if a.Canvas.Pan != nil {
		a.Canvas.Pan = nil
		a.Canvas.markSharpDirty()
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
