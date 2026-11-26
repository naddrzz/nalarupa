package main

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func findLayerIDByName(layers []LayerRow, name string) string {
	for _, l := range layers {
		if l.Name == name {
			return l.ID
		}
	}
	return ""
}

func TestRound12_FlightStrip16NodeStreamedCardCollapsesToSingleCardWithZeroWireframeStack(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", false)

	// Build "Dashboard — Flight Strip" shell + Card 1 (settled) matching the user's screenshot.
	if _, err := app.Bridge.ExecuteJS(`
		screen = Insert(document, {
			type: "frame", name: "Dashboard — Flight Strip", x: 0, y: 0, width: 920, height: 640,
			layout: "vertical", fill: "#F8FAFC", placeholder: true, clip: true
		});
		hdr = Insert(screen, {
			type: "frame", name: "ManifestHeader", width: "fill_container", height: 56,
			padding: [0, 24], layout: "horizontal", justifyContent: "space_between", alignItems: "center",
			fill: "#172554"
		});
		Insert(hdr, { type: "text", content: "MILES AIR · OCTOBER MANIFEST", fontSize: 15, fontWeight: "700", fill: "#FBBF24" });
		Insert(hdr, { type: "text", content: "GATE 10 · SEAT 1A · ON TIME", fontSize: 11, fill: "#E2E8F0" });
		list = Insert(screen, {
			type: "frame", name: "StripList", width: "fill_container", layout: "vertical",
			padding: 28, gap: 18
		});
		card1 = Insert(list, {
			type: "frame", name: "CardJM101", width: "fill_container", height: 84,
			cornerRadius: 8, fill: "#EDE9FE", layout: "horizontal", padding: 14, gap: 16, alignItems: "center"
		});
		Insert(card1, { type: "frame", width: 4, height: "fill_container", cornerRadius: 2, fill: "#1E1B4B" });
		Insert(card1, { type: "text", content: "JM-101 HOUSING", fontSize: 14, fontWeight: "700", fill: "#1E293B" });
	`); err != nil {
		t.Fatalf("setup Flight Strip shell: %v", err)
	}

	listID := findLayerIDByName(app.Bridge.LayersDirect(), "StripList")
	if listID == "" {
		t.Fatalf("StripList ID not found")
	}

	app.RefreshState()
	app.Screen = "editor"
	app.Cam = Camera{X: -20, Y: -20, Zoom: 1.0}

	curNow := time.Now()
	tt := ui.NewTester(app.View, 1280, 820)
	app.Bridge.eng.Renderer.Now = func() time.Time { return curNow }

	// Settle initial shell & Card 1 instantaneously without spinning 125 frames.
	curNow = curNow.Add(4 * time.Second)
	app.PollNow(float64(curNow.UnixNano()) / 1e6)
	tt.Frame()
	app.Store.Flashes = nil
	delete(app.Store.Cursors, "agent")

	// Start agent Wulan and park her above-left of Card 2's future position.
	nowMS := float64(curNow.UnixNano()) / 1e6
	app.Store.ApplyEvents([]map[string]any{
		{
			"type": "agent",
			"kind": "start",
			"agent": map[string]any{
				"id":    "wulan",
				"name":  "Wulan",
				"color": "#60A5FA",
			},
		},
	}, nowMS)
	cur := app.Store.Cursors["wulan"]
	if cur == nil {
		t.Fatalf("expected Wulan cursor")
	}
	cur.X, cur.Y = 120, 140
	cur.TX, cur.TY = 120, 140
	cur.VX, cur.VY = 0, 0
	cur.Waypoints = nil

	// Stream Card 2 ("JM-102 GROCERIES") with 16 statements (> 12 IDs!) in a single StreamUpdate,
	// reproducing the exact >12-node Flight Strip card structure where NeedsSync() == true immediately after StreamUpdate!
	streamSrc := `
		const c2 = Insert("` + listID + `", {
			type: "frame", name: "CardJM102", width: "fill_container", height: 92,
			cornerRadius: 8, fill: "#1D4ED8", layout: "horizontal", padding: 14, gap: 18, alignItems: "center"
		});
		Insert(c2, { type: "frame", name: "AccentBar", width: 4, height: "fill_container", cornerRadius: 2, fill: "#FBBF24" });
		const left = Insert(c2, { type: "frame", layout: "vertical", gap: 4, width: 220 });
		const frow = Insert(left, { type: "frame", layout: "horizontal", gap: 6 });
		Insert(frow, { type: "text", content: "FLIGHT", fontSize: 10, fill: "#DBEAFE" });
		Insert(frow, { type: "text", content: "JM-102", fontSize: 10, fontWeight: "700", fill: "#FFFFFF" });
		Insert(left, { type: "text", content: "GROCERIES", fontSize: 15, fontWeight: "700", fill: "#FFFFFF" });
		Insert(left, { type: "text", content: "FROM WALLET · TO GROCERIES", fontSize: 10, fill: "#BFDBFE" });
		const mid = Insert(c2, { type: "frame", layout: "vertical", gap: 6, width: 180 });
		Insert(mid, { type: "text", content: "LOAD 28.4%", fontSize: 10, fill: "#DBEAFE" });
		const trk = Insert(mid, { type: "frame", width: 160, height: 4, cornerRadius: 2, fill: "#1E3A8A", layout: "horizontal" });
		Insert(trk, { type: "frame", width: 64, height: 4, cornerRadius: 2, fill: "#FBBF24" });
		const right = Insert(c2, { type: "frame", layout: "horizontal", gap: 12, alignItems: "center" });
		const priceCol = Insert(right, { type: "frame", layout: "vertical", gap: 2 });
		Insert(priceCol, { type: "text", content: "$640.00", fontSize: 16, fontWeight: "700", fill: "#FFFFFF" });
		Insert(priceCol, { type: "text", content: "CLASS: VARIABLE", fontSize: 9, fill: "#BFDBFE" });
	`
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.StreamUpdate("stream_fs_1", "wulan", streamSrc)
	})

	// Before any Sync() runs (NeedsSync() == true!), verify TrySubtreeBoxesDirect does NOT starve (ok == true)
	// and collapses all 16 card nodes into ONE cohesive CardJM102 box!
	app.PollNow(float64(curNow.UnixNano()) / 1e6)
	if len(app.Store.Activity) == 0 {
		t.Fatalf("expected write activity from StreamUpdate")
	}
	actIDs := normalizeActivityRootIDs(app.Store.Activity[0].IDs)
	if len(actIDs) < 15 {
		t.Fatalf("expected >= 15 streamed IDs in Flight Strip card batch, got %d", len(actIDs))
	}
	subBoxes, ok := app.Bridge.TrySubtreeBoxesDirect(actIDs, 64)
	if !ok {
		t.Fatalf("TrySubtreeBoxesDirect returned ok=false while NeedsSync()==true; would starve cursor choreography!")
	}
	if len(subBoxes) != 1 || subBoxes[0].Name != "CardJM102" {
		t.Fatalf("expected 16-node Flight Strip card to collapse into 1 cohesive CardJM102 box, got %d boxes: %+v", len(subBoxes), subBoxes)
	}
	cardCenterY := subBoxes[0].Y + subBoxes[0].H*0.5

	// Step VSync frames at 16ms: verify Wulan gets at most 1 waypoint, NEVER stacks >1 wireframe flash,
	// and CardJM102 (#1D4ED8 = 29, 78, 216) materializes immediately upon arrival (< 260ms total)!
	appearedStep := -1
	settledStep := -1
	for step := 1; step <= 28; step++ {
		curNow = curNow.Add(16 * time.Millisecond)
		app.PollNow(float64(curNow.UnixNano()) / 1e6)
		tt.Advance(16 * time.Millisecond)

		if len(app.Store.Flashes) > 1 {
			t.Fatalf("step %d: stacked %d NodeFlash wireframe boxes simultaneously (want <= 1): %+v", step, len(app.Store.Flashes), app.Store.Flashes)
		}
		if len(cur.Waypoints) > 1 {
			t.Fatalf("step %d: expected cohesive card to queue 0 remaining inner leaf waypoints, got %d", step, len(cur.Waypoints))
		}
		// Sample right-center of CardJM102 (away from white text on left/right so we hit clean #1D4ED8 surface)
		sampleX := subBoxes[0].X + subBoxes[0].W*0.88
		sampleY := cardCenterY
		px := sampleDocPixel(t, app, sampleX, sampleY)
		// Screen ground #F8FAFC is (248, 250, 252); CardJM102 #1D4ED8 is (29, 78, 216).
		// Measure progression specifically toward CardJM102's cobalt blue fill!
		distToBlue := colorDistRGB(px, 29, 78, 216)
		if appearedStep < 0 && distToBlue < 220 && px.B > px.R+40 {
			appearedStep = step
		}
		if settledStep < 0 && distToBlue < 16 {
			settledStep = step
		}
	}

	if appearedStep < 0 || appearedStep > 14 {
		t.Fatalf("CardJM102 took too long to appear (appearedStep=%d, >224ms) — delayed wireframe bug!", appearedStep)
	}
	if settledStep < 0 || settledStep > 24 {
		t.Fatalf("CardJM102 did not crisply settle within 380ms (settledStep=%d)", settledStep)
	}

	// Stage 2: Empty-shell-first card that settles, followed by 14 inner nodes streamed later!
	// Even when CardJM103's outer shell settled earlier, streaming 14 inner nodes must STILL collapse to CardJM103!
	shellSrc := streamSrc + `
		const c3 = Insert("` + listID + `", {
			type: "frame", name: "CardJM103", width: "fill_container", height: 92,
			cornerRadius: 8, fill: "#EDE9FE", layout: "horizontal", padding: 14, gap: 18, alignItems: "center"
		});
	`
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.StreamUpdate("stream_fs_1", "wulan", shellSrc)
	})
	curNow = curNow.Add(2 * time.Second)
	app.PollNow(float64(curNow.UnixNano()) / 1e6)
	tt.Frame()

	innerSrc := shellSrc + `
		Insert(c3, { type: "frame", width: 4, height: "fill_container", cornerRadius: 2, fill: "#4C1D95" });
		const l3 = Insert(c3, { type: "frame", layout: "vertical", gap: 4, width: 220 });
		const fr3 = Insert(l3, { type: "frame", layout: "horizontal", gap: 6 });
		Insert(fr3, { type: "text", content: "FLIGHT", fontSize: 10, fill: "#6D28D9" });
		Insert(fr3, { type: "text", content: "JM-103", fontSize: 10, fontWeight: "700", fill: "#1E1B4B" });
		Insert(l3, { type: "text", content: "DINING", fontSize: 15, fontWeight: "700", fill: "#1E1B4B" });
		Insert(l3, { type: "text", content: "FROM WALLET · TO DINING", fontSize: 10, fill: "#5B21B6" });
	`
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.StreamUpdate("stream_fs_1", "wulan", innerSrc)
	})
	app.PollNow(float64(curNow.UnixNano()) / 1e6)
	if len(app.Store.Activity) == 0 {
		t.Fatalf("expected write activity for inner nodes of CardJM103")
	}
	innerIDs := normalizeActivityRootIDs(app.Store.Activity[len(app.Store.Activity)-1].IDs)
	innerBoxes, ok2 := app.Bridge.TrySubtreeBoxesDirect(innerIDs, 64)
	if !ok2 || len(innerBoxes) != 1 || innerBoxes[0].Name != "CardJM103" {
		t.Fatalf("expected inner children of settled shell CardJM103 to collapse to 1 cohesive CardJM103 box, got %+v", innerBoxes)
	}
}

