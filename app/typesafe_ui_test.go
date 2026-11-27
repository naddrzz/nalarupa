package main

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestUITypeSafeSettingsAndScoreCardAccessibility(t *testing.T) {
	app, tt := newTestApp(t, ui.TesterOptions{})

	if err := tt.Click("New design: A 1440 desktop frame"); err != nil {
		t.Fatalf("open desktop template: %v", err)
	}
	if err := tt.Click("Agent settings"); err != nil {
		t.Fatalf("open Agent settings: %v", err)
	}

	if !tt.HasText("TypeSafe AI visual gate (optional)") {
		t.Fatalf("missing TypeSafe AI section in Agent settings; texts=%v", tt.Texts())
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent settings with TypeSafe off")

	// Scroll the Agent settings drawer down so the TypeSafe section is inside the 400px scroll viewport.
	app.AgentUI.SettingsScroll.Y = math.MaxFloat32
	tt.Frame()

	// Toggle TypeSafe visual gate ON and save BYOK key via setSetting, then audit a11y with Test/Remove/Refresh visible!
	if err := tt.Click("Score each finished screen image (0–100)"); err != nil {
		t.Fatalf("click TypeSafe toggle: %v", err)
	}
	if !app.AgentUI.TypeSafeEnabled {
		t.Fatalf("expected TypeSafeEnabled=true after clicking toggle")
	}

	app.setSetting("typesafe_api_key", "ts-test-byok-123")
	app.AgentUI.SettingsScroll.Y = math.MaxFloat32
	tt.Frame()
	if !app.AgentUI.HasSavedTypeSafeKey || !app.AgentUI.HasTypeSafeKey {
		t.Fatalf("expected HasSavedTypeSafeKey=true after saving key")
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent settings with TypeSafe BYOK key saved")

	// Select jev-1.14.0 model chip and 85/100 threshold chip
	if err := tt.Click("TypeSafe model: jev-1.14.0"); err != nil {
		t.Fatalf("click jev-1.14.0 model chip: %v", err)
	}
	if app.AgentUI.TypeSafeModel != "jev-1.14.0" {
		t.Fatalf("expected TypeSafeModel=jev-1.14.0, got %q", app.AgentUI.TypeSafeModel)
	}
	if err := tt.Click("TypeSafe minimum passing score: 85 out of 100"); err != nil {
		t.Fatalf("click 85 threshold chip: %v", err)
	}
	if app.AgentUI.TypeSafeMinScore != 85 {
		t.Fatalf("expected TypeSafeMinScore=85, got %d", app.AgentUI.TypeSafeMinScore)
	}

	// Verify custom score threshold (1–100), auto-revise toggle, and max auto-revisions selector
	if !app.AgentUI.TypeSafeAutoRevise {
		t.Fatalf("expected TypeSafeAutoRevise=true by default")
	}
	if err := tt.Click("Maximum auto-revisions per screen: 3"); err != nil {
		t.Fatalf("click max auto-revisions 3× chip: %v", err)
	}
	if app.AgentUI.TypeSafeMaxRevisions != 3 {
		t.Fatalf("expected TypeSafeMaxRevisions=3, got %d", app.AgentUI.TypeSafeMaxRevisions)
	}
	app.setSetting("typesafe_min_score", 88)
	app.AgentUI.SettingsScroll.Y = math.MaxFloat32
	tt.Frame()
	if app.AgentUI.TypeSafeMinScore != 88 {
		t.Fatalf("expected custom TypeSafeMinScore=88, got %d", app.AgentUI.TypeSafeMinScore)
	}

	// Toggle auto-revision OFF and back ON while auditing accessibility in both states
	if err := tt.Click("Auto-revise screens below score threshold"); err != nil {
		t.Fatalf("click auto-revise toggle off: %v", err)
	}
	if app.AgentUI.TypeSafeAutoRevise {
		t.Fatalf("expected TypeSafeAutoRevise=false after toggling off")
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent settings with TypeSafe auto-revise off")

	app.AgentUI.SettingsScroll.Y = math.MaxFloat32
	tt.Frame()
	if err := tt.Click("Auto-revise screens below score threshold"); err != nil {
		t.Fatalf("click auto-revise toggle back on: %v", err)
	}
	if !app.AgentUI.TypeSafeAutoRevise {
		t.Fatalf("expected TypeSafeAutoRevise=true after toggling back on")
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent settings with TypeSafe auto-revise on")

	// Close settings and simulate both an auto-revision card and a passing card in the transcript
	_ = tt.Click("Agent settings")
	app.Store.ApplyEvents([]map[string]any{
		{
			"type":       "agent",
			"kind":       "tool_start",
			"id":         "ts_eval_0",
			"name":       "typesafe_decision",
			"screenName": "Checkout Screen",
			"agent":      map[string]any{"id": "ag1", "name": "Arunika", "color": "#7C9CFF"},
		},
		{
			"type":   "agent",
			"kind":   "tool_done",
			"id":     "ts_eval_0",
			"name":   "typesafe_decision",
			"output": "Screen \"Checkout Screen\" (frm1) — TypeSafe Visual Score: 72/100 [AUTO-REVISE 1/3]",
			"error":  false,
			"agent":  map[string]any{"id": "ag1", "name": "Arunika", "color": "#7C9CFF"},
			"typesafe": map[string]any{
				"screenId":        "frm1",
				"screenName":      "Checkout Screen",
				"score":           72,
				"passScore":       88,
				"verdict":         "revise",
				"autoRevise":      true,
				"revisionAttempt": 1,
				"maxRevisions":    3,
				"model":           "jev-1.14.0",
				"dimensions": []any{
					map[string]any{"key": "visual_hierarchy", "label": "Hierarchy", "score": 70, "status": "warn"},
					map[string]any{"key": "accessibility_contrast", "label": "Accessibility", "score": 68, "status": "warn"},
				},
			},
		},
		{
			"type":       "agent",
			"kind":       "tool_start",
			"id":         "ts_eval_1",
			"name":       "typesafe_decision",
			"screenName": "Checkout Screen",
			"agent":      map[string]any{"id": "ag1", "name": "Arunika", "color": "#7C9CFF"},
		},
		{
			"type":   "agent",
			"kind":   "tool_done",
			"id":     "ts_eval_1",
			"name":   "typesafe_decision",
			"output": "Screen \"Checkout Screen\" (frm1) — TypeSafe Visual Score: 91/100 [PASS]",
			"error":  false,
			"agent":  map[string]any{"id": "ag1", "name": "Arunika", "color": "#7C9CFF"},
			"typesafe": map[string]any{
				"screenId":   "frm1",
				"screenName": "Checkout Screen",
				"score":      91,
				"passScore":  88,
				"verdict":    "pass",
				"autoRevise": false,
				"model":      "jev-1.14.0",
				"dimensions": []any{
					map[string]any{"key": "visual_hierarchy", "label": "Hierarchy", "score": 92, "status": "good"},
					map[string]any{"key": "accessibility_contrast", "label": "Accessibility", "score": 90, "status": "good"},
					map[string]any{"key": "has_overlap_or_clipping", "label": "No overlap", "score": 96, "status": "good"},
					map[string]any{"key": "is_ai_slop", "label": "Anti-AI slop", "score": 88, "status": "good"},
				},
			},
		},
	}, 1000)
	tt.Frame()

	if !tt.HasText("TypeSafe · Checkout Screen") || !tt.HasText("72/100 · AUTO-REVISE 1/3 (<88)") || !tt.HasText("91/100 · PASS") {
		t.Fatalf("expected TypeSafe auto-revise & pass score cards in transcript, texts=%v", tt.Texts())
	}
	assertAllInteractiveControlsNamed(t, tt, "Agent transcript with TypeSafe score card")
}
