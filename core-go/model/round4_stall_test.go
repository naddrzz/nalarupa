package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func TestThirdPartyChatUsesMaxTokensAndHandlesQwen4ReasoningFallback(t *testing.T) {
	if !ReasoningModel("qwen4-max") {
		t.Fatalf("expected ReasoningModel(\"qwen4-max\") = true")
	}

	var reqCount atomic.Int32
	var firstHasMaxCompletion atomic.Bool
	var firstHasMaxTokens atomic.Bool
	var efforts [3]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		parsed, _ := jsonv.Parse(raw)
		n := reqCount.Add(1)
		obj := jsonv.Obj(parsed)
		if n >= 1 && n <= 3 && obj != nil {
			efforts[n-1] = jsonv.StrOr(jsonv.Get(obj, "reasoning_effort"))
		}
		if n == 1 {
			firstHasMaxCompletion.Store(obj != nil && obj.Has("max_completion_tokens"))
			firstHasMaxTokens.Store(obj != nil && obj.Has("max_tokens"))
		}
		if obj != nil && obj.Has("reasoning_effort") {
			// Simulate third-party gateway rejecting reasoning_effort with a generic 400 that does NOT echo field name.
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"InvalidParameter: extra_forbidden"}}`)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, chunks(`{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewChat(srv.URL+"/compatible-mode/v1", "sk-qwen", true)
	c.canReason = true // simulate remote third-party OpenAI-compatible gateway (!c.openai && c.canReason)

	body := jsonv.MustParse(`{
		"model": "qwen4-max",
		"max_tokens": 32000,
		"output_config": {"effort": "max"},
		"messages": [{"role": "user", "content": "Design a dashboard"}]
	}`)
	out, err := c.Stream(body, "", never, ignore)
	if err != nil {
		t.Fatalf("expected generic 400 on reasoning_effort to fallback cleanly, got error: %v", err)
	}
	if firstHasMaxCompletion.Load() || !firstHasMaxTokens.Load() {
		t.Fatalf("third-party provider must receive max_tokens (got max_tokens=%v, max_completion_tokens=%v)",
			firstHasMaxTokens.Load(), firstHasMaxCompletion.Load())
	}
	if reqCount.Load() != 3 || efforts != ([3]string{"xhigh", "high", ""}) {
		t.Fatalf("expected 3 requests stepping reasoning_effort xhigh->high->omitted, got count=%d efforts=%v",
			reqCount.Load(), efforts)
	}
	eq(t, at(out, "content", 0, "text"), `"done"`)
}

func TestChatRecoversFromPreOutputStallAndParsesAltUsageKeys(t *testing.T) {
	if stallLimit < 240*time.Second {
		t.Fatalf("expected base stallLimit >= 240s to tolerate deep reasoning prefill, got %v", stallLimit)
	}

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		n := attempts.Add(1)
		if n == 1 {
			// Silent prefill stall before emitting any event
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, chunks(
			delta(`{"content":"Recovered!"}`),
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"input_tokens":412,"output_tokens":95}}`,
			`{"choices":[],"usage":{}}`,
		))
	}))
	defer srv.Close()

	c := NewChat(srv.URL+"/v1", "sk-test", true)
	c.stall = 80 * time.Millisecond
	out, err := c.Stream(jsonv.MustParse(`{"model":"qwen4-max","messages":[{"role":"user","content":"Hi"}]}`), "", never, ignore)
	if err != nil {
		t.Fatalf("expected pre-output stall to retry automatically and recover, got: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
	eq(t, at(out, "content", 0, "text"), `"Recovered!"`)
	eq(t, at(out, "usage"), `{"input_tokens": 412, "output_tokens": 95}`)
}
