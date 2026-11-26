package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestEmilMotionRulesDurationsAsymmetricExitAndReducedMotion(t *testing.T) {
	cases := []struct {
		name  string
		build func(reduced bool) ui.ElementTransition
	}{
		{"PopoverTransition", PopoverTransition},
		{"ChatItemTransition", ChatItemTransition},
		{"DrawerCollapseTransition", DrawerCollapseTransition},
		{"ChipTransition", ChipTransition},
		{"ToastTransition", ToastTransition},
		{"DialogBannerTransition", DialogBannerTransition},
	}

	for _, tc := range cases {
		tr := tc.build(false)
		if tr.Duration <= 0 || tr.Duration >= 300*time.Millisecond {
			t.Fatalf("%s: Duration = %v, want (0ms, 300ms) per Emil UI ceiling", tc.name, tr.Duration)
		}
		if tr.ExitDuration <= 0 || tr.ExitDuration >= tr.Duration {
			t.Fatalf("%s: ExitDuration (%v) must be positive and faster than Enter Duration (%v)", tc.name, tr.ExitDuration, tr.Duration)
		}
		if tr.Enter != nil && tr.Enter.Scale != 0 && tr.Enter.Scale < 0.94 {
			t.Fatalf("%s: Enter.Scale = %.3f, must never start near 0 (want >= 0.95)", tc.name, tr.Enter.Scale)
		}

		// Reduced motion must disable all motion & fades immediately (Duration < 0).
		trReduced := tc.build(true)
		if trReduced.Duration >= 0 || trReduced.ExitDuration >= 0 {
			t.Fatalf("%s(reduced=true): expected negative instant durations, got (%v, %v)", tc.name, trReduced.Duration, trReduced.ExitDuration)
		}
	}

	if EaseOutQuint64(0) != 0 || EaseOutQuint64(1) != 1 {
		t.Fatalf("EaseOutQuint64 endpoints invalid")
	}
	// Strong ease-out must cover >65% of travel by t=0.25 for immediate responsiveness.
	if mid := EaseOutQuint64(0.25); mid < 0.70 {
		t.Fatalf("EaseOutQuint64(0.25) = %.3f, want > 0.70 fast start", mid)
	}
}

func TestEmilStreamChatPopoversDrawingGhostAndGeneratingAnimations(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	// Insert a placeholder ("Generating: ...") frame to exercise aurora + perimeter specular beam + label transition!
	if _, err := app.Bridge.ExecuteJS(`
		Insert(document, {
			type: "frame", name: "Hero Surface", x: 40, y: 40, width: 320, height: 220,
			fill: "#F5EFE4", placeholder: true
		});
	`); err != nil {
		t.Fatalf("ExecuteJS: %v", err)
	}
	app.RefreshState()
	app.Screen = "editor"
	app.RightTab = "agent"

	tt := ui.NewTester(app.View, 800, 560)
	tt.Frame()

	nowMS := float64(tt.Now().UnixNano()) / 1e6
	// Simulate live chat stream: start -> thinking -> assistant streaming markdown -> running tool with lines -> completed tool!
	app.Store.ApplyEvents([]map[string]any{
		{"type": "agent", "kind": "start", "agent": map[string]any{"id": "bayu", "name": "Bayu", "color": "#FF7EB6"}},
		{"type": "agent", "kind": "thinking_delta", "agent": map[string]any{"id": "bayu", "name": "Bayu", "color": "#FF7EB6"}, "text": "Calibrating editorial rhythm..."},
		{"type": "agent", "kind": "text_delta", "agent": map[string]any{"id": "bayu", "name": "Bayu", "color": "#FF7EB6"}, "text": "Building **Hero Surface** with warm paper ground and forest accents."},
		{"type": "agent", "kind": "tool_start", "id": "t1", "name": "execute", "agent": map[string]any{"id": "bayu", "name": "Bayu", "color": "#FF7EB6"}},
		{"type": "agent", "kind": "tool_delta", "id": "t1", "lines": float64(28), "agent": map[string]any{"id": "bayu", "name": "Bayu", "color": "#FF7EB6"}},
	}, nowMS)

	tt.Frame()
	tt.Advance(80 * time.Millisecond)
	tt.Advance(80 * time.Millisecond)

	// Complete the tool call & stream, verifying smooth markdown reveal handoff and completion badge swap.
	nowMS2 := float64(tt.Now().UnixNano()) / 1e6
	app.Store.ApplyEvents([]map[string]any{
		{"type": "agent", "kind": "tool_end", "id": "t1", "output": "ok", "agent": map[string]any{"id": "bayu", "name": "Bayu", "color": "#FF7EB6"}},
		{"type": "agent", "kind": "end", "durationMs": float64(3200), "agent": map[string]any{"id": "bayu", "name": "Bayu", "color": "#FF7EB6"}},
	}, nowMS2)
	tt.Frame()
	tt.Advance(200 * time.Millisecond)

	// Open & close an origin-aware popover (Effort menu) to exercise AnimatedPopover enter/exit without key errors.
	if err := tt.Click("Reasoning effort: High"); err != nil {
		t.Fatalf("click effort button: %v", err)
	}
	tt.Advance(90 * time.Millisecond)
	app.AgentUI.EffortOpen = false
	tt.Frame()
	tt.Advance(130 * time.Millisecond)

	// Exercise interactive rectangle drawing ghost + creation settle pulse on mouse release!
	app.SetTool("rectangle", true)
	tt.Frame()
	tt.Press(360, 220)
	tt.Move(460, 310)
	tt.Advance(17 * time.Millisecond)
	tt.Release(460, 310)
	if app.Canvas.createdPulseID == "" {
		t.Fatalf("expected createdPulseID to be armed on interactive shape creation")
	}
	tt.Advance(100 * time.Millisecond)
	tt.Advance(120 * time.Millisecond)
	if app.Canvas.createdPulseID != "" {
		t.Fatalf("expected createdPulseID to clear cleanly after 180ms settle pulse")
	}

	assertAllInteractiveControlsNamed(t, tt, "Emil animations editor state")
}
