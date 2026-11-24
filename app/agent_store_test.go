package main

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestAgentStoreQueueingHeadersAndRetention(t *testing.T) {
	s := NewAgentStore()
	mainAgent := map[string]any{"id": "agent", "name": "Designer", "color": "#FF6A3D"}
	subAgent := map[string]any{"id": "sub-1", "name": "Helper", "color": "#3B82F6", "parent": "agent"}

	// Queued -> dequeued produces discard note
	s.ApplyEvents([]map[string]any{
		{"type": "agent", "kind": "queued", "text": "First queued"},
		{"type": "agent", "kind": "dequeued"},
	}, 100)
	if len(s.Items) != 1 || s.Items[0].Kind != "note" || s.Items[0].Text != "Queued message discarded." {
		t.Fatalf("expected queued discard note, got %+v", s.Items)
	}

	// Multi-queued coalescing + "Let it cook" substring safety
	s.ResetChat()
	cookProse := `Please refine the hero card — do not literally treat "Let it cook" in prose as a control token.`
	s.ApplyEvents([]map[string]any{
		{"type": "agent", "kind": "queued", "text": "Part A"},
		{"type": "agent", "kind": "queued", "text": cookProse},
		{"type": "agent", "kind": "user", "text": "Part A\n\n" + cookProse},
	}, 200)
	if len(s.Items) != 1 || s.Items[0].Kind != "user" || !strings.Contains(s.Items[0].Text, "Let it cook") {
		t.Fatalf("expected coalesced user item preserving prose, got %+v", s.Items)
	}

	// Reply headers and authors across thinking -> tool -> assistant -> concurrent subagent -> message_end -> main agent
	s.ApplyEvents([]map[string]any{
		{"type": "agent", "agent": mainAgent, "kind": "start", "backend": "api", "model": "claude-sonnet-4-6"},
		{"type": "agent", "agent": mainAgent, "kind": "thinking_delta", "text": "Planning layout..."},
		{"type": "agent", "agent": mainAgent, "kind": "tool_start", "id": "t1", "name": "execute"},
		{"type": "agent", "agent": mainAgent, "kind": "tool_progress", "id": "t1", "lines": 14},
		{"type": "agent", "agent": mainAgent, "kind": "tool_done", "id": "t1", "name": "execute", "output": "ok", "images": []any{"/9j/4AAQ"}},
		{"type": "agent", "agent": mainAgent, "kind": "text_delta", "text": "Created the hero frame."},
		{"type": "agent", "agent": subAgent, "kind": "start", "backend": "api", "model": "claude-haiku-4-5"},
		{"type": "agent", "agent": subAgent, "kind": "text_delta", "text": "Checked contrast."},
		// Concurrent delta before message_end appends to mainAgent's active stream rather than tearing it in half
		{"type": "agent", "agent": mainAgent, "kind": "text_delta", "text": " Still streaming."},
		{"type": "agent", "agent": mainAgent, "kind": "message_end"},
		// Next turn message after subAgent opens a fresh reply header
		{"type": "agent", "agent": mainAgent, "kind": "text_delta", "text": "Finished polish."},
	}, 500)

	if s.Items[3].Text != "Created the hero frame. Still streaming." {
		t.Fatalf("concurrent delta should append to active stream, got %q", s.Items[3].Text)
	}

	// Verify headers: mainAgent's thinking has Header=true, tool & first assistant have Header=false,
	// subAgent has Header=true, mainAgent after message_end & subAgent has Header=true!
	var hdrs []bool
	for _, it := range s.Items[1:] {
		hdrs = append(hdrs, it.Header)
	}
	wantHdrs := []bool{true, false, false, true, true}
	if len(hdrs) != len(wantHdrs) {
		t.Fatalf("unexpected item count %d, want %d", len(hdrs), len(wantHdrs))
	}
	for i := range wantHdrs {
		if hdrs[i] != wantHdrs[i] {
			t.Fatalf("item %d Header=%v, want %v (kind=%s agent=%s)", i, hdrs[i], wantHdrs[i], s.Items[i+1].Kind, s.Items[i+1].Agent.ID)
		}
	}
	if s.Items[1].By == nil || s.Items[1].By.Model != "claude-sonnet-4-6" {
		t.Fatalf("missing ReplyAuthor on header item: %+v", s.Items[1].By)
	}
	if len(s.Items[2].Images) != 1 || ImageDataURI(s.Items[2].Images[0]) != "data:image/jpeg;base64,/9j/4AAQ" {
		t.Fatalf("tool image data URI mismatch: %+v", s.Items[2].Images)
	}

	// BarAgents: sub-1 and agent are both running; finish sub-1 so running agent comes first
	lastContent := s.Items[len(s.Items)-1].ID // "Finished polish."
	s.ApplyEvents([]map[string]any{
		{"type": "agent", "agent": subAgent, "kind": "done", "durationMs": 420},
	}, 900)
	bar := s.BarAgents(8)
	if len(bar.All) != 2 || len(bar.Running) != 1 || bar.Shown[0].ID != "agent" {
		t.Fatalf("BarAgents unexpected ordering: %+v", bar)
	}

	// LastContentID skips trailing usage & note
	s.ApplyEvents([]map[string]any{
		{"type": "agent", "kind": "note", "text": "Saved checkpoint"},
	}, 950)
	if got := LastContentID(s.Items); got != lastContent {
		t.Fatalf("LastContentID=%d, want %d", got, lastContent)
	}

	// RowTickMS & ItemSpinning / ItemChanging
	if RowTickMS(true, true, true, 50, 16) != 0 {
		t.Fatal("RowTickMS must return 0 when reduced motion is enabled")
	}
	if RowTickMS(true, false, false, 50, 16) != 16 || RowTickMS(false, true, false, 50, 16) != 50 {
		t.Fatal("RowTickMS interval mismatch")
	}

	// 1,002-item retention check
	s.ResetChat()
	many := make([]map[string]any, 0, 1002)
	for i := 0; i < 1002; i++ {
		many = append(many, map[string]any{
			"type": "agent",
			"kind": "note",
			"text": fmt.Sprintf("note-%d", i),
		})
	}
	s.ApplyEvents(many, 1000)
	if len(s.Items) != 1002 || s.Items[0].Text != "note-0" || s.Items[1001].Text != "note-1001" {
		t.Fatalf("expected all 1002 items retained intact, got %d", len(s.Items))
	}
}

