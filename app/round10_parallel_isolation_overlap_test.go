package main

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/naddrzz/nalarupa/core-go/agent"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func boxesOverlap(a, b Box) bool {
	return a.X < b.X+b.W && a.X+a.W > b.X && a.Y < b.Y+b.H && a.Y+a.H > b.Y
}

func TestRound10_ParallelCountSpawnsMatchingCursorsImmediately(t *testing.T) {
	// Mock SSE server that holds open briefly so we can inspect active cursors right after Send(parallel=4).
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer func() {
		close(release)
		agent.Stop(app.Bridge.eng)
		app.Close()
	}()

	app.setSetting("agent_backend", "api")
	app.setSetting("anthropic_api_key", "sk-test-redacted")
	app.setSetting("anthropic_base_url", srv.URL)

	app.Screen = "editor"
	tt := ui.NewTester(app.View, 1280, 800)
	tt.Frame()

	// User selects 4x Split and sends prompt!
	app.AgentUI.Parallel = 4
	app.AgentUI.ParallelMode = "split"
	if err := app.Bridge.Call("agent_send", map[string]any{
		"text":     "Buat 4 variasi halaman pricing modern",
		"parallel": 4,
		"mode":     "split",
		"designs":  1,
		"iterate":  "layout",
	}, nil); err != nil {
		t.Fatalf("agent_send: %v", err)
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for agent stream to start")
	}
	time.Sleep(40 * time.Millisecond)
	app.PollNow(float64(tt.Now().UnixNano()) / 1e6)
	tt.Frame()

	// Must show 4 distinct agent cursors matching the 4x setting, spread apart (not stacked on top of each other)!
	if got := len(app.Store.Cursors); got != 4 {
		t.Fatalf("expected 4 active cursors on canvas when Parallel=4, got %d", got)
	}
	var pts [][2]float64
	for _, c := range app.Store.Cursors {
		for _, prev := range pts {
			if math.Hypot(c.TX-prev[0], c.TY-prev[1]) < 24.0 {
				t.Fatalf("parallel cursors stacked at same target (%.1f, %.1f) and (%.1f, %.1f)", c.TX, c.TY, prev[0], prev[1])
			}
		}
		pts = append(pts, [2]float64{c.TX, c.TY})
	}
}

func TestRound10_NewScreenDoesNotMutateExistingScreenVariablesOrNodes(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", false)
	curNow := time.Now()
	app.Bridge.eng.Renderer.Now = func() time.Time { return curNow }

	// Turn 1: Build "Pricing — Tally" using semantic variables $ground (#F5EFE4) and $accent (#B63B24).
	if _, err := app.Bridge.ExecuteJS(`
		SetVariables({
			ground: { type: "color", value: "#F5EFE4" },
			accent: { type: "color", value: "#B63B24" }
		});
		tally = Insert(document, {
			type: "frame", name: "Pricing — Tally", x: 0, y: 0, width: 600, height: 420,
			layout: "vertical", padding: 32, gap: 20, fill: "$ground", clip: true,
			children: [
				{ type: "frame", name: "TallyCTA", width: 220, height: 64, cornerRadius: 8, fill: "$accent" }
			]
		});
	`); err != nil {
		t.Fatalf("Turn 1 ExecuteJS: %v", err)
	}

	var tallyID string
	for _, f := range app.Bridge.FramesDirect() {
		if f.Name == "Pricing — Tally" {
			tallyID = f.ID
		}
	}
	if tallyID == "" {
		t.Fatalf("Pricing — Tally not found")
	}

	app.RefreshState()
	app.Screen = "editor"
	tt := ui.NewTester(app.View, 1440, 800)
	tt.Frame()
	app.Cam = Camera{X: -40, Y: -40, Zoom: 0.55}
	app.Canvas.camAnim.active = false
	curNow = curNow.Add(4 * time.Second)
	app.PollNow(float64(curNow.UnixNano()) / 1e6)
	tt.Frame()
	tt.Frame()

	// Verify Turn 1 colors on "Pricing — Tally": ground #F5EFE4 (245,239,228) at (400, 300), CTA #B63B24 (182,59,36) at (120, 64).
	pxGroundBefore := sampleDocPixel(t, app, 400, 300)
	pxCTABefore := sampleDocPixel(t, app, 120, 64)
	if colorDistRGB(pxGroundBefore, 245, 239, 228) > 12 {
		t.Fatalf("Turn 1 ground pixel=%+v, want #F5EFE4", pxGroundBefore)
	}
	if colorDistRGB(pxCTABefore, 182, 59, 36) > 12 {
		t.Fatalf("Turn 1 CTA pixel=%+v, want #B63B24", pxCTABefore)
	}

	// Turn 2: Begin a new agent turn on the document (which already has Pricing — Tally!) where the agent builds a NEW screen
	// ("Pricing — Control Deck") and redefines SetVariables({ ground: "#0A0D14", accent: "#A3E635" })!
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.BeginTurnSnapshot()
		app.Bridge.eng.Agent.Running = true
	})
	_, err := app.Bridge.ExecuteJS(`
		SetVariables({
			ground: { type: "color", value: "#0A0D14" },
			accent: { type: "color", value: "#A3E635" }
		});
		deck = Insert(document, {
			type: "frame", name: "Pricing — Control Deck", x: 720, y: 0, width: 600, height: 420,
			layout: "vertical", padding: 32, fill: "$ground", clip: true,
			children: [
				{ type: "frame", name: "DeckCTA", width: 220, height: 64, cornerRadius: 8, fill: "$accent" }
			]
		});
	`)
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.Agent.Running = false
	})
	if err != nil {
		t.Fatalf("Turn 2 ExecuteJS: %v", err)
	}

	curNow = curNow.Add(4 * time.Second)
	app.PollNow(float64(curNow.UnixNano()) / 1e6)
	tt.Frame()
	tt.Frame()

	// Existing "Pricing — Tally" MUST STILL keep #F5EFE4 and #B63B24 (not clobbered into #0A0D14 / #A3E635)!
	pxGroundAfter := sampleDocPixel(t, app, 400, 300)
	pxCTAAfter := sampleDocPixel(t, app, 120, 64)
	if colorDistRGB(pxGroundAfter, 245, 239, 228) > 12 {
		t.Fatalf("Existing screen 'Pricing — Tally' ground was clobbered by new screen's SetVariables! got %+v, want #F5EFE4", pxGroundAfter)
	}
	if colorDistRGB(pxCTAAfter, 182, 59, 36) > 12 {
		t.Fatalf("Existing screen 'Pricing — Tally' CTA was clobbered into lime green! got %+v, want #B63B24", pxCTAAfter)
	}

	// Meanwhile the NEW screen "Pricing — Control Deck" at x=720 MUST render its dark ground #0A0D14 (10,13,20) and lime CTA #A3E635 (163,230,53)!
	pxNewGround := sampleDocPixel(t, app, 1100, 300)
	pxNewCTA := sampleDocPixel(t, app, 840, 64)
	if colorDistRGB(pxNewGround, 10, 13, 20) > 12 {
		t.Fatalf("New screen ground pixel=%+v, want #0A0D14", pxNewGround)
	}
	if colorDistRGB(pxNewCTA, 163, 230, 53) > 12 {
		t.Fatalf("New screen CTA pixel=%+v, want #A3E635", pxNewCTA)
	}

	// Also verify container sandbox: a spawned child assigned ONLY to `deck` must NOT be allowed to Update or Delete `Pricing — Tally`!
	var deckID string
	for _, f := range app.Bridge.FramesDirect() {
		if f.Name == "Pricing — Control Deck" {
			deckID = f.ID
		}
	}
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.Agent.Running = true
		app.Bridge.eng.SetAgentContainerScope("child_1", []string{deckID})
	})
	okOut := app.Bridge.eng.CallTool("execute", jsonv.MustParse(`{"__agent":"child_1","input":"Update(\"`+deckID+`\", { name: \"Pricing — Control Deck V2\" });"}`))
	out := app.Bridge.eng.CallTool("execute", jsonv.MustParse(`{"__agent":"child_1","input":"Update(\"`+tallyID+`\", { fill: \"#FF0000\" });"}`))
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.Agent.Running = false
	})
	if okOut.Error {
		t.Fatalf("expected child_1 editing inside its assigned container (%s) to succeed, got: %s", deckID, okOut.Text)
	}
	if !out.Error || !strings.Contains(out.Text, "assigned container") {
		t.Fatalf("expected child_1 editing outside its assigned container (%s) to fail with scope error, got err=%v text=%q", tallyID, out.Error, out.Text)
	}
}

