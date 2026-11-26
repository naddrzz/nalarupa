package main

import (
	"image"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestProgressiveRevealOnMonolithicNestedInsertAndVSyncSpringCursor(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	var curNow time.Time
	baseTime := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	curNow = baseTime
	app.Bridge.eng.Renderer.Now = func() time.Time { return curNow }

	// Simulate an LLM or buffered provider delivering a monolithic nested Insert(document, { children: [...] }) all at once!
	src := `
		page = Insert(document, {
			type: "frame", name: "Landing", x: 0, y: 0, width: 800, height: 600,
			layout: "vertical", padding: 32, gap: 24, fill: "#F5EFE4", clip: true,
			children: [
				{ type: "frame", name: "Header", width: "fill_container", height: 56, fill: "#1E3A2F" },
				{ type: "frame", name: "Hero", width: "fill_container", height: 140, fill: "#B63B24" },
				{
					type: "frame", name: "CardRow", width: "fill_container", layout: "horizontal", gap: 16,
					children: [
						{ type: "frame", name: "Card1", width: "fill_container", height: 180, fill: "#1E3A2F" },
						{ type: "frame", name: "Card2", width: "fill_container", height: 180, fill: "#B63B24" },
						{ type: "frame", name: "Card3", width: "fill_container", height: 180, fill: "#1F2E4A" },
						{ type: "frame", name: "Card4", width: "fill_container", height: 180, fill: "#CAA55E" }
					]
				}
			]
		});
	`
	if _, err := app.Bridge.ExecuteJS(src); err != nil {
		t.Fatalf("ExecuteJS failed: %v", err)
	}

	// 1. Even for a monolithic nested Insert into document, progressive slides must be scheduled for its inner elements!
	if !app.Bridge.HasSlidesDirect() {
		t.Fatalf("expected HasSlidesDirect() == true after monolithic nested Insert so content reveals progressively instead of popping in all at once")
	}

	// At t = +15ms: Card4 (the last card in the grid) must still be waiting on its stagger delay (showing #F5EFE4 ground),
	// while later at t = +1500ms Card4 has smoothly slid & faded into place (#CAA55E)!
	var earlyRGBA, settledRGBA *image.RGBA
	curNow = baseTime.Add(15 * time.Millisecond)
	app.Bridge.RenderInto(&earlyRGBA, 800, 600, 0, 0, 1.0, CanvasBG, "", false)

	var card4Box *Box
	for _, l := range app.Bridge.LayersDirect() {
		if l.Name == "Card4" {
			bs := app.Bridge.BoxesDirect([]string{l.ID})
			if len(bs) == 1 {
				b := bs[0]
				card4Box = &b
			}
		}
	}
	if card4Box == nil {
		t.Fatalf("Card4 box not found")
	}
	cx, cy := int(card4Box.X+card4Box.W/2), int(card4Box.Y+card4Box.H/2)
	earlyPx := earlyRGBA.RGBAAt(cx, cy)
	// #F5EFE4 is (245, 239, 228); #CAA55E is (202, 165, 94)
	if earlyPx.R < 230 || earlyPx.B < 200 {
		t.Fatalf("at t=+15ms Card4 already popped in at (%d,%d): got RGBA=%+v, want page ground #F5EFE4 before stagger turn", cx, cy, earlyPx)
	}

	curNow = baseTime.Add(1500 * time.Millisecond)
	app.Bridge.RenderInto(&settledRGBA, 800, 600, 0, 0, 1.0, CanvasBG, "", true)
	settledPx := settledRGBA.RGBAAt(cx, cy)
	if settledPx.R > 220 || settledPx.B > 130 {
		t.Fatalf("at t=+1500ms Card4 did not settle to #CAA55E at (%d,%d): got RGBA=%+v", cx, cy, settledPx)
	}

	// 2. Poll events & verify cursor schedules VSync AnimationFrame (animFrame == true) and glides continuously!
	app.Screen = "editor"
	tt := ui.NewTester(app.View, 640, 400)
	nowMS := float64(tt.Now().UnixNano()) / 1e6
	app.Store.Running["agent"] = true
	app.PollNow(nowMS)
	app.consumeCanvasActivity(nowMS)

	cur := app.Store.Cursors["agent"]
	if cur == nil {
		t.Fatalf("expected agent cursor to be created from monolithic nested Insert activity")
	}
	if len(cur.Waypoints) < 3 {
		t.Fatalf("expected subtree waypoints for progressive build choreography, got %d waypoints", len(cur.Waypoints))
	}
	animFrame, _ := app.Store.FrameSchedule(nowMS, false, 0, nowMS, true)
	if !animFrame {
		t.Fatalf("expected active moving cursor FrameSchedule to request VSync AnimationFrame (animFrame=true), got false")
	}

	// Drive real VSync Painter.AnimationFrame() repaints via tt.Advance across 24 frames and ensure cursor never stalls mid-waypoint queue!
	tt.Frame()
	stalledFramesWhileQueued := 0
	prevX, prevY := cur.X, cur.Y
	for step := 1; step <= 24; step++ {
		tt.Advance(17 * time.Millisecond)
		dist := math.Hypot(cur.X-prevX, cur.Y-prevY)
		if len(cur.Waypoints) > 0 && step > 2 && dist < 0.05 {
			stalledFramesWhileQueued++
		}
		prevX, prevY = cur.X, cur.Y
	}
	if stalledFramesWhileQueued > 2 {
		t.Fatalf("cursor dead-stopped for %d frames while waypoints remained queued (choppy stop-and-go motion!)", stalledFramesWhileQueued)
	}
}

func TestDualLayerOverviewAndOverscanPreventZoomOutBlankEdges(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	// Create two vivid frames far apart: Page 1 at x=0 (#1E3A2F) and Page 2 at x=1600 (#B63B24).
	if _, err := app.Bridge.ExecuteJS(`
		Insert(document, { type: "frame", name: "Page 1", x: 0, y: 0, width: 1200, height: 900, fill: "#1E3A2F" });
		Insert(document, { type: "frame", name: "Page 2", x: 1600, y: 0, width: 1200, height: 900, fill: "#B63B24" });
	`); err != nil {
		t.Fatalf("ExecuteJS: %v", err)
	}
	app.RefreshState()

	// Start zoomed in tightly on Page 1 so Page 2 (x=1600..2800) is completely outside the visible viewport!
	app.Screen = "editor"
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1.0}

	tt := ui.NewTester(app.View, 1280, 800)
	tt.Frame()

	// Check 1: Viewport detail render must include overscan margin (> canvas pane width) so panning/zooming out doesn't immediately hit a wall!
	paneW := int(math.Round(app.CanvasW))
	if app.Canvas.RGBA == nil || app.Canvas.RGBA.Bounds().Dx() <= paneW {
		gotW := 0
		if app.Canvas.RGBA != nil {
			gotW = app.Canvas.RGBA.Bounds().Dx()
		}
		t.Fatalf("expected overscanned detail raster width > paneW (%d), got %d (0px overscan causes edge pop-in during pan/zoom!)", paneW, gotW)
	}

	// Check 2: Whole-document overview backing must cover off-screen Page 2 (x=2200, y=450 -> #B63B24) immediately!
	docRect := app.Canvas.OverviewDocRect
	if docRect.W <= 0 || !(2200 >= docRect.X && 2200 <= docRect.X+docRect.W) {
		t.Fatalf("expected Canvas.OverviewDocRect to cover off-screen Page 2 at x=2200, got %+v", docRect)
	}
	if app.Canvas.OverviewRGBA == nil || app.Canvas.OverviewBitmap == nil {
		t.Fatalf("expected OverviewRGBA and OverviewBitmap to be initialized")
	}
	ovB := app.Canvas.OverviewRGBA.Bounds()
	px := int(math.Round((2200.0 - docRect.X) / docRect.W * float64(ovB.Dx())))
	py := int(math.Round((450.0 - docRect.Y) / docRect.H * float64(ovB.Dy())))
	gotCol := app.Canvas.OverviewRGBA.RGBAAt(px, py)
	if gotCol.R < 150 || gotCol.G > 90 {
		t.Fatalf("expected OverviewRGBA at off-screen Page 2 (%d,%d) to hold #B63B24, got %+v", px, py, gotCol)
	}
}