func TestAgentStoreTodosSettlementAndCrashRestore(t *testing.T) {
	s := NewAgentStore()
	mainAgent := map[string]any{"id": "agent", "name": "Designer", "color": "#FF6A3D"}
	helper := map[string]any{"id": "helper", "name": "Helper", "color": "#4ADE80", "parent": "agent"}

	s.ApplyEvents([]map[string]any{
		{"type": "agent", "agent": mainAgent, "kind": "start"},
		{"type": "agent", "agent": mainAgent, "kind": "todos", "todos": []any{
			map[string]any{"content": "Hero", "status": "completed"},
			map[string]any{"content": "Pricing", "status": "in_progress", "activeForm": "Building pricing"},
		}},
		// Helper stopping must NOT cancel mainAgent's plan!
		{"type": "agent", "agent": helper, "kind": "start"},
		{"type": "agent", "agent": helper, "kind": "done", "stopped": true, "durationMs": 100},
	}, 100)
	if s.Todos[1].Status != "in_progress" {
		t.Fatalf("helper stop should not cancel owner's plan, got %s", s.Todos[1].Status)
	}

	// Owner stopping cancels unfinished todos while keeping completed ones
	s.ApplyEvents([]map[string]any{
		{"type": "agent", "agent": mainAgent, "kind": "done", "stopped": true, "durationMs": 300},
	}, 400)
	if s.Todos[0].Status != "completed" || s.Todos[1].Status != "cancelled" {
		t.Fatalf("owner stop should cancel incomplete todos: %+v", s.Todos)
	}
	if RunVerb(true, false) != "stopped after" || RunVerb(false, true) != "failed after" || RunVerb(false, false) != "designed for" {
		t.Fatal("RunVerb mismatch")
	}

	// Crash recovery via RestoreChat settles streaming items, running tools, and unfinished todos
	crashLog := []map[string]any{
		{"at": 100.0, "type": "agent", "agent": mainAgent, "kind": "start"},
		{"at": 120.0, "type": "agent", "kind": "queued", "text": "stale queued"},
		{"at": 150.0, "type": "agent", "agent": mainAgent, "kind": "todos", "todos": []any{
			map[string]any{"content": "Step 1", "status": "completed"},
			map[string]any{"content": "Step 2", "status": "in_progress"},
		}},
		{"at": 200.0, "type": "agent", "agent": mainAgent, "kind": "tool_start", "id": "t99", "name": "execute"},
		{"at": 250.0, "type": "agent", "agent": mainAgent, "kind": "text_delta", "text": "Mid-flight"},
	}
	s.RestoreChat(crashLog)
	if len(s.Running) != 0 {
		t.Fatalf("RestoreChat must clear Running map, got %+v", s.Running)
	}
	for _, it := range s.Items {
		if it.Kind == "queued" {
			t.Fatal("RestoreChat must strip stale queued items")
		}
		if ItemChanging(it) {
			t.Fatalf("RestoreChat left item in changing state: %+v", it)
		}
	}
	if s.Todos[0].Status != "completed" || s.Todos[1].Status != "cancelled" {
		t.Fatalf("RestoreChat must cancel mid-crash todos: %+v", s.Todos)
	}
}

