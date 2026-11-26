package main

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestRound13_ZoomInAndZoomOutStayCrispEvenWhileAgentHoldsEngineLock(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", false)

	// Build a crisp dark pricing card matching the user's screenshot ($100 / mo + crisp 1px border).
	if _, err := app.Bridge.ExecuteJS(`
		screen = Insert(document, {
			type: "frame", name: "Pricing — Control Deck", x: 0, y: 0, width: 1440, height: 900,
			fill: "#090D16", layout: "vertical", padding: 80, gap: 24
		});
		card = Insert(screen, {
			type: "frame", name: "FirmCard", width: 420, height: 520, cornerRadius: 16,
			fill: "#111622", stroke: "#2E3A52", strokeWidth: 2, padding: 32, layout: "vertical", gap: 16
		});
		Insert(card, { type: "text", content: "$100", fontSize: 56, fontWeight: "700", fill: "#F8FAFC" });
		Insert(card, { type: "text", content: "Everything in Studio, plus:", fontSize: 15, fill: "#CBD5E1" });
	`); err != nil {
		t.Fatalf("setup card: %v", err)
	}

	readyCh := make(chan struct{}, 8)
	worker := NewCanvasRenderWorker(app.Bridge, func() {
		select {
		case readyCh <- struct{}{}:
		default:
		}
	})
	defer worker.Close()

	// Step 1: Prime worker at zoomed-out 15% (0.15) — exact starting state before zooming in to 135%!
	ver := app.Bridge.VersionDirect()
	worker.Submit(canvasRenderJob{
		key:          canvasRenderKey{version: ver, pw: 1280, ph: 800, camX: -200, camY: -100, zoom: 0.15, dpr: 1.0},
		w:            1280,
		h:            800,
		camX:         -200,
		camY:         -100,
		zoom:         0.15,
		bg:           CanvasBG,
		sharp:        true,
		wantOverview: true,
	})
	select {
	case <-readyCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out priming 15%% render")
	}
	var imgOut *image.RGBA
	doneKey15, _, ok := worker.TakeReady(&imgOut)
	if !ok || doneKey15.zoom != 0.15 {
		t.Fatalf("expected initial 0.15 ready frame, got %+v", doneKey15)
	}

	// Step 2: Simulate "1/5 agents running" holding Engine.Lock() for 700ms while the user zooms 9x in to 135% (1.35)
	// AND then zooms out to 35% (0.35)! Worker MUST re-rasterize the cached immutable vector SceneSnapshot lock-free
	// in < 180ms without waiting 700ms for Engine.Lock()!
	lockHeld := make(chan struct{})
	releaseLock := make(chan struct{})
	go func() {
		app.Bridge.eng.Locked(func() {
			close(lockHeld)
			<-releaseLock
		})
	}()
	<-lockHeld
	defer func() {
		select {
		case <-releaseLock:
		default:
			close(releaseLock)
		}
	}()

	tZoomIn := time.Now()
	worker.Submit(canvasRenderJob{
		key:          canvasRenderKey{version: ver, pw: 1280, ph: 800, camX: 60, camY: 60, zoom: 1.35, dpr: 1.0},
		w:            1280,
		h:            800,
		camX:         60,
		camY:         60,
		zoom:         1.35,
		bg:           CanvasBG,
		sharp:        true,
		wantOverview: false,
	})
	select {
	case <-readyCh:
	case <-time.After(450 * time.Millisecond):
		t.Fatalf("zoom-in to 135%% blocked on Engine.Lock() held by running agent! (causes blurry upscale wait)")
	}
	zoomInElapsed := time.Since(tZoomIn)
	doneKey135, _, ok := worker.TakeReady(&imgOut)
	if !ok || doneKey135.zoom != 1.35 {
		t.Fatalf("expected crisp 1.35 ready frame, got ok=%v key=%+v", ok, doneKey135)
	}
	if zoomInElapsed > 400*time.Millisecond {
		t.Fatalf("expected fast lock-free 135%% vector raster, took %v", zoomInElapsed)
	}

	// Step 3: Zoom OUT from 135% -> 35% while Engine.Lock() is STILL held!
	worker.Submit(canvasRenderJob{
		key:          canvasRenderKey{version: ver, pw: 1280, ph: 800, camX: -100, camY: -60, zoom: 0.35, dpr: 1.0},
		w:            1280,
		h:            800,
		camX:         -100,
		camY:         -60,
		zoom:         0.35,
		bg:           CanvasBG,
		sharp:        true,
		wantOverview: false,
	})
	select {
	case <-readyCh:
	case <-time.After(450 * time.Millisecond):
		t.Fatalf("zoom-out to 35%% blocked on Engine.Lock()!")
	}
	doneKey35, _, ok := worker.TakeReady(&imgOut)
	if !ok || doneKey35.zoom != 0.35 {
		t.Fatalf("expected crisp 0.35 ready frame on zoom-out, got ok=%v key=%+v", ok, doneKey35)
	}
	close(releaseLock)

	// Step 4: Verify viewCanvas immediately pre-renders targetZoom during smooth ZoomAt!
	app.Screen = "editor"
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1.0}
	tt := ui.NewTester(app.View, 1280, 800)
	tt.Frame()
	app.ZoomAt(400, 300, 1.35, false)
	tt.Frame()
	if app.Canvas.LastKey.zoom != 1.35 {
		t.Fatalf("expected viewCanvas to pre-rasterize directly at target zoom 1.35 on first zoom frame, got %.3f", app.Canvas.LastKey.zoom)
	}
}

