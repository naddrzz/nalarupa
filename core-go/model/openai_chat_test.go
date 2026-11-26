package model

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// The ports of openai_chat.rs's tests.

// chunks is an SSE body from events, ended by `[DONE]`.
func chunks(events ...string) string {
	return sse(events...) + "data: [DONE]\n\n"
}

// delta is a chunk carrying delta d.
func delta(d string) string {
	return `{"choices": [{"index": 0, "delta": ` + d + `, "finish_reason": null}]}`
}

func mustStream(t *testing.T, p Provider, body string, on func(Event)) jsonv.Value {
	t.Helper()
	out, err := p.Stream(jsonv.MustParse(body), "", never, on)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestATextReplyStreamsWithItsUsageAndReasoning(t *testing.T) {
	reply := chunks(
		delta(`{"role": "assistant", "reasoning_content": "Plan first."}`),
		delta(`{"content": "Hello "}`),
		delta(`{"content": "there"}`),
		`{"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}`,
		`{"choices": [], "usage": {"prompt_tokens": 21, "completion_tokens": 4}}`,
	)
	base, seen := fakeServer(t, reply)
	c := NewChat(base+"/v1", "sk-or", true)
	var events []string
	out := mustStream(t, c, `{"model": "m", "max_tokens": 100, "messages": [{"role": "user", "content": "Hi"}]}`, record(&events))
	if want := []string{"thinking:Plan first.", "text:Hello ", "text:there"}; !slices.Equal(events, want) {
		t.Fatalf("events %q", events)
	}
	eq(t, at(out, "content"), `[{"type": "thinking", "thinking": "Plan first."}, {"type": "text", "text": "Hello there"}]`)
	eq(t, at(out, "stop_reason"), `"end_turn"`)
	eq(t, at(out, "usage"), `{"input_tokens": 21, "output_tokens": 4}`)
	r := seen.at(t, 0)
	if r.path != "/v1/chat/completions" {
		t.Fatal(r.path)
	}
	if got := r.header.Get("authorization"); got != "Bearer sk-or" {
		t.Fatal(got)
	}
	eq(t, at(r.body, "stream"), `true`)
	eq(t, at(r.body, "stream_options"), `{"include_usage": true}`)
}

func TestToolCallsArriveInFragmentsWholeOrInParallel(t *testing.T) {
	reply := chunks(
		delta(`{"tool_calls": [{"index": 0, "id": "call_a", "type": "function", "function": {"name": "execute", "arguments": ""}}]}`),
		delta(`{"tool_calls": [{"index": 0, "function": {"arguments": "{\"input\":"}}]}`),
		delta(`{"tool_calls": [{"index": 1, "id": "call_b", "type": "function", "function": {"name": "get_editor_state", "arguments": "{}"}}]}`),
		delta(`{"tool_calls": [{"index": 0, "function": {"arguments": "\"Insert(document)\"}"}}]}`),
		// OpenRouter-style: the usage chunk repeats the finish reason; servers may omit ids.
		`{"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}], "usage": {"prompt_tokens": 9, "completion_tokens": 7}}`,
	)
	base, _ := fakeServer(t, reply)
	var events []string
	out := mustStream(t, NewChat(base, "", true), `{"model": "m", "messages": [{"role": "user", "content": "Go"}]}`, record(&events))
	eq(t, at(out, "stop_reason"), `"tool_use"`)
	eq(t, at(out, "content"), `[
		{"type": "tool_use", "id": "call_a", "name": "execute", "input": {"input": "Insert(document)"}},
		{"type": "tool_use", "id": "call_b", "name": "get_editor_state", "input": {}}
	]`)
	if events[0] != "start:call_a:execute" {
		t.Fatal(events)
	}
	if !slices.Contains(events, `input:call_a:{"input":"Insert(document)"}`) || !slices.Contains(events, "start:call_b:get_editor_state") {
		t.Fatalf("%q", events)
	}
	// A call whose arguments never parse is handed back for repair, like the Claude client does.
	bad := chunks(delta(`{"tool_calls": [{"index": 0, "function": {"name": "execute", "arguments": "{\"input\": \"x"}}]}`), `{"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}]}`)
	base, _ = fakeServer(t, bad)
	out = mustStream(t, NewChat(base, "", true), `{"model": "m", "messages": []}`, ignore)
	eq(t, at(out, "content", 0, "input"), `{"__invalid_json": "{\"input\": \"x"}`)
	eq(t, at(out, "stop_reason"), `"tool_use"`)
	// Cut off by the token limit mid-call: the agent must see max_tokens, so it does not run a truncated call.
	cut := chunks(delta(`{"tool_calls": [{"index": 0, "id": "call_c", "function": {"name": "execute", "arguments": "{\"input\": \"Ins"}}]}`), `{"choices": [{"index": 0, "delta": {}, "finish_reason": "length"}]}`)
	base, _ = fakeServer(t, cut)
	out = mustStream(t, NewChat(base, "", true), `{"model": "m", "messages": []}`, ignore)
	eq(t, at(out, "stop_reason"), `"max_tokens"`)
	// An id that only arrives after the call started does not change the call's identity mid-stream.
	late := chunks(
		delta(`{"tool_calls": [{"index": 0, "function": {"name": "execute", "arguments": "{}"}}]}`),
		delta(`{"tool_calls": [{"index": 0, "id": "call_late", "function": {"arguments": ""}}]}`),
	)
	base, _ = fakeServer(t, late)
	var ids []string
	out = mustStream(t, NewChat(base, "", true), `{"model": "m", "messages": []}`, func(ev Event) {
		if ev.Kind == EventToolStart || ev.Kind == EventToolInput {
			ids = append(ids, ev.ID)
		}
	})
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("%q", ids)
		}
	}
	eq(t, at(out, "content", 0, "id"), fmt.Sprintf("%q", ids[0]))
	if id := jsonv.StrOr(at(out, "content", 0, "id")); id == "" {
		t.Fatal("a missing id is made up")
	}
}

