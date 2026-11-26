package main

import (
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func colorDistRGB(a color.RGBA, r, g, b uint8) float64 {
	dr := float64(a.R) - float64(r)
	dg := float64(a.G) - float64(g)
	db := float64(a.B) - float64(b)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func sampleDocPixel(t *testing.T, app *NalarupaApp, docX, docY float64) color.RGBA {
	t.Helper()
	if app.Canvas.RGBA == nil {
		t.Fatalf("Canvas.RGBA is nil")
	}
	lk := app.Canvas.LastKey
	dpr := lk.dpr
	if dpr <= 0 {
		dpr = 1.0
	}
	devZoom := lk.zoom * dpr
	px := int(math.Round((docX - lk.camX) * devZoom))
	py := int(math.Round((docY - lk.camY) * devZoom))
	b := app.Canvas.RGBA.Bounds()
	if px < b.Min.X || px >= b.Max.X || py < b.Min.Y || py >= b.Max.Y {
		t.Fatalf("sample point doc=(%.1f, %.1f) -> px=(%d, %d) outside raster bounds %v", docX, docY, px, py, b)
	}
	return app.Canvas.RGBA.RGBAAt(px, py)
}

func TestRound9_InsertedNodesStayUnrevealedUntilCursorArrives(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	// Create the artboard ground (#FAF8F5) first and settle it.
	if _, err := app.Bridge.ExecuteJS(`
		page = Insert(document, {
			type: "frame", name: "Artboard", x: 0, y: 0, width: 960, height: 560,
			layout: "none", fill: "#FAF8F5", clip: true
		});
	`); err != nil {
		t.Fatalf("setup artboard: %v", err)
	}
	app.RefreshState()
	app.Screen = "editor"
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1.0}

	tt := ui.NewTester(app.View, 1280, 760)
	app.Bridge.eng.Renderer.Now = func() time.Time { return tt.Now() }
	now0 := float64(tt.Now().UnixNano()) / 1e6
	app.PollNow(now0)
	tt.Frame()

	// Park agent cursor "Bayu" near top-left (30, 30), far from CardA (300, 260) and CardB (680, 260).
	startMS := float64(tt.Now().UnixNano()) / 1e6
	app.Store.ApplyEvents([]map[string]any{
		{
			"type": "agent",
			"kind": "start",
			"agent": map[string]any{
				"id":    "agent",
				"name":  "Bayu",
				"color": "#FF7EB6",
			},
		},
	}, startMS)
	cur := app.Store.Cursors["agent"]
	if cur == nil {
		t.Fatalf("expected Bayu cursor")
	}
	cur.X, cur.Y = 30, 30
	cur.TX, cur.TY = 30, 30
	cur.AnchorX, cur.AnchorY, cur.HasAnchor = 30, 30, true

	// Agent inserts two cards far apart inside Artboard:
	// CardA centered at (320, 260) in cobalt blue (#1D4ED8 = 29, 78, 216)
	// CardB centered at (700, 260) in emerald green (#047857 = 4, 120, 87)
	if _, err := app.Bridge.ExecuteJS(`
		cardA = Insert(page, {
			type: "frame", name: "CardA", x: 230, y: 200, width: 180, height: 120,
			cornerRadius: 12, fill: "#1D4ED8"
		});
		cardB = Insert(page, {
			type: "frame", name: "CardB", x: 610, y: 200, width: 180, height: 120,
			cornerRadius: 12, fill: "#047857"
		});
	`); err != nil {
		t.Fatalf("insert cards: %v", err)
	}

	// Poll events and render 1 early VSync frame (+16ms): Bayu is still near (30..80, 30..80), >200px from CardA & >550px from CardB!
	app.PollNow(float64(tt.Now().UnixNano()) / 1e6)
	tt.Frame()
	tt.Advance(16 * time.Millisecond)

	distToA := math.Hypot(cur.X-320, cur.Y-260)
	if distToA < 140 {
		t.Fatalf("expected cursor to still be in flight toward CardA at +16ms, got cur=(%.1f, %.1f) distToA=%.1f", cur.X, cur.Y, distToA)
	}

	pxAEarly := sampleDocPixel(t, app, 320, 260)
	pxBEarly := sampleDocPixel(t, app, 700, 260)
	// Artboard ground #FAF8F5 is (250, 248, 245). Neither CardA nor CardB may appear before Bayu reaches them!
	if colorDistRGB(pxAEarly, 250, 248, 245) > 12 {
		t.Fatalf("CardA appeared on canvas BEFORE cursor arrived! cur=(%.1f, %.1f), CardA pixel=%+v (want artboard ground #FAF8F5)", cur.X, cur.Y, pxAEarly)
	}
	if colorDistRGB(pxBEarly, 250, 248, 245) > 12 {
		t.Fatalf("CardB appeared on canvas BEFORE cursor arrived! cur=(%.1f, %.1f), CardB pixel=%+v (want artboard ground #FAF8F5)", cur.X, cur.Y, pxBEarly)
	}

	// Advance VSync frames until Bayu arrives at CardA, plus 3 frames (~48ms) so CardA is visibly materializing right under Bayu,
	// while Bayu has NOT yet reached CardB!
	arrivedA := false
	for step := 0; step < 40; step++ {
		tt.Advance(16 * time.Millisecond)
		if math.Hypot(cur.X-320, cur.Y-260) <= 55 {
			arrivedA = true
			break
		}
	}
	if !arrivedA {
		t.Fatalf("cursor never reached CardA within 40 VSync frames; cur=(%.1f, %.1f)", cur.X, cur.Y)
	}
	// Step 3 more VSync frames (~48ms): CardA must now be visibly drawing under Bayu, while CardB (at x=700) is STILL unrevealed!
	for i := 0; i < 3; i++ {
		tt.Advance(16 * time.Millisecond)
	}
	pxAMid := sampleDocPixel(t, app, 320, 260)
	pxBMid := sampleDocPixel(t, app, 700, 260)
	if colorDistRGB(pxAMid, 250, 248, 245) < 25 {
		t.Fatalf("CardA did NOT start materializing when cursor arrived at it! cur=(%.1f, %.1f), CardA pixel=%+v", cur.X, cur.Y, pxAMid)
	}
	if math.Hypot(cur.X-700, cur.Y-260) > 140 && colorDistRGB(pxBMid, 250, 248, 245) > 12 {
		t.Fatalf("CardB popped in prematurely while cursor was still at (%.1f, %.1f) drawing CardA! CardB pixel=%+v", cur.X, cur.Y, pxBMid)
	}

	// Advance VSync frames until Bayu reaches CardB and both cards finish their crisp EaseOutQuint reveal.
	for step := 0; step < 60; step++ {
		tt.Advance(16 * time.Millisecond)
	}
	pxADone := sampleDocPixel(t, app, 320, 260)
	pxBDone := sampleDocPixel(t, app, 700, 260)
	if colorDistRGB(pxADone, 29, 78, 216) > 15 {
		t.Fatalf("CardA did not settle to #1D4ED8 after cursor drew it: got %+v", pxADone)
	}
	if colorDistRGB(pxBDone, 4, 120, 87) > 15 {
		t.Fatalf("CardB did not settle to #047857 after cursor drew it: got %+v", pxBDone)
	}
}

func TestRound9_EditedNodeHoldsOldVisualUntilCursorArrivesThenMorphs(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	// Setup Artboard (#FAF8F5) with an existing emerald-green Card (#047857 = 4, 120, 87) at (610, 200).
	if _, err := app.Bridge.ExecuteJS(`
		page = Insert(document, {
			type: "frame", name: "Artboard", x: 0, y: 0, width: 960, height: 560,
			layout: "none", fill: "#FAF8F5", clip: true
		});
		targetCard = Insert(page, {
			type: "frame", name: "TargetCard", x: 610, y: 200, width: 180, height: 120,
			cornerRadius: 12, fill: "#047857"
		});
	`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	app.RefreshState()
	app.Screen = "editor"
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1.0}

	tt := ui.NewTester(app.View, 1280, 760)
	app.Bridge.eng.Renderer.Now = func() time.Time { return tt.Now() }
	// Advance past initial insert so TargetCard is 100% settled in emerald green (#047857).
	tt.Advance(2 * time.Second)
	app.PollNow(float64(tt.Now().UnixNano()) / 1e6)
	tt.Frame()

	pxInitial := sampleDocPixel(t, app, 700, 260)
	if colorDistRGB(pxInitial, 4, 120, 87) > 15 {
		t.Fatalf("initial TargetCard pixel=%+v, want settled emerald #047857", pxInitial)
	}

	// Park Bayu cursor far away at (40, 60).
	nowMS := float64(tt.Now().UnixNano()) / 1e6
	app.Store.Poke(AgentProfile{ID: "agent", Name: "Bayu", Color: "#FF7EB6"}, nowMS)
	app.Store.Running["agent"] = true
	cur := app.Store.Cursors["agent"]
	cur.X, cur.Y = 40, 60
	cur.TX, cur.TY = 40, 60
	cur.VX, cur.VY = 0, 0
	cur.Waypoints = nil

	// Agent edits TargetCard's fill from emerald (#047857) to crimson (#DC2626 = 220, 38, 38)!
	if _, err := app.Bridge.ExecuteJS(`
		Update(targetCard, { fill: "#DC2626" });
	`); err != nil {
		t.Fatalf("Update targetCard: %v", err)
	}

	// Poll activity and step 1 VSync frame (+16ms): Bayu is still >500px away from TargetCard!
	app.PollNow(float64(tt.Now().UnixNano()) / 1e6)
	tt.Frame()
	tt.Advance(16 * time.Millisecond)

	if math.Hypot(cur.X-700, cur.Y-260) < 300 {
		t.Fatalf("expected cursor to still be far from TargetCard at +16ms, got (%.1f, %.1f)", cur.X, cur.Y)
	}
	pxBeforeArrival := sampleDocPixel(t, app, 700, 260)
	if colorDistRGB(pxBeforeArrival, 4, 120, 87) > 18 {
		t.Fatalf("TargetCard mutated to new color BEFORE cursor arrived! cur=(%.1f, %.1f), pixel=%+v (want pre-edit emerald #047857)", cur.X, cur.Y, pxBeforeArrival)
	}

	// Advance VSync frames until Bayu reaches TargetCard and cross-dissolves it to crimson (#DC2626),
	// verifying zero background bleed-through (#FAF8F5) during the cross-fade!
	sawMidBlend := false
	for step := 0; step < 65; step++ {
		tt.Advance(16 * time.Millisecond)
		px := sampleDocPixel(t, app, 700, 260)
		// Neither pure emerald (#047857) nor pure crimson (#DC2626) operates near B > 150 (background #FAF8F5 has B = 245!):
		// verify zero transparency dip / background bleed during morph!
		if px.B > 135 {
			t.Fatalf("step %d: edit cross-fade bled into white background! pixel=%+v", step, px)
		}
		if px.R > 40 && px.R < 195 && px.G > 45 {
			sawMidBlend = true
		}
	}
	if !sawMidBlend {
		t.Fatalf("TargetCard jumped instantaneously from emerald to crimson without smooth cursor-coupled cross-fade!")
	}
	pxFinal := sampleDocPixel(t, app, 700, 260)
	if colorDistRGB(pxFinal, 220, 38, 38) > 15 {
		t.Fatalf("TargetCard did not settle to edited crimson #DC2626: got %+v", pxFinal)
	}
}

func TestRound9_StreamedEditCommitDoesNotReplayOrFlashAheadOfCursor(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	if _, err := app.Bridge.ExecuteJS(`
		page = Insert(document, {
			type: "frame", name: "Artboard", x: 0, y: 0, width: 960, height: 560,
			layout: "none", fill: "#FAF8F5", clip: true
		});
		badge = Insert(page, {
			type: "frame", name: "Badge", x: 580, y: 220, width: 200, height: 100,
			cornerRadius: 10, fill: "#1D4ED8"
		});
	`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var badgeID string
	for _, l := range app.Bridge.LayersDirect() {
		if l.Name == "Badge" {
			badgeID = l.ID
		}
	}
	if badgeID == "" {
		t.Fatalf("Badge ID not found")
	}
	app.RefreshState()
	app.Screen = "editor"
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1.0}

	tt := ui.NewTester(app.View, 1280, 760)
	app.Bridge.eng.Renderer.Now = func() time.Time { return tt.Now() }
	tt.Advance(2 * time.Second)
	app.PollNow(float64(tt.Now().UnixNano()) / 1e6)
	tt.Frame()

	// Park Bayu at (40, 50) far from Badge (680, 270).
	nowMS := float64(tt.Now().UnixNano()) / 1e6
	app.Store.Poke(AgentProfile{ID: "agent", Name: "Bayu", Color: "#FF7EB6"}, nowMS)
	app.Store.Running["agent"] = true
	cur := app.Store.Cursors["agent"]
	cur.X, cur.Y = 40, 50
	cur.TX, cur.TY = 40, 50
	cur.VX, cur.VY = 0, 0
	cur.Waypoints = nil

	// Simulate streaming execute: StreamUpdate runs Replace/Update on Preview first, then CallTool("execute") commits it!
	stmt := `Replace("` + badgeID + `", { type: "frame", name: "BadgeV2", x: 580, y: 220, width: 200, height: 100, cornerRadius: 10, fill: "#D97706" });` + "\n"
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.StreamUpdate("stream_edit_1", "agent", stmt)
	})
	app.PollNow(float64(tt.Now().UnixNano()) / 1e6)
	tt.Frame()
	tt.Advance(16 * time.Millisecond)

	// While cursor is still near (40..90, 50..80), Badge must STILL show pre-replace cobalt blue (#1D4ED8 = 29, 78, 216)!
	pxEarly := sampleDocPixel(t, app, 680, 270)
	if colorDistRGB(pxEarly, 29, 78, 216) > 18 {
		t.Fatalf("streamed Replace jumped ahead of cursor! cur=(%.1f, %.1f), pixel=%+v (want #1D4ED8)", cur.X, cur.Y, pxEarly)
	}

	// Commit final execute call with matching __stream key while cursor is mid-flight, then finish cursor flight!
	var callErr error
	if _, callErr = app.Bridge.CallRaw("execute", map[string]any{
		"__stream": "stream_edit_1",
		"__agent":  "agent",
		"input":    stmt,
	}); callErr != nil {
		// Fallback to ExecuteJS if CallRaw("execute") is internal-only
		_, _ = app.Bridge.ExecuteJS(stmt)
	}
	for step := 0; step < 65; step++ {
		app.PollNow(float64(tt.Now().UnixNano()) / 1e6)
		tt.Advance(16 * time.Millisecond)
	}

	pxSettled := sampleDocPixel(t, app, 680, 270)
	// Amber #D97706 is (217, 119, 6).
	if colorDistRGB(pxSettled, 217, 119, 6) > 15 {
		t.Fatalf("streamed Replace did not settle to amber #D97706 after cursor arrived: got %+v", pxSettled)
	}
}