func TestRound12_NodeFlashWaitsForWorkerBitmapReleaseSeqBeforePainting(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", false)

	if _, err := app.Bridge.ExecuteJS(`
		page = Insert(document, { type: "frame", name: "Board", x: 0, y: 0, width: 600, height: 400, fill: "#FFFFFF" });
		card = Insert(page, { type: "frame", name: "Card", x: 100, y: 100, width: 240, height: 90, fill: "#2563EB" });
	`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cardID := findLayerIDByName(app.Bridge.LayersDirect(), "Card")
	boxes := app.Bridge.BoxesDirect([]string{cardID})
	if len(boxes) != 1 {
		t.Fatalf("expected Card box")
	}

	// Simulate cursor releasing a slide (bumping SlideReleaseSeqDirect to >= 1), while Canvas.LastKey is still behind (releaseSeq == 0).
	app.Bridge.QueueCursorUpdateDirect([]string{cardID}, nil)
	relSeq := app.Bridge.SlideReleaseSeqDirect()
	if relSeq == 0 {
		t.Fatalf("expected SlideReleaseSeqDirect > 0 after QueueCursorUpdateDirect with releaseIDs")
	}
	b := boxes[0]
	app.pushNodeFlashWithBox(cardID, &b, "wulan", "#60A5FA", "write", 1000.0, 260.0)
	if len(app.Store.Flashes) != 1 {
		t.Fatalf("expected 1 queued flash, got %d", len(app.Store.Flashes))
	}

	// Force Canvas.LastKey behind MinReleaseSeq and invoke drawAgentLayer via a real ui.Tester Painter frame.
	app.Canvas.LastKey = canvasRenderKey{
		version:    app.Bridge.VersionDirect(),
		releaseSeq: relSeq - 1,
		pw:         800,
		ph:         600,
		zoom:       1.0,
		dpr:        1.0,
	}
	testNowMS := 1032.0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(800).Height(600).Draw(func(p *ui.Painter, r ui.Rect) {
			app.drawAgentLayer(p, r, nil, false, testNowMS, testNowMS-16.0, app.Bridge.VersionDirect())
		})
	}, 800, 600)
	tt.Frame()
	if len(app.Store.Flashes) != 1 {
		t.Fatalf("expected flash to remain queued while waiting for worker bitmap")
	}
	if app.Store.Flashes[0].PaintStartAt != 0 {
		t.Fatalf("flash painted prematurely before Canvas.LastKey caught up! PaintStartAt=%.1f", app.Store.Flashes[0].PaintStartAt)
	}

	// Now advance Canvas.LastKey.releaseSeq to relSeq (worker swapped in the rendered frame): flash must start right at 1048ms!
	app.Canvas.LastKey.releaseSeq = relSeq
	testNowMS = 1048.0
	tt.Frame()
	if len(app.Store.Flashes) != 1 || math.Abs(app.Store.Flashes[0].PaintStartAt-1048.0) > 1e-6 {
		t.Fatalf("expected flash PaintStartAt=1048.0 on exact bitmap swap frame, got %+v", app.Store.Flashes)
	}
}
