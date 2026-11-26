package main

import (
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestRound7_RepaintOnlyVSyncAdvancesPreDesignAndDesigningCursor(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	app.Screen = "editor"
	tt := ui.NewTester(app.View, 640, 400)
	tt.Frame()

	// 1. Trigger "mau designing" phase: agent "Bayu" starts before any canvas node exists.
	startMS := float64(tt.Now().UnixNano()) / 1e6
	app.Store.ViewCenter = Pt{260, 180}
	app.Store.ApplyEvents([]map[string]any{
		{
			"type": "agent",
			"kind": "start",
			"agent": map[string]any{
				"id":    "bayu",
				"name":  "Bayu",
				"color": "#FF7EB6",
			},
		},
	}, startMS)

	// Initial frame mounts View(c) once; subsequent VSync refreshes ONLY repaint vp.Draw via tt.Advance!
	tt.Frame()
	tt.Advance(240 * time.Millisecond)

	nowAfterFade := float64(tt.Now().UnixNano()) / 1e6
	animFrame, after := app.Store.FrameSchedule(nowAfterFade, false, 0, nowAfterFade, true)
	if !animFrame || after != 0 {
		t.Fatalf("pre-design ('mau designing') cursor FrameSchedule = (%v, %v), want (true, 0) for locked VSync smoothness", animFrame, after)
	}
	if !app.Canvas.vsyncActive.Load() {
		t.Fatalf("expected Canvas.vsyncActive == true while Bayu cursor is active")
	}

	cur := app.Store.Cursors["bayu"]
	if cur == nil {
		t.Fatalf("expected Bayu cursor to exist in 'mau designing' state")
	}

	// Step 12 repaint-only 60Hz VSync frames (17ms each) WITHOUT calling tt.Frame() / rebuilding View(c).
	prevX, prevY := cur.X, cur.Y
	frozenPreDesignFrames := 0
	for step := 1; step <= 12; step++ {
		tt.Advance(17 * time.Millisecond)
		dist := math.Hypot(cur.X-prevX, cur.Y-prevY)
		if dist < 1e-4 {
			frozenPreDesignFrames++
		}
		if dist > 45.0 {
			t.Fatalf("step %d: pre-design cursor jumped discontinuously by %.2fpx in a single VSync frame", step, dist)
		}
		prevX, prevY = cur.X, cur.Y
	}
	if frozenPreDesignFrames > 0 {
		t.Fatalf("pre-design cursor stayed frozen on %d/12 VSync repaint frames (nowMS stale across Painter.AnimationFrame repaints!)", frozenPreDesignFrames)
	}

	// 2. Active "designing" phase: queue sequential node waypoints and verify repaint-only VSync frames glide smoothly.
	cur.Waypoints = append(cur.Waypoints,
		CursorWaypoint{X: 360, Y: 140, BoxID: "n1", Op: "write", HoldMS: 420},
		CursorWaypoint{X: 360, Y: 260, BoxID: "n2", Op: "write", HoldMS: 420},
		CursorWaypoint{X: 480, Y: 260, BoxID: "n3", Op: "write", HoldMS: 420},
	)
	prevX, prevY = cur.X, cur.Y
	frozenDesignFrames := 0
	totalTravel := 0.0
	for step := 1; step <= 16; step++ {
		tt.Advance(17 * time.Millisecond)
		dist := math.Hypot(cur.X-prevX, cur.Y-prevY)
		if step >= 2 && dist < 1e-4 {
			frozenDesignFrames++
		}
		totalTravel += dist
		prevX, prevY = cur.X, cur.Y
	}
	if frozenDesignFrames > 0 {
		t.Fatalf("designing cursor froze on %d/15 VSync repaint frames while traversing waypoints", frozenDesignFrames)
	}
	if totalTravel < 60.0 {
		t.Fatalf("designing cursor barely moved across 16 VSync frames: totalTravel=%.2fpx", totalTravel)
	}
}

func TestRound7_DrawAgentLayerNeverBlocksOnLockedEngineWithActiveFlashes(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	if _, err := app.Bridge.ExecuteJS(`
		Insert(document, { type: "rectangle", name: "Card", x: 60, y: 60, width: 200, height: 120, fill: "#2B48D8" });
	`); err != nil {
		t.Fatalf("ExecuteJS: %v", err)
	}
	app.RefreshState()
	app.Screen = "editor"

	tt := ui.NewTester(app.View, 640, 400)
	nowMS := float64(tt.Now().UnixNano()) / 1e6
	app.Store.Poke(AgentProfile{ID: "bayu", Name: "Bayu", Color: "#FF7EB6"}, nowMS)
	app.Store.Running["bayu"] = true
	app.PollNow(nowMS)
	tt.Frame()

	boxes := app.Bridge.FramesDirect()
	if len(boxes) == 0 {
		t.Fatalf("expected inserted rectangle box")
	}
	app.pushNodeFlash(boxes[0].ID, "#FF7EB6", "write", float64(tt.Now().UnixNano())/1e6, 1200)

	// Hold Engine.Lock() on a background goroutine for up to 400ms (simulating heavy StreamUpdate / Sync during designing).
	locked := make(chan struct{})
	release := make(chan struct{})
	doneHold := make(chan struct{})
	go func() {
		app.Bridge.eng.Lock()
		close(locked)
		select {
		case <-release:
		case <-time.After(400 * time.Millisecond):
		}
		app.Bridge.eng.Unlock()
		close(doneHold)
	}()
	<-locked

	t0 := time.Now()
	// Trigger a single VSync repaint (vp.Draw -> drawCanvasOverlays + drawAgentLayer) while Engine.Lock() is held!
	tt.Advance(20 * time.Millisecond)
	elapsed := time.Since(t0)

	close(release)
	<-doneHold

	if elapsed > 120*time.Millisecond {
		t.Fatalf("VSync canvas repaint blocked on Engine.Lock() for %v with active Flashes (want < 120ms non-blocking paint!)", elapsed)
	}
}

func TestRound7_WorkerReadySkipsRedundantFullWindowUpdateDuringVSync(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	var windowUpdates atomic.Int32
	cbDone := make(chan struct{}, 4)
	worker := NewCanvasRenderWorker(app.Bridge, func() {
		if shouldWakeWindowOnWorkerReady(&app.Canvas.vsyncActive) {
			windowUpdates.Add(1)
		}
		select {
		case cbDone <- struct{}{}:
		default:
		}
	})
	defer worker.Close()

	// 1. During active VSync animation, worker completions do NOT hammer full-window View(c) rebuilds.
	app.Canvas.vsyncActive.Store(true)
	worker.Submit(canvasRenderJob{
		key: canvasRenderKey{version: app.Bridge.VersionDirect(), pw: 320, ph: 200, zoom: 1, dpr: 1},
		w:   320, h: 200, zoom: 1, bg: CanvasBG,
	})
	select {
	case <-cbDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("expected worker to finish job and run onReady")
	}
	if !worker.HasReady() {
		t.Fatalf("expected worker to have ready frame")
	}
	if got := windowUpdates.Load(); got != 0 {
		t.Fatalf("expected 0 redundant Window.Update calls while vsyncActive=true, got %d", got)
	}
	_, _, _ = worker.TakeReady(&app.Canvas.RGBA)

	// 2. When VSync animation is idle, worker completion wakes the window cleanly.
	app.Canvas.vsyncActive.Store(false)
	worker.Submit(canvasRenderJob{
		key: canvasRenderKey{version: app.Bridge.VersionDirect(), pw: 320, ph: 200, zoom: 1, dpr: 1, sharp: true},
		w:   320, h: 200, zoom: 1, bg: CanvasBG, sharp: true,
	})
	select {
	case <-cbDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("expected second worker job to finish")
	}
	if got := windowUpdates.Load(); got != 1 {
		t.Fatalf("expected 1 Window.Update call when vsyncActive=false, got %d", got)
	}
}
