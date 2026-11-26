package main

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestIdleHoverZeroJSONAllocationsAndPointerCoalescing(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	// Create 35 top-level frames so any per-move JSON "frames" serialization stands out immediately.
	for i := range 35 {
		var ins map[string]any
		if err := app.Bridge.Call("insert", map[string]any{
			"node": map[string]any{
				"type":   "frame",
				"name":   fmt.Sprintf("Frame %d", i),
				"x":      float64((i % 7) * 220),
				"y":      float64((i / 7) * 220),
				"width":  180.0,
				"height": 140.0,
			},
		}, &ins); err != nil {
			t.Fatalf("insert frame %d: %v", i, err)
		}
	}
	app.RefreshState()
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1}
	app.CanvasW = 1000
	app.CanvasH = 700
	app.ensureFramesFetched()

	// 1. Idle hover over 50 distinct coordinates should NOT JSON-marshal/unmarshal 35 frames on each move.
	var step int
	allocs := testing.AllocsPerRun(50, func() {
		step++
		sx := float64(50 + (step%40)*12)
		sy := float64(40 + (step%25)*10)
		app.updateIdleHover(sx, sy, app.screenToDoc(sx, sy))
	})
	if allocs > 15 {
		t.Fatalf("updateIdleHover allocated %.1f objects/move (expected <= 15 without JSON frames roundtrip)", allocs)
	}

	// 2. Burst of 40 rapid pointer-move events during active Pan or Move within a single VSync interval
	// should coalesce instead of returning true (triggering redundant UI tree rebuilds) 40 times.
	app.Canvas.Pan = &panGesture{startSX: 100, startSY: 100, startCX: 0, startCY: 0}
	panRebuilds := 0
	for i := range 40 {
		if app.onCanvasPointerMove(ui.InputEvent{X: float32(100 + i*2), Y: float32(100 + i)}) {
			panRebuilds++
		}
	}
	app.Canvas.Pan = nil
	if panRebuilds > 2 {
		t.Fatalf("expected 40 burst Pan moves within one frame window to coalesce to <= 2 rebuilds, got %d", panRebuilds)
	}
}

func TestComposerBottomButtonsStayOnSingleRowWithQwen4Max(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	app.StartTemplate("mobile", true)
	app.RightTab = "agent"
	app.AgentUI.Backend = "provider"
	app.AgentUI.Effort = "max"
	app.AgentUI.ProviderModel = "qwen4-max"
	app.AgentUI.ActiveProvID = "qwen"
	app.AgentUI.Providers = []ProviderConfig{{
		ID:      "qwen",
		Name:    "Qwen",
		Format:  "chat",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models:  []string{"qwen4-max"},
		Vision:  true,
	}}

	tt := ui.NewTester(app.View, 1280, 800)
	tt.Frame()

	var modelY, effortY, skillsY, cookY, sendY float32
	foundModel, foundEffort, foundSkills, foundCook, foundSend := false, false, false, false, false
	for _, n := range tt.AccessAll() {
		switch n.Name {
		case "Model: qwen4-max":
			modelY, foundModel = n.Bounds.Y, true
		case "Reasoning effort: Max":
			effortY, foundEffort = n.Bounds.Y, true
		case "Agent skills":
			skillsY, foundSkills = n.Bounds.Y, true
		case "Let it cook: off":
			cookY, foundCook = n.Bounds.Y, true
		case "Send":
			sendY, foundSend = n.Bounds.Y, true
		}
	}
	if !foundModel || !foundEffort || !foundSkills || !foundCook || !foundSend {
		t.Fatalf("missing composer-bottom accessibility controls: model=%v effort=%v skills=%v cook=%v send=%v",
			foundModel, foundEffort, foundSkills, foundCook, foundSend)
	}
	for name, y := range map[string]float32{
		"effort": effortY,
		"skills": skillsY,
		"cook":   cookY,
		"send":   sendY,
	} {
		if math.Abs(float64(y-modelY)) > 2.0 {
			t.Fatalf("composer-bottom control %q wrapped onto another row: modelY=%.1f %sY=%.1f", name, modelY, name, y)
		}
	}
}

func TestOBSBitBltCompatFlagAndEnv(t *testing.T) {
	emptyEnv := func(string) string { return "" }
	if shouldEnableOBSBitBltCompat(nil, emptyEnv) {
		t.Fatal("default startup should keep hardware Direct3D 11 swapchain enabled")
	}
	if !shouldEnableOBSBitBltCompat([]string{"--obs-compat"}, emptyEnv) {
		t.Fatal("--obs-compat CLI flag should enable MYGO_GPU=0 software GDI presenter")
	}
	if !shouldEnableOBSBitBltCompat(nil, func(k string) string {
		if k == "NALARUPA_OBS_COMPAT" {
			return "1"
		}
		return ""
	}) {
		t.Fatal("NALARUPA_OBS_COMPAT=1 should enable MYGO_GPU=0 software GDI presenter")
	}
	if shouldEnableOBSBitBltCompat([]string{"--obs-compat"}, func(k string) string {
		if k == "MYGO_GPU" {
			return "1"
		}
		return ""
	}) {
		t.Fatal("explicit MYGO_GPU env var must take precedence")
	}
}

func TestDesigningStreamDetachedRenderZeroMainThreadStall(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	// Build a heavy multi-blur + backdrop-blur page via live StreamUpdate (simulating "Designing...")
	eng := app.Bridge.Engine()
	scriptSrc := buildTallShadowedGlassPageScript()
	eng.Locked(func() {
		eng.StreamUpdate("toolu_perf_1", "agent_perf", scriptSrc)
	})
	app.RefreshState()

	worker := NewCanvasRenderWorker(app.Bridge, func() {})
	defer worker.Close()

	// Continuously submit full-viewport renders to CanvasRenderWorker while measuring main-thread Try*Direct + HasSlidesDirect latency.
	for i := range 8 {
		worker.Submit(canvasRenderJob{
			key:   canvasRenderKey{version: uint64(i + 1), pw: 1100, ph: 800, camX: 0, camY: float64(i * 40), zoom: 0.45, sharp: false},
			w:     1100,
			h:     800,
			camX:  0,
			camY:  float64(i * 40),
			zoom:  0.45,
			bg:    CanvasBG,
			sharp: false,
		})
	}

	var maxProbe time.Duration
	deadline := time.Now().Add(400 * time.Millisecond)
	probes := 0
	for time.Now().Before(deadline) {
		t0 := time.Now()
		_ = app.Bridge.HasSlidesDirect()
		_, _ = app.Bridge.TryFramesDirect()
		_, _ = app.Bridge.TryLiveDirect()
		_, _ = app.Bridge.TryLayersDirect()
		_, _ = app.Bridge.TryPollFast()
		dt := time.Since(t0)
		if dt > maxProbe {
			maxProbe = dt
		}
		probes++
		time.Sleep(4 * time.Millisecond)
	}
	if probes < 20 {
		t.Fatalf("expected >= 20 concurrent UI probes, got %d", probes)
	}
	if maxProbe > 25*time.Millisecond {
		t.Fatalf("main-thread non-blocking UI reads stalled for %v during concurrent DetachedRenderer work (want <= 25ms)", maxProbe)
	}
}