// history is the agent loop's history after one tool turn: text, a call, its result with a screenshot.
func history() jsonv.Value {
	return jsonv.MustParse(`{
		"model": "gpt-x",
		"max_tokens": 32000,
		"system": [{"type": "text", "text": "You are a design agent.", "cache_control": {"type": "ephemeral"}}],
		"tools": [{"name": "execute", "description": "Run a snippet", "input_schema": {"type": "object", "properties": {"input": {"type": "string"}}}, "eager_input_streaming": true}],
		"output_config": {"effort": "high"},
		"thinking": {"type": "adaptive"},
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Design a page"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "hm", "signature": "sig"},
				{"type": "text", "text": "Building."},
				{"type": "tool_use", "id": "call_a", "name": "execute", "input": {"input": "Insert(document)"}},
				{"type": "tool_use", "id": "call_b", "name": "execute", "input": {"input": "TakeScreenshot()"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_a", "content": [{"type": "text", "text": "OK"}], "is_error": false},
				{"type": "tool_result", "tool_use_id": "call_b", "content": [{"type": "text", "text": "OK"}, {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBOR"}}], "is_error": false}
			]}
		]
	}`)
}

func TestRequestsBecomeChatMessagesWithScreenshotsAfterTheToolReplies(t *testing.T) {
	req := ChatTranslate(history(), true, false)
	eq(t, at(req, "model"), `"gpt-x"`)
	eq(t, at(req, "max_tokens"), `32000`)
	if jsonv.Obj(req).Has("thinking") || jsonv.Obj(req).Has("output_config") {
		t.Fatalf("Claude-only fields are dropped: %s", jsonv.Marshal(req))
	}
	eq(t, at(req, "tools"), `[{"type": "function", "function": {"name": "execute", "description": "Run a snippet", "parameters": {"type": "object", "properties": {"input": {"type": "string"}}}}}]`)
	eq(t, at(req, "messages"), `[
		{"role": "system", "content": "You are a design agent."},
		{"role": "user", "content": [{"type": "text", "text": "Design a page"}]},
		{"role": "assistant", "content": "Building.", "tool_calls": [
			{"id": "call_a", "type": "function", "function": {"name": "execute", "arguments": "{\"input\":\"Insert(document)\"}"}},
			{"id": "call_b", "type": "function", "function": {"name": "execute", "arguments": "{\"input\":\"TakeScreenshot()\"}"}}
		]},
		{"role": "tool", "tool_call_id": "call_a", "content": "OK"},
		{"role": "tool", "tool_call_id": "call_b", "content": "OK\n[The screenshot of this call follows in the next message.]"},
		{"role": "user", "content": [{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBOR"}}]}
	]`)
	// A model that cannot see images gets a note instead, and no image message.
	blind := ChatTranslate(history(), false, false)
	msgs := jsonv.Items(at(blind, "messages"))
	if len(msgs) != 5 || !strings.Contains(jsonv.StrOr(at(msgs[4], "content")), "screenshot is not available") {
		t.Fatal(string(jsonv.Marshal(blind)))
	}
	// OpenAI itself wants max_completion_tokens; only its reasoning models take the effort.
	openai := jsonv.Obj(ChatTranslate(history(), true, true))
	eq(t, at(openai, "max_completion_tokens"), `32000`)
	if openai.Has("max_tokens") || openai.Has("reasoning_effort") {
		t.Fatal("gpt-x is not a reasoning model, and OpenAI takes no max_tokens", string(jsonv.Marshal(openai)))
	}
	for _, model := range []string{"o3", "o4-mini", "gpt-5.1", "gpt-6-astra"} {
		h := history()
		jsonv.Obj(h).Set("model", model)
		eq(t, at(ChatTranslate(h, true, true), "reasoning_effort"), `"high"`, model)
	}
	// The request keeps serde's key order, so the bytes sent match Rust's.
	want := `{"model":"gpt-x","messages":[{"role":"system","content":"You are a design agent."},{"role":"user","content":[{"type":"text","text":"Design a page"}]},{"role":"assistant","content":"Building.","tool_calls":[{"id":"call_a","type":"function","function":{"name":"execute","arguments":"{\"input\":\"Insert(document)\"}"}},{"id":"call_b","type":"function","function":{"name":"execute","arguments":"{\"input\":\"TakeScreenshot()\"}"}}]},{"role":"tool","tool_call_id":"call_a","content":"OK"},{"role":"tool","tool_call_id":"call_b","content":"OK\n[The screenshot of this call follows in the next message.]"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}],"tools":[{"type":"function","function":{"name":"execute","description":"Run a snippet","parameters":{"type":"object","properties":{"input":{"type":"string"}}}}}],"max_tokens":32000}`
	if got := string(jsonv.Marshal(req)); got != want {
		t.Fatalf("bytes\n got %s\nwant %s", got, want)
	}
}

