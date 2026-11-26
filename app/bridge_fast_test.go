package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/naddrzz/nalarupa/core-go/engine"
)

func TestBridgeDirectAccessorsMatchJSONDispatch(t *testing.T) {
	b := NewBridge(t.TempDir())
	defer b.Shutdown()

	var inserted map[string]any
	if err := b.Call("insert", map[string]any{
		"node": map[string]any{
			"type":         "frame",
			"name":         "Hero",
			"x":            100,
			"y":            80,
			"width":        400,
			"height":       240,
			"cornerRadius": 16,
			"placeholder":  true,
		},
	}, &inserted); err != nil {
		t.Fatalf("insert frame: %v", err)
	}
	frameID, _ := inserted["id"].(string)

	var child map[string]any
	if err := b.Call("insert", map[string]any{
		"parent": frameID,
		"node": map[string]any{
			"type":         "rectangle",
			"name":         "Card",
			"x":            20,
			"y":            30,
			"width":        120,
			"height":       60,
			"cornerRadius": []float64{4, 8, 12, 16},
		},
	}, &child); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	rectID, _ := child["id"].(string)

	// State parity
	var jsonState CoreState
	if err := b.Call("state", nil, &jsonState); err != nil {
		t.Fatalf("call state: %v", err)
	}
	if got := b.CoreStateDirect(); !reflect.DeepEqual(got, jsonState) {
		t.Fatalf("CoreStateDirect mismatch:\ngot:  %+v\nwant: %+v", got, jsonState)
	}

	// Frames parity
	var jsonFrames []Box
	if err := b.Call("frames", nil, &jsonFrames); err != nil {
		t.Fatalf("call frames: %v", err)
	}
	if got := b.FramesDirect(); !reflect.DeepEqual(got, jsonFrames) {
		t.Fatalf("FramesDirect mismatch:\ngot:  %+v\nwant: %+v", got, jsonFrames)
	}

	// Boxes parity
	ids := []string{frameID, rectID}
	var jsonBoxes []Box
	if err := b.Call("boxes", map[string]any{"ids": ids}, &jsonBoxes); err != nil {
		t.Fatalf("call boxes: %v", err)
	}
	if got := b.BoxesDirect(ids); !reflect.DeepEqual(got, jsonBoxes) {
		t.Fatalf("BoxesDirect mismatch:\ngot:  %+v\nwant: %+v", got, jsonBoxes)
	}

	// ChildrenBoxes parity
	var jsonChildren []Box
	if err := b.Call("children_boxes", map[string]any{"id": frameID}, &jsonChildren); err != nil {
		t.Fatalf("call children_boxes: %v", err)
	}
	if got := b.ChildrenBoxesDirect(frameID); !reflect.DeepEqual(got, jsonChildren) {
		t.Fatalf("ChildrenBoxesDirect mismatch:\ngot:  %+v\nwant: %+v", got, jsonChildren)
	}

	// Live parity
	var jsonLive []LiveNode
	if err := b.Call("live", nil, &jsonLive); err != nil {
		t.Fatalf("call live: %v", err)
	}
	if got := b.LiveDirect(); !reflect.DeepEqual(got, jsonLive) {
		t.Fatalf("LiveDirect mismatch:\ngot:  %+v\nwant: %+v", got, jsonLive)
	}

	// Hit parity (center of rect inside Hero: 100+20+60=180, 80+30+30=140)
	var jsonHit []string
	if err := b.Call("hit", map[string]any{"x": 180.0, "y": 140.0}, &jsonHit); err != nil {
		t.Fatalf("call hit: %v", err)
	}
	if got := b.HitDirect(180.0, 140.0); !reflect.DeepEqual(got, jsonHit) {
		t.Fatalf("HitDirect mismatch: got %v, want %v", got, jsonHit)
	}

	// Corner radii parity
	if r, eq := b.NodeCornerRadiiDirect(rectID); !reflect.DeepEqual(r, [4]float64{4, 8, 12, 16}) || eq {
		t.Fatalf("NodeCornerRadiiDirect(%s) = (%v, %v), want ([4 8 12 16], false)", rectID, r, eq)
	}
	if r, eq := b.NodeCornerRadiiDirect(frameID); !reflect.DeepEqual(r, [4]float64{16, 16, 16, 16}) || !eq {
		t.Fatalf("NodeCornerRadiiDirect(%s) = (%v, %v), want ([16 16 16 16], true)", frameID, r, eq)
	}

	// PollFast drains emitted events cleanly.
	b.Engine().Locked(func() {
		b.Engine().Emit(engine.Object("type", "ping", "id", "evt-1"))
	})
	poll := b.PollFast()
	if len(poll.Events) != 1 || poll.Events[0]["type"] != "ping" {
		t.Fatalf("PollFast first call events = %+v, want ping event", poll.Events)
	}
	pollEmpty := b.PollFast()
	if len(pollEmpty.Events) != 0 {
		t.Fatalf("PollFast second call should have 0 events, got %+v", pollEmpty.Events)
	}
}