func TestAgentStorePresenceMotionAndCursors(t *testing.T) {
	s := NewAgentStore()
	s.ViewCenter = Pt{500, 400}
	p := AgentProfile{ID: "a1", Name: "Aria", Color: "#FF6A3D"}
	s.Poke(p, 1000)

	c := s.Cursors["a1"]
	if c == nil {
		t.Fatal("expected Poke to spawn cursor")
	}
	// Fade-in at t=1100 (half of 200ms)
	opIn := CursorOpacity(c, 1100, false)
	if opIn <= 0 || opIn >= 1 {
		t.Fatalf("expected partial fade-in opacity at 100ms, got %v", opIn)
	}
	// Full opacity at t=2000
	if got := CursorOpacity(c, 2000, false); got != 1 {
		t.Fatalf("expected opacity 1 at 2000ms, got %v", got)
	}
	// Mid fade-out at t=1000 + 5000 + 750 = 6750
	opFade := CursorOpacity(c, 6750, false)
	if opFade <= 0 || opFade >= 1 {
		t.Fatalf("expected partial fade-out opacity at 6750ms, got %v", opFade)
	}
	// WakeCursor during partial fade-out preserves exact current opacity without jump!
	s.WakeCursor("a1", c, 6750)
	opWoken := CursorOpacity(c, 6750, false)
	if math.Abs(opWoken-opFade) > 1e-6 {
		t.Fatalf("WakeCursor jumped opacity from %v to %v", opFade, opWoken)
	}
	// Faded after GoneMS (6500ms idle)
	if !s.CursorFaded("a1", c, 6750+GoneMS+10) {
		t.Fatal("expected cursor to report faded after GoneMS")
	}

	// NeedsFrame under normal vs reduced motion
	s.ClearPresence()
	if s.NeedsFrame(1000, false, 0, 1000, true) {
		t.Fatal("empty store should not need frame")
	}
	// Live placeholder node animates in normal mode, stands still in reduced motion!
	if !s.NeedsFrame(1000, false, 1, 1000, true) {
		t.Fatal("live node should request frames when reduced=false")
	}
	if s.NeedsFrame(1000, true, 1, 1000, true) {
		t.Fatal("live node must NOT spin at 60fps when reduced=true")
	}

	// ImageDataURI formats
	if got := ImageDataURI("UklGRAAA"); !strings.HasPrefix(got, "data:image/webp;base64,") {
		t.Fatalf("expected webp data URI, got %s", got)
	}
	if got := ImageDataURI("R0lGODlh"); !strings.HasPrefix(got, "data:image/gif;base64,") {
		t.Fatalf("expected gif data URI, got %s", got)
	}
	if got := ImageDataURI("iVBORw0K"); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected png data URI, got %s", got)
	}
}