func TestAStreamCutBeforeItsEndIsAnErrorAndQueryBasesKeepTheirQuery(t *testing.T) {
	// Text, then the connection closes: no finish reason and no [DONE].
	base, _ := fakeServer(t, sse(delta(`{"content": "Half a sen"}`)))
	_, err := NewChat(base, "", true).Stream(jsonv.MustParse(`{"model": "m", "messages": []}`), "", never, ignore)
	if err == nil || !strings.Contains(err.Error(), "ended before") {
		t.Fatal(err)
	}
	base, seen := fakeServer(t, chunks(`{"choices": [{"index": 0, "delta": {"content": "ok"}, "finish_reason": "stop"}]}`))
	mustStream(t, NewChat(base+"/v1?tenant=a", "", true), `{"model": "m", "messages": []}`, ignore)
	if p := seen.at(t, 0).path; p != "/v1/chat/completions?tenant=a" {
		t.Fatal(p)
	}
}

func TestAServerThatRejectsStreamOptionsIsAskedAgainWithoutThem(t *testing.T) {
	var mu sync.Mutex
	var bodies []jsonv.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body, _ := jsonv.Parse(raw)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, chunks(`{"choices": [{"index": 0, "delta": {"content": "ok"}, "finish_reason": "stop"}]}`))
	}))
	defer srv.Close()
	out := mustStream(t, NewChat(srv.URL, "", true), `{"model": "m", "messages": []}`, ignore)
	eq(t, at(out, "content", 0, "text"), `"ok"`)
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !jsonv.Obj(bodies[0]).Has("stream_options") || jsonv.Obj(bodies[1]).Has("stream_options") {
		t.Fatalf("%d bodies", len(bodies))
	}
}