func TestFrameScheduleAndDragCoalescing(t *testing.T) {
	store := NewAgentStore()
	now := 10000.0

	// Visible cursor while running locks to hardware VSync Painter.AnimationFrame (true, 0)
	// (lightweight GPU canvas repaint with zero View(c) rebuild) so pre-design scouting and glides never stutter.
	store.Running["main"] = true
	store.Cursors["main"] = &CursorState{
		Profile:      AgentProfile{ID: "main", Name: "Agent"},
		X:            100,
		Y:            100,
		TX:           100,
		TY:           100,
		AppearAt:     now - 1000,
		LastActivity: now - 100,
	}
	anim, after := store.FrameSchedule(now, false, 0, now, true)
	if !anim || after != 0 {
		t.Fatalf("running cursor FrameSchedule = (%v, %v), want (true, 0)", anim, after)
	}

	// Once the agent stops and GoneMS elapses, FrameSchedule drops to (false, 0) for 0% idle CPU/GPU.
	store.Running["main"] = false
	store.Cursors["main"].LastActivity = now - GoneMS - 100
	anim, after = store.FrameSchedule(now, false, 0, now, true)
	if anim || after != 0 {
		t.Fatalf("faded idle cursor FrameSchedule = (%v, %v), want (false, 0)", anim, after)
	}

	// Drag coalescing flushes pending event cleanly when pointer releases immediately.
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	var ins map[string]any
	if err := app.Bridge.Call("insert", map[string]any{
		"node": map[string]any{"type": "rectangle", "name": "R", "x": 10, "y": 10, "width": 50, "height": 50},
	}, &ins); err != nil {
		t.Fatalf("insert: %v", err)
	}
	app.RefreshState()
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1}
	id, _ := ins["id"].(string)
	initBoxes := app.Bridge.BoxesDirect([]string{id})
	if len(initBoxes) != 1 {
		t.Fatalf("expected 1 initial box, got %d", len(initBoxes))
	}

	app.Canvas.Move = &moveGesture{
		ids:       []string{id},
		origIDs:   []string{id},
		startSX:   20,
		startSY:   20,
		startDoc:  Pt{20, 20},
		startUnion: RectD{X: 10, Y: 10, W: 50, H: 50},
		items: []moveItemStart{{
			id:     id,
			origX:  10,
			origY:  10,
			parent: "document",
			box:    initBoxes[0],
		}},
		started:   true,
		begunDrag: false,
	}
	// Simulate immediate burst of two pointer moves inside the coalescing window using a pinned timestamp.
	app.onCanvasPointerMove(ui.InputEvent{X: 30, Y: 30})
	app.Canvas.lastDragStepAt = time.Now()
	app.onCanvasPointerMove(ui.InputEvent{X: 70, Y: 50})
	if app.Canvas.pendingDragEv == nil {
		t.Fatalf("expected second burst move within coalescing window to be coalesced into pendingDragEv")
	}
	app.onCanvasPointerUp(ui.InputEvent{X: 70, Y: 50})
	boxes := app.Bridge.BoxesDirect([]string{id})
	if len(boxes) != 1 || boxes[0].X < 58.5 || boxes[0].X > 61.5 || boxes[0].Y < 38.5 || boxes[0].Y > 41.5 {
		t.Fatalf("pending drag move was not flushed to (60, 40) on pointer up: boxes=%+v", boxes)
	}

	// Verify unsynced TryBoxesDirect preserves rotation/flip M matrix (Thread 54).
	var rotIns map[string]any
	if err := app.Bridge.Call("insert", map[string]any{
		"node": map[string]any{"type": "rectangle", "name": "Rot", "x": 100, "y": 100, "width": 80, "height": 40, "rotation": 45},
	}, &rotIns); err != nil {
		t.Fatalf("insert rotated rect: %v", err)
	}
	rotID, _ := rotIns["id"].(string)
	unsyncedBoxes, ok := app.Bridge.TryBoxesDirect([]string{rotID})
	if !ok || len(unsyncedBoxes) != 1 || unsyncedBoxes[0].M == nil {
		t.Fatalf("expected unsynced TryBoxesDirect to preserve rotation matrix M, got ok=%v boxes=%+v", ok, unsyncedBoxes)
	}
}
