package main

import (
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRound11_SpacebarGrabAndGrabbingCursorUX(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", false)
	app.Screen = "editor"

	tt := ui.NewTester(app.View, 1280, 800)
	tt.Frame()

	// Default move tool cursor must be arrow (CursorDefault)
	if got := app.computeCanvasCursor(); got != ui.CursorDefault {
		t.Fatalf("initial canvas cursor = %v, want CursorDefault", got)
	}

	// Press Space WITHOUT clicking canvas first: editor-shell must still engage SpaceHeld -> CursorGrab!
	tt.KeyDown(ui.KeySpace)
	tt.Frame()
	if !app.Canvas.SpaceHeld {
		t.Fatalf("expected SpaceHeld=true immediately on KeyDown(KeySpace) without prior canvas click")
	}
	if got := app.computeCanvasCursor(); got != ui.CursorGrab {
		t.Fatalf("cursor while holding Space = %v, want CursorGrab", got)
	}
	if got := app.nativeGrabCursorMode(); got != 1 {
		t.Fatalf("nativeGrabCursorMode while holding Space = %d, want 1 (open-hand grab)", got)
	}

	// Start dragging on canvas while Space is held -> must transition to CursorGrabbing (mode=2) and pan camera!
	origCamX := app.Cam.X
	tt.Press(600, 400)
	tt.Frame()
	if got := app.computeCanvasCursor(); got != ui.CursorGrabbing {
		t.Fatalf("cursor while Space+dragging = %v, want CursorGrabbing", got)
	}
	if got := app.nativeGrabCursorMode(); got != 2 {
		t.Fatalf("nativeGrabCursorMode while Space+dragging = %d, want 2 (closed-hand grabbing)", got)
	}
	tt.Move(520, 360)
	tt.Frame()
	if app.Cam.X <= origCamX {
		t.Fatalf("expected Space+drag left to pan camera right, origCamX=%.1f newCamX=%.1f", origCamX, app.Cam.X)
	}

	// Release pointer while Space is still held -> back to CursorGrab (mode=1)
	tt.Release(520, 360)
	tt.Frame()
	if got := app.computeCanvasCursor(); got != ui.CursorGrab {
		t.Fatalf("cursor after pointer release with Space still held = %v, want CursorGrab", got)
	}

	// Release Space -> back to CursorDefault (mode=0)
	tt.KeyUp(ui.KeySpace)
	tt.Frame()
	if app.Canvas.SpaceHeld {
		t.Fatalf("expected SpaceHeld=false after KeyUp(KeySpace)")
	}
	if got := app.computeCanvasCursor(); got != ui.CursorDefault {
		t.Fatalf("cursor after releasing Space = %v, want CursorDefault", got)
	}

	if runtime.GOOS == "windows" {
		hGrab, hGrabbing := ExpectedNativeGrabCursors()
		if hGrab == 0 || hGrabbing == 0 || hGrab == hGrabbing {
			t.Fatalf("expected distinct valid Win32 HCURSOR handles for grab/grabbing, got grab=%#x grabbing=%#x", hGrab, hGrabbing)
		}
	}
}

func TestRound11_NewScreensAlignFlushAndWrapConditionallyToRowsBelow(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", false)

	// Turn 1: Create reusable component at y=-360 plus 2 screens ("Ledger", "Night") at y=0.
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.BeginTurnSnapshot()
		app.Bridge.eng.Agent.Running = true
	})
	if _, err := app.Bridge.ExecuteJS(`
		comp = Insert(document, { type: "frame", name: "Top Nav Comp", reusable: true, x: 0, y: -360, width: 1440, height: 72, fill: "#0F172A" });
		s1 = Insert(document, { type: "frame", name: "Dashboard — Ledger", x: 0, y: 0, width: 1440, height: 900, fill: "#FAF8F5", children: [
			{ type: "rectangle", width: 200, height: 80, fill: "#18181B" }
		]});
		spot2 = FindEmptySpace({ width: 1440, height: 900, direction: "right", padding: 120 });
		s2 = Insert(document, { type: "frame", name: "Dashboard — Night", x: spot2.x, y: spot2.y, width: 1440, height: 900, fill: "#0B0F19", children: [
			{ type: "rectangle", width: 200, height: 80, fill: "#22C55E" }
		]});
	`); err != nil {
		t.Fatalf("Turn 1 ExecuteJS: %v", err)
	}
	app.Bridge.eng.Locked(func() { app.Bridge.eng.Agent.Running = false })

	// Turn 2: Add 2 more screens ("Daylight", "Swiss") even with crooked y=-240 -> Row 1 still has room (2+2=4 <= 4),
	// so ALL 4 screens MUST sit 100% flush (sejajar) at y == 0!
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.BeginTurnSnapshot()
		app.Bridge.eng.Agent.Running = true
	})
	if _, err := app.Bridge.ExecuteJS(`
		s3 = Insert(document, { type: "frame", name: "Dashboard — Daylight", x: 3400, y: -240, width: 1440, height: 900, fill: "#FFFFFF", children: [
			{ type: "rectangle", width: 200, height: 80, fill: "#3B82F6" }
		]});
		spot4 = FindEmptySpace({ width: 1440, height: 900, direction: "right", padding: 120 });
		s4 = Insert(document, { type: "frame", name: "Dashboard — Swiss", x: spot4.x, y: spot4.y, width: 1440, height: 900, fill: "#F8FAFC", children: [
			{ type: "rectangle", width: 200, height: 80, fill: "#EF4444" }
		]});
	`); err != nil {
		t.Fatalf("Turn 2 ExecuteJS: %v", err)
	}
	app.Bridge.eng.Locked(func() { app.Bridge.eng.Agent.Running = false })

	byName := map[string]Box{}
	for _, f := range app.Bridge.FramesDirect() {
		byName[f.Name] = f
	}
	for _, name := range []string{"Dashboard — Ledger", "Dashboard — Night", "Dashboard — Daylight", "Dashboard — Swiss"} {
		b, ok := byName[name]
		if !ok {
			t.Fatalf("missing screen %q", name)
		}
		if b.Y != 0 {
			t.Fatalf("Row 1 screen %q is NOT flush (sejajar) with Row 1! got Y=%.1f, want Y=0", name, b.Y)
		}
	}

	// Turn 3 (Reproduces Gambar 2!): Row 1 now has 4 screens (4 columns full). Generating 4 MORE screens
	// MUST wrap conditionally to Row 2 below (Y > 900, starting back at X == 0), all 4 flush on Row 2's Y!
	app.Bridge.eng.Locked(func() {
		app.Bridge.eng.BeginTurnSnapshot()
		app.Bridge.eng.Agent.Running = true
	})
	if _, err := app.Bridge.ExecuteJS(`
		for (let k = 5; k <= 8; k++) {
			const sp = FindEmptySpace({ width: 1440, height: 900, direction: "right", padding: 120 });
			Insert(document, { type: "frame", name: "Dashboard — Var " + k, x: sp.x, y: sp.y - 90, width: 1440, height: 900, placeholder: true, fill: "#111827" });
		}
	`); err != nil {
		t.Fatalf("Turn 3 ExecuteJS: %v", err)
	}
	app.Bridge.eng.Locked(func() { app.Bridge.eng.Agent.Running = false })

	byName = map[string]Box{}
	allFrames := app.Bridge.FramesDirect()
	for _, f := range allFrames {
		byName[f.Name] = f
	}
	b5 := byName["Dashboard — Var 5"]
	if b5.X != 0 {
		t.Fatalf("expected 5th screen to wrap conditionally to left column X=0 below Row 1, got X=%.1f", b5.X)
	}
	if b5.Y < 1100 {
		t.Fatalf("expected 5th screen to wrap below Row 1 (Y >= 1100), got Y=%.1f", b5.Y)
	}
	for _, name := range []string{"Dashboard — Var 6", "Dashboard — Var 7", "Dashboard — Var 8"} {
		bk := byName[name]
		if bk.Y != b5.Y {
			t.Fatalf("Row 2 screen %q not flush with Row 2 Y=%.1f: got Y=%.1f", name, b5.Y, bk.Y)
		}
		if bk.X <= b5.X {
			t.Fatalf("Row 2 screen %q expected to the right of Var 5 (X=%.1f), got X=%.1f", name, b5.X, bk.X)
		}
	}
	for i := 0; i < len(allFrames); i++ {
		for j := i + 1; j < len(allFrames); j++ {
			if boxesOverlap(allFrames[i], allFrames[j]) {
				t.Fatalf("overlap between %q and %q", allFrames[i].Name, allFrames[j].Name)
			}
		}
	}
}

func TestRound11_CleanWhiteCanvasBackground(t *testing.T) {
	if CanvasBG != [3]uint8{0xff, 0xff, 0xff} {
		t.Fatalf("CanvasBG = %v, want pure clean white [255 255 255]", CanvasBG)
	}
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()
	app.StartTemplate("empty", false)
	app.Screen = "editor"

	tt := ui.NewTester(app.View, 1280, 800)
	tt.Frame()
	tt.Frame()

	px := sampleDocPixel(t, app, 200, 200)
	if px.R != 255 || px.G != 255 || px.B != 255 {
		t.Fatalf("empty canvas pixel = %+v, want clean white (255,255,255)", px)
	}
}
