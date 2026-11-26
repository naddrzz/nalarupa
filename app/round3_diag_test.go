package main

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func buildTallShadowedGlassPageScript() string {
	var b strings.Builder
	b.WriteString(`const page = Insert(document, {type:"frame", name:"Arunika — Landing Page", x:0, y:0, width:1440, height:4154, layout:"none", clip:true, fill:"#eef5ff", effect:{type:"shadow", blur:12, color:"#00000033"}});`)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, `Insert(page, {type:"ellipse", name:"Glow %d", x:%d, y:%d, width:520, height:520, fill:"#7dd3fc55", effect:{type:"blur", radius:48}});`,
			i, (i%3)*420, i*400)
	}
	for s := 0; s < 8; s++ {
		fmt.Fprintf(&b, `const sec%d = Insert(page, {type:"frame", name:"Section %d", x:0, y:%d, width:1440, height:480, layout:"none"});`, s, s, s*500)
		for c := 0; c < 3; c++ {
			fmt.Fprintf(&b, `const card%d_%d = Insert(sec%d, {type:"frame", name:"Card %d_%d", x:%d, y:40, width:380, height:360, cornerRadius:24, fill:"#ffffff70", effect:{type:"background_blur", radius:20}});`,
				s, c, s, s, c, 60+c*420)
			fmt.Fprintf(&b, `Insert(card%d_%d, {type:"text", name:"Title %d_%d", x:24, y:24, content:"Rp 128,4 jt", fontSize:28, fontWeight:"bold", fill:"#0f172a"});`,
				s, c, s, c)
		}
	}
	return b.String()
}

func TestAllPanelsFitViewportAndScrollToBottom(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	app.StartTemplate("mobile", true)
	app.RightTab = "agent"

	// Populate 20 chat items + running state + open plan so Agent panel is tall
	app.State.AgentRunning = true
	app.AgentUI.PlanOpen = true
	app.Store.Todos = []TodoItem{
		{Content: "Frame layout & design direction", Status: "completed"},
		{Content: "Build mobile screens", Status: "in_progress", ActiveForm: "Building mobile screens"},
		{Content: "Verify contrast & spacing", Status: "pending"},
	}
	for i := 0; i < 20; i++ {
		app.Store.Items = append(app.Store.Items, ChatItem{
			ID:   i + 1,
			Kind: "assistant",
			Text: fmt.Sprintf("Step %d: Designing clean typography, spacing, and card hierarchy across the mobile screen.", i+1),
		})
	}

	const winW, winH = 1366, 720
	tt := ui.NewTester(app.View, winW, winH)
	tt.Frame()

	// 1. Agent panel bottom controls MUST be strictly inside [0, winH] (never clipped off-screen as in Gambar 2 & 3!)
	for _, labelSub := range []string{"Message the design agent", "Model:", "Reasoning effort:", "Agent skills", "Send"} {
		found := false
		for _, node := range tt.AccessAll() {
			if strings.Contains(node.Name, labelSub) {
				found = true
				bottom := node.Bounds.Y + node.Bounds.H
				if node.Bounds.Y < 0 || bottom > winH+0.5 {
					t.Errorf("Agent control %q out of viewport [0..%d]: Y=%.1f H=%.1f Bottom=%.1f",
						node.Name, winH, node.Bounds.Y, node.Bounds.H, bottom)
				}
				break
			}
		}
		if !found {
			t.Errorf("Agent control %q missing from accessibility tree", labelSub)
		}
	}

	// 2. Switch to Design Inspector with a frame selected, scroll to bottom, verify SVG & HTML export buttons fit inside [0, winH]!
	var frames []Box
	app.Bridge.TryCall("frames", nil, &frames)
	if len(frames) == 0 {
		t.Fatal("expected mobile frame")
	}
	app.Select([]string{frames[0].ID})
	app.RightTab = "design"
	app.Inspector.Scroll.Y = math.MaxFloat32
	tt.Frame()
	tt.Frame()

	for _, expLabel := range []string{"PNG", "JPEG", "WebP", "SVG", "HTML"} {
		found := false
		for _, node := range tt.AccessAll() {
			if node.Name == expLabel {
				found = true
				bottom := node.Bounds.Y + node.Bounds.H
				if node.Bounds.Y < float32(TopH) || bottom > winH+0.5 {
					t.Errorf("Inspector Export button %q out of viewport [%d..%d] after scrolling to bottom: Y=%.1f Bottom=%.1f (Scroll.Y=%.1f MaxY=%.1f)",
						expLabel, TopH, winH, node.Bounds.Y, bottom, app.Inspector.Scroll.Y, app.Inspector.Scroll.MaxY)
				}
				break
			}
		}
		if !found {
			t.Errorf("Inspector Export button %q missing", expLabel)
		}
	}
}