func TestRound13_CustomProviderContextWindowAndLiveChatContextRemainingUI(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	var savedID string
	if err := app.Bridge.Call("provider_save", map[string]any{
		"provider": map[string]any{
			"name":           "Qwen Studio",
			"format":         "chat",
			"base_url":       "http://127.0.0.1:11434/v1",
			"api_key":        "sk-qwen",
			"models":         []string{"qwen4-max"},
			"vision":         true,
			"context_window": 96000,
		},
	}, &savedID); err != nil {
		t.Fatalf("provider_save: %v", err)
	}
	app.setSetting("agent_backend", "provider")
	app.setSetting("agent_provider", savedID)
	app.setSetting("provider_model", "qwen4-max")

	if gotLim := app.effectiveContextLimit(); gotLim != 96000 {
		t.Fatalf("effectiveContextLimit() = %d, want custom provider 96000", gotLim)
	}

	// Simulate live context usage reported during an agent turn: 24,000 used of 96,000 -> 72,000 remaining!
	app.Store.ApplyEvents([]map[string]any{
		{
			"type":      "agent_context",
			"used":      float64(24000),
			"limit":     float64(96000),
			"remaining": float64(72000),
		},
	}, 1000.0)

	used, lim, rem, ratio := app.effectiveContextStats()
	if used != 24000 || lim != 96000 || rem != 72000 || ratio < 0.24 || ratio > 0.26 {
		t.Fatalf("effectiveContextStats() = (used=%d, lim=%d, rem=%d, ratio=%.3f), want (24000, 96000, 72000, 0.25)", used, lim, rem, ratio)
	}

	app.Screen = "editor"
	app.RightTab = "agent"
	app.AgentUI.SettingsOpen = true
	ap := app.activeProvider()
	if ap == nil {
		t.Fatalf("expected active custom provider")
	}
	cp := *ap
	app.AgentUI.EditingProv = &cp
	app.AgentUI.ProvContextDraft = "96000"

	tt := ui.NewTester(app.View, 1360, 860)
	tt.Frame()

	// Audit accessibility across all new context meter & custom provider context window controls!
	assertAllInteractiveControlsNamed(t, tt, "Round 13 context window & custom provider settings")
}