func TestRound10_ComponentsAndScreensNeverOverlapWhenAgentBuilds(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.Agent.Running = true
	})
	// Reproduce exact pattern from user's screenshot:
	// 1. Existing screen at (0, 0, 1440x900)
	// 2. Multiple reusable components ("Modern nav", "Modern footer", tall 600px "Glow field") all dumped at colliding y=-200!
	// 3. Three new screen frames inserted at colliding coordinates (0, 0) / (1560, 0)!
	_, err := app.Bridge.ExecuteJS(`
		s0 = Insert(document, { type: "frame", name: "Pricing — Tally", x: 0, y: 0, width: 1440, height: 900, fill: "#F5EFE4" });
		cNav = Insert(document, { type: "frame", name: "Modern nav", reusable: true, x: 1560, y: -200, width: 1440, height: 72, fill: "#111827" });
		cFoot = Insert(document, { type: "frame", name: "Modern footer", reusable: true, x: 1560, y: -200, width: 1440, height: 120, fill: "#111827" });
		cGlow = Insert(document, { type: "frame", name: "Glow field", reusable: true, x: 3120, y: -200, width: 1440, height: 600, fill: "#090D16" });
		s1 = Insert(document, { type: "frame", name: "Pricing — Control Deck", x: 1560, y: 0, width: 1440, height: 900, placeholder: true, fill: "#0B0F19" });
		s2 = Insert(document, { type: "frame", name: "Pricing — Glass Layers", x: 3120, y: 0, width: 1440, height: 900, placeholder: true, fill: "#0B0F19" });
		s3 = Insert(document, { type: "frame", name: "Pricing — Swiss Grid", x: 0, y: 0, width: 1440, height: 900, placeholder: true, fill: "#0B0F19" });
	`)
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.Agent.Running = false
	})
	if err != nil {
		t.Fatalf("ExecuteJS: %v", err)
	}

	frames := app.Bridge.FramesDirect()
	if len(frames) < 7 {
		t.Fatalf("expected at least 7 top-level frames/components, got %d", len(frames))
	}
	for i := 0; i < len(frames); i++ {
		for j := i + 1; j < len(frames); j++ {
			if boxesOverlap(frames[i], frames[j]) {
				t.Fatalf("top-level overlap detected between %q (%.0f, %.0f, %.0fx%.0f) and %q (%.0f, %.0f, %.0fx%.0f)!",
					frames[i].Name, frames[i].X, frames[i].Y, frames[i].W, frames[i].H,
					frames[j].Name, frames[j].X, frames[j].Y, frames[j].W, frames[j].H)
			}
		}
	}
}