func TestNewScreenSymmetricCenteringInActualViewport(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	// Window width 1800 != DefaultWinW (1440), start on Home screen like real user
	tt := ui.NewTester(app.View, 1800, 960)
	if err := tt.Click("New mobile app: A 393×852 phone frame"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	var frames []Box
	app.Bridge.TryCall("frames", nil, &frames)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	sr := app.docRectToScreen(frames[0].RectOf())
	leftMargin := float64(sr.X)
	rightMargin := app.CanvasW - float64(sr.X+sr.W)
	if math.Abs(leftMargin-rightMargin) > 2.0 {
		t.Fatalf("new screen is asymmetrically positioned on canvas: leftMargin=%.1f rightMargin=%.1f (CanvasW=%.1f)",
			leftMargin, rightMargin, app.CanvasW)
	}
}

func TestAgentCursorVisitsWaypointsSmoothly(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", true)

	_, err := app.Bridge.ExecuteJS(`
		const p = Insert(document, {type:"frame", name:"Page", x:0, y:0, width:800, height:900, layout:"none"});
		Insert(p, {type:"rectangle", name:"N1", x:50, y:50, width:100, height:60, fill:"#3b82f6"});
		Insert(p, {type:"rectangle", name:"N2", x:350, y:250, width:100, height:60, fill:"#10b981"});
		Insert(p, {type:"rectangle", name:"N3", x:600, y:650, width:100, height:60, fill:"#f59e0b"});
	`)
	if err != nil {
		t.Fatal(err)
	}
	var layers []LayerRow
	app.Bridge.TryCall("layers", nil, &layers)
	var ids []string
	for _, l := range layers {
		if l.Type == "rectangle" {
			ids = append(ids, l.ID)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 rectangles, got %d", len(ids))
	}

	app.Screen = "editor"
	tt := ui.NewTester(app.View, 1024, 960)
	tt.Frame()

	now := float64(tt.Now().UnixNano()) / 1e6
	app.Store.Running["a1"] = true
	app.Store.Profiles["a1"] = AgentProfile{ID: "a1", Name: "Bayu", Color: "#f59e0b"}
	app.Store.Activity = append(app.Store.Activity, ActivityReq{
		Agent: "a1",
		IDs:   ids,
		Op:    "write",
		At:    now,
	})
	tt.Frame()
	cur := app.Store.Cursors["a1"]
	if cur == nil {
		t.Fatal("expected cursor for a1")
	}
	// On a multi-node batch, cursor must queue remaining waypoints and visit N1 -> N2 -> N3 sequentially!
	if len(cur.Waypoints) < 2 {
		t.Fatalf("expected >=2 remaining waypoints queued after starting N1, got %d", len(cur.Waypoints))
	}
	if cur.TX > 300 || cur.TY > 200 {
		t.Fatalf("cursor skipped N1 and jumped ahead: TX=%.1f TY=%.1f", cur.TX, cur.TY)
	}
	// Drive real production VSync frames via tt.Advance: verify cursor physically approaches N2 (400, 280) before N3 (650, 680)!
	reachedN2 := false
	reachedN3 := false
	reachedN3BeforeN2 := false
	for step := 1; step <= 75; step++ {
		tt.Advance(16 * time.Millisecond)
		if math.Hypot(cur.X-400, cur.Y-280) < 65 {
			reachedN2 = true
		}
		if math.Hypot(cur.X-650, cur.Y-680) < 65 {
			if !reachedN2 {
				reachedN3BeforeN2 = true
			}
			reachedN3 = true
		}
	}
	if !reachedN2 || reachedN3BeforeN2 {
		t.Fatalf("cursor did not visit N2 before N3: reachedN2=%v reachedN3BeforeN2=%v cur=(%.1f,%.1f)", reachedN2, reachedN3BeforeN2, cur.X, cur.Y)
	}
	if len(cur.Waypoints) != 0 || !reachedN3 {
		t.Fatalf("cursor did not finish visiting final waypoint N3: remaining=%d reachedN3=%v pos=(%.1f, %.1f)",
			len(cur.Waypoints), reachedN3, cur.X, cur.Y)
	}
}

func TestSmoothZoomInterpolatesAcrossFrames(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("blank", true)

	tt := ui.NewTester(app.View, 1440, 900)
	tt.Frame()

	startZoom := app.Cam.Zoom
	app.ZoomBy(1.5)
	// Immediately after triggering zoom (1 frame), smooth zoom must interpolate rather than snapping 100% of 1.5x in 1 step!
	tt.Frame()
	afterFirstFrame := app.Cam.Zoom
	wantFinal := startZoom * 1.5
	if afterFirstFrame <= startZoom || math.Abs(afterFirstFrame-wantFinal) < 0.01 {
		t.Fatalf("zoom did not smoothly interpolate on frame 1: start=%.3f after1=%.3f final=%.3f", startZoom, afterFirstFrame, wantFinal)
	}
	// Advancing frames converges smoothly to the exact target zoom!
	for i := 0; i < 60; i++ {
		tt.Frame()
	}
	if math.Abs(app.Cam.Zoom-wantFinal) > 0.005 {
		t.Fatalf("smooth zoom did not converge to target: got=%.4f want=%.4f", app.Cam.Zoom, wantFinal)
	}
}

func TestExportTallLiquidGlassTimingAndNonBlockingUI(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", true)

	if _, err := app.Bridge.ExecuteJS(buildTallShadowedGlassPageScript()); err != nil {
		t.Fatal(err)
	}
	var frames []Box
	app.Bridge.TryCall("frames", nil, &frames)
	if len(frames) != 1 {
		t.Fatalf("expected 1 root frame, got %d", len(frames))
	}
	app.Select([]string{frames[0].ID})
	app.RightTab = "design"

	// Measure raw synchronous export cost first
	t0 := time.Now()
	var files []string
	err := app.Bridge.Call("export", map[string]any{
		"ids":    []string{frames[0].ID},
		"format": "png",
		"path":   "",
		"opts":   map[string]any{"scale": 2},
	}, &files)
	rawDur := time.Since(t0)
	t.Logf("Raw synchronous PNG @2x export (1440x4154 shadowed glass page): dur=%v err=%v files=%v", rawDur, err, files)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Clicking Export JPEG in the UI must NOT block the UI frame render for > 80ms!
	// Use ReduceMotion: true here so ui.Tester measures single-frame click dispatch rather than 20 software-rasterized ToastTransition settle frames.
	tt := ui.NewTesterWith(app.View, 1440, 1200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	app.Inspector.Scroll.Y = math.MaxFloat32
	tt.Frame()
	uiStart := time.Now()
	if err := tt.Click("JPEG"); err != nil {
		t.Fatalf("click JPEG export failed: %v", err)
	}
	uiCallDur := time.Since(uiStart)
	t.Logf("UI frame duration on clicking JPEG export: %v", uiCallDur)
	if uiCallDur > 80*time.Millisecond {
		t.Fatalf("Clicking Export JPEG blocked UI thread for %v (causes Windows Not Responding!)", uiCallDur)
	}
}
