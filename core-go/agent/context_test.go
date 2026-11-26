package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func TestContextWindowEnforcementPreservesToolPairingAndStripsStaleImages(t *testing.T) {
	bigImg := strings.Repeat("A", 8000)
	bigText := strings.Repeat("design card layout specification ", 400)

	// Turn 1 (old human turn with old editor_state + screenshot tool_result)
	u1 := engine.Object("role", "user", "content", jsonv.NewArray(
		engine.Object("type", "text", "text", "Build screen 1"+editorStateNote+bigText),
	))
	a1 := engine.Object("role", "assistant", "content", jsonv.NewArray(
		engine.Object("type", "tool_use", "id", "call_1", "name", "screenshot", "input", engine.Object()),
	))
	tr1 := engine.Object("role", "user", "content", jsonv.NewArray(
		engine.Object("type", "tool_result", "tool_use_id", "call_1", "content", jsonv.NewArray(
			engine.Object("type", "text", "text", "ok"),
			engine.Object("type", "image", "source", engine.Object("type", "base64", "data", bigImg)),
		)),
	))
	a1End := engine.Object("role", "assistant", "content", jsonv.NewArray(
		engine.Object("type", "text", "text", "Screen 1 finished."),
	))

	// Turn 2 (latest human turn with 3 sequential tool_use / tool_result steps)
	u2 := engine.Object("role", "user", "content", jsonv.NewArray(
		engine.Object("type", "text", "text", "Now build screen 2"+editorStateNote+"current_state_marker"),
	))
	var msgs []jsonv.Value
	msgs = append(msgs, u1, a1, tr1, a1End, u2)
	for step := 1; step <= 3; step++ {
		cid := fmt.Sprintf("t2_call_%d", step)
		msgs = append(msgs,
			engine.Object("role", "assistant", "content", jsonv.NewArray(
				engine.Object("type", "tool_use", "id", cid, "name", "execute", "input", engine.Object("input", bigText)),
			)),
			engine.Object("role", "user", "content", jsonv.NewArray(
				engine.Object("type", "tool_result", "tool_use_id", cid, "content", jsonv.NewArray(
					engine.Object("type", "text", "text", "executed "+cid),
				)),
			)),
		)
	}

	// Tight 12k token limit forces Pass 1 + Pass 2 + Pass 3 compaction!
	trimmed, compacted, dropped := EnforceContextWindow(msgs, 2000, 12000)
	if !compacted || dropped == 0 {
		t.Fatalf("expected EnforceContextWindow to compact messages, got compacted=%v dropped=%d", compacted, dropped)
	}
	if len(trimmed) == 0 || !jsonv.StrIs(jsonv.Get(trimmed[0], "role"), "user") || isToolResultUserMsg(trimmed[0]) {
		t.Fatalf("first message after compaction must be a non-tool_result user message, got %v", trimmed[0])
	}

	// Verify strict user/assistant role alternation and 1:1 tool_use -> tool_result matching!
	for i := range trimmed {
		wantRole := "user"
		if i%2 == 1 {
			wantRole = "assistant"
		}
		gotRole := jsonv.StrOr(jsonv.Get(trimmed[i], "role"))
		if gotRole != wantRole {
			t.Fatalf("message[%d] role=%q, want strict alternation %q", i, gotRole, wantRole)
		}
		if gotRole == "assistant" && i+1 < len(trimmed) {
			var wantIDs []string
			for _, b := range jsonv.Items(jsonv.Get(trimmed[i], "content")) {
				if jsonv.StrIs(jsonv.Get(b, "type"), "tool_use") {
					wantIDs = append(wantIDs, jsonv.StrOr(jsonv.Get(b, "id")))
				}
			}
			if len(wantIDs) > 0 {
				var gotIDs []string
				for _, b := range jsonv.Items(jsonv.Get(trimmed[i+1], "content")) {
					if jsonv.StrIs(jsonv.Get(b, "type"), "tool_result") {
						gotIDs = append(gotIDs, jsonv.StrOr(jsonv.Get(b, "tool_use_id")))
					}
				}
				if strings.Join(wantIDs, ",") != strings.Join(gotIDs, ",") {
					t.Fatalf("mismatched tool_use_ids (%v) vs tool_result (%v)", wantIDs, gotIDs)
				}
			}
		}
	}

	// Verify ClampMaxOutputTokens keeps estInput + maxOut inside custom 32k context window!
	maxOut := ClampMaxOutputTokens(32768, 26000)
	if 26000+maxOut > 32768 {
		t.Fatalf("ClampMaxOutputTokens(32768, 26000) = %d exceeded 32768 window!", maxOut)
	}
}