func TestErrorsReadLikeTheProvidersOwn(t *testing.T) {
	if e := providerError(401, "Incorrect API key"); !strings.Contains(e, "rejected this key") {
		t.Fatal(e)
	}
	if e := providerError(404, "The model `x` does not exist"); e != "Provider error 404: The model `x` does not exist" {
		t.Fatal(e)
	}
}

func TestReasoningModelsEffortNormalizationAnd400Fallback(t *testing.T) {
	for _, m := range []string{"o3", "o4-mini", "gpt-5.4", "gpt-6.2-astra", "deepseek/deepseek-r1", "qwen3-coder", "gemini-3.8-flash", "grok-4", "claude-opus-5-5"} {
		if !ReasoningModel(m) {
			t.Fatalf("expected ReasoningModel(%q) = true", m)
		}
	}
	if ReasoningModel("gpt-4o-mini") {
		t.Fatal("gpt-4o-mini should not be treated as a reasoning model")
	}
	if supportsReasoningEndpoint("http://127.0.0.1:11434/v1") || supportsReasoningEndpoint("http://localhost:8080/v1") {
		t.Fatal("localhost endpoints should default canReason to false")
	}
	if !supportsReasoningEndpoint("https://openrouter.ai/api/v1") {
		t.Fatal("remote OpenAI-compatible gateway should enable reasoning forwarding")
	}
	// max normalizes to xhigh for OpenAI-family endpoints
	req := ChatTranslate(jsonv.MustParse(`{"model":"gpt-6.2-astra","output_config":{"effort":"max"},"messages":[]}`), true, true)
	eq(t, at(req, "reasoning_effort"), `"xhigh"`)

	// When remote gateway rejects xhigh or reasoning_effort with 400, Chat retries downgraded then stripped.
	var mu sync.Mutex
	var seen []jsonv.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		parsed, _ := jsonv.Parse(raw)
		mu.Lock()
		seen = append(seen, parsed)
		n := len(seen)
		mu.Unlock()
		if n <= 2 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Unsupported parameter reasoning_effort"}}`)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, chunks(`{"choices": [{"index": 0, "delta": {"content": "ok"}, "finish_reason": "stop"}]}`))
	}))
	defer srv.Close()
	c := NewChat(srv.URL, "k", true)
	c.canReason = true
	out := mustStream(t, c, `{"model":"gpt-6.2-astra","output_config":{"effort":"max"},"messages":[]}`, ignore)
	eq(t, at(out, "content", 0, "text"), `"ok"`)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("expected 3 attempts (xhigh -> high -> stripped), got %d", len(seen))
	}
	eq(t, at(seen[0], "reasoning_effort"), `"xhigh"`)
	eq(t, at(seen[1], "reasoning_effort"), `"high"`)
	if jsonv.Obj(seen[2]).Has("reasoning_effort") {
		t.Fatal("final fallback should omit reasoning_effort")
	}
}