func TestCustomProviderContextWindowAndLiveContextEvents(t *testing.T) {
	var seenMaxTokens atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if parsed, err := jsonv.Parse(raw); err == nil {
			if mt, ok := jsonv.F64(jsonv.Get(parsed, "max_tokens")); ok {
				seenMaxTokens.Store(int64(mt))
			} else if mt2, ok := jsonv.F64(jsonv.Get(parsed, "max_completion_tokens")); ok {
				seenMaxTokens.Store(int64(mt2))
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Done.\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":14200,\"completion_tokens\":380}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	e := engine.New(t.TempDir())
	Attach(e)

	var provID string
	e.Locked(func() {
		var err error
		provID, err = e.SaveProvider(jsonv.MustParse(fmt.Sprintf(
			`{"name":"Custom Qwen","format":"chat","base_url":%q,"api_key":"sk-test","models":["qwen4-max"],"vision":true,"context_window":64000}`,
			srv.URL,
		)))
		if err != nil {
			t.Fatalf("SaveProvider: %v", err)
		}
		_ = e.SetSetting("agent_backend", "provider")
		_ = e.SetSetting("agent_provider", provID)
		_ = e.SetSetting("provider_model", "qwen4-max")
		if got := e.EffectiveContextWindow(); got != 64000 {
			t.Fatalf("EffectiveContextWindow() = %d, want 64000", got)
		}
		// Re-saving provider without context_window field must preserve existing 64000!
		_, err = e.SaveProvider(jsonv.MustParse(fmt.Sprintf(
			`{"id":%q,"name":"Custom Qwen","format":"chat","base_url":%q,"models":["qwen4-max","qwen4-plus"],"vision":true}`,
			provID, srv.URL,
		)))
		if err != nil {
			t.Fatalf("SaveProvider update: %v", err)
		}
		if got := e.EffectiveContextWindow(); got != 64000 {
			t.Fatalf("EffectiveContextWindow() after model append = %d, want preserved 64000", got)
		}
	})

	if err := Send(e, "Design a clean card", 1, "split", 1, "layout"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	sawContextEv := false
	for time.Now().Before(deadline) {
		var running bool
		var ctxUsed, ctxLim int
		e.Locked(func() {
			running = e.Agent.Running
			ctxUsed = e.Agent.ContextUsed
			ctxLim = e.Agent.ContextLimit
			for _, ev := range e.TakeEvents() {
				if jsonv.StrIs(jsonv.Get(ev, "type"), "agent_context") {
					sawContextEv = true
				}
			}
		})
		if !running && ctxUsed == 14580 && ctxLim == 64000 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	var finalUsed, finalLim int
	e.Locked(func() {
		finalUsed = e.Agent.ContextUsed
		finalLim = e.Agent.ContextLimit
	})
	if !sawContextEv || finalUsed != 14580 || finalLim != 64000 {
		t.Fatalf("expected live context tracking (used=14580, limit=64000, sawEv=true), got used=%d limit=%d sawEv=%v", finalUsed, finalLim, sawContextEv)
	}
	if mt := seenMaxTokens.Load(); mt <= 0 || mt > 16000 {
		t.Fatalf("expected clamped max_tokens <= 16000 (25%% of 64k window), got %d", mt)
	}
}
