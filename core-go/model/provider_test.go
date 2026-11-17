package model

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// The ports of provider.rs's tests, and the fake server the client tests share (provider.rs `tests::fake_server`).
// `saving_a_provider_keeps_its_key_unless_a_new_one_is_given` is in engine/provider_test.go, where SaveProvider is.

// received is one request as a fake server received it.
type received struct {
	path   string
	header http.Header
	body   jsonv.Value
}

// seenLog is what a fake server received, in order.
type seenLog struct {
	mu   sync.Mutex
	reqs []received
}

func (s *seenLog) at(t *testing.T, i int) received {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.reqs) {
		t.Fatalf("request %d was not received (%d were)", i, len(s.reqs))
	}
	return s.reqs[i]
}

// fakeServer answers request n with replies[n] (an SSE body) and keeps every request it received. It returns the
// server's root URL.
func fakeServer(t *testing.T, replies ...string) (string, *seenLog) {
	t.Helper()
	seen := &seenLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body, err := jsonv.Parse(raw)
		if err != nil {
			body = nil
		}
		seen.mu.Lock()
		n := len(seen.reqs)
		seen.reqs = append(seen.reqs, received{r.URL.RequestURI(), r.Header.Clone(), body})
		seen.mu.Unlock()
		if n >= len(replies) {
			http.Error(w, "no reply left", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("connection", "close")
		io.WriteString(w, replies[n])
	}))
	t.Cleanup(srv.Close)
	return srv.URL, seen
}

// sse is an SSE body from `data:` payloads.
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "data: %s\n\n", jsonv.Marshal(jsonv.MustParse(e)))
	}
	return b.String()
}

func never() bool { return false }

func ignore(Event) {}

// record writes each event as `text:…`, `thinking:…`, `start:<id>:<name>` or `input:<id>:<json>`.
func record(events *[]string) func(Event) {
	return func(ev Event) {
		switch ev.Kind {
		case EventText:
			*events = append(*events, "text:"+ev.Text)
		case EventThinking:
			*events = append(*events, "thinking:"+ev.Text)
		case EventToolStart:
			*events = append(*events, "start:"+ev.ID+":"+ev.Name)
		case EventToolInput:
			*events = append(*events, "input:"+ev.ID+":"+ev.JSON)
		}
	}
}

// eq fails unless got equals the JSON want (serde's `==`).
func eq(t *testing.T, got jsonv.Value, want string, what ...any) {
	t.Helper()
	if !jsonv.Equal(got, jsonv.MustParse(want)) {
		t.Fatalf("%v: got %s, want %s", fmt.Sprint(what...), jsonv.Marshal(got), want)
	}
}

// at reads a value by a path of keys and indexes.
func at(v jsonv.Value, path ...any) jsonv.Value {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = jsonv.Get(v, k)
		case int:
			items := jsonv.Items(v)
			if k >= len(items) {
				return nil
			}
			v = items[k]
		}
	}
	return v
}

func providers() jsonv.Value {
	return jsonv.MustParse(`[
		{"id": "or", "name": "OpenRouter", "format": "chat", "base_url": "https://openrouter.ai/api/v1", "api_key": "sk-or", "models": ["qwen/qwen3-coder", "x/y"], "vision": false},
		{"id": "gw", "name": "Gateway", "format": "anthropic", "base_url": "https://gw.example.com", "api_key": "sk-gw", "models": ["claude-opus-5-5"], "vision": true},
		{"id": "rs", "name": "Responses", "format": "responses", "base_url": "https://api.openai.com/v1", "api_key": "sk", "models": ["gpt-x"], "vision": true}
	]`)
}

func TestTheActiveProviderPicksTheClientAndTheModel(t *testing.T) {
	ps := providers()
	for i := range 3 {
		if _, err := FromConfig(at(ps, i)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := FromConfig(jsonv.MustParse(`{"format": "chat"}`)); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatal("a provider needs a base URL", err)
	}
	if _, err := FromConfig(jsonv.MustParse(`{"format": "gemini", "base_url": "x"}`)); err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Fatal(err)
	}
	// The chosen model when the provider offers it, else its first model.
	for _, c := range []struct{ chosen, want string }{{"x/y", "x/y"}, {"claude-opus-5-5", "qwen/qwen3-coder"}, {"", "qwen/qwen3-coder"}} {
		if got, ok := PickModel(at(ps, 0), c.chosen); !ok || got != c.want {
			t.Fatalf("PickModel(%q) = %q, want %q", c.chosen, got, c.want)
		}
	}
	if n := at(Active(ps, "gw"), "name"); !jsonv.StrIs(n, "Gateway") {
		t.Fatal(n)
	}
	if id := at(Active(ps, "gone"), "id"); !jsonv.StrIs(id, "or") {
		t.Fatal("a removed choice falls back to the first provider", id)
	}
	if Active(jsonv.MustParse(`[]`), "") != nil {
		t.Fatal("an empty list has no active provider")
	}
}

func TestABaseURLRoutesTheClaudeAPIAndKeepsItsHeaders(t *testing.T) {
	reply := sse(`{"type": "message_stop"}`)
	for _, base := range []string{"/gateway", "/gateway/", "/gateway/v1", "/gateway?team=a#notes"} {
		root, seen := fakeServer(t, reply)
		var client Provider = ClaudeAt("sk-test", root+base)
		if _, err := client.Stream(jsonv.MustParse(`{"model": "m", "messages": []}`), "b1", never, ignore); err != nil {
			t.Fatal(base, err)
		}
		r := seen.at(t, 0)
		want := "/gateway/v1/messages"
		if strings.Contains(base, "?") {
			want = "/gateway/v1/messages?team=a"
		}
		if r.path != want {
			t.Fatalf("%s: path %q, want %q", base, r.path, want)
		}
		for k, v := range map[string]string{"x-api-key": "sk-test", "anthropic-version": "2023-06-01", "anthropic-beta": "b1"} {
			if got := r.header.Get(k); got != v {
				t.Fatalf("%s: %s = %q, want %q", base, k, got, v)
			}
		}
		eq(t, at(r.body, "stream"), `true`, base)
	}
}

func TestAChatSwitchedToTheClaudeAPIDropsWhatOnlyOtherFormatsUnderstand(t *testing.T) {
	root, seen := fakeServer(t, sse(`{"type": "message_stop"}`))
	var client Provider = ClaudeAt("k", root)
	messages := `[
		{"role": "user", "content": "Design a page"},
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "Plan."}, {"type": "reasoning_item", "item": {"id": "rs_1"}}]},
		{"role": "user", "content": "Go on"},
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "Hm.", "signature": "sig"}, {"type": "text", "text": "Done."}]},
		{"role": "user", "content": "Thanks"}
	]`
	// From a Responses provider: an unsigned thinking summary and an opaque reasoning item, nothing else. From
	// Claude: signed thinking stays.
	body := jsonv.MustParse(`{"model": "m", "messages": ` + messages + `}`)
	if _, err := client.Stream(body, "", never, ignore); err != nil {
		t.Fatal(err)
	}
	eq(t, at(seen.at(t, 0).body, "messages"), `[
		{"role": "user", "content": "Design a page"},
		{"role": "user", "content": "Go on"},
		{"role": "assistant", "content": [{"type": "thinking", "thinking": "Hm.", "signature": "sig"}, {"type": "text", "text": "Done."}]},
		{"role": "user", "content": "Thanks"}
	]`)
	eq(t, at(body, "messages"), messages, "the caller's body is not changed")
}

func TestAWholeTurnPassesThroughTheProviderInterface(t *testing.T) {
	turn1 := sse(
		`{"type": "message_start", "message": {"usage": {"input_tokens": 12}}}`,
		`{"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Building the hero."}}`,
		`{"type": "content_block_stop", "index": 0}`,
		`{"type": "content_block_start", "index": 1, "content_block": {"type": "tool_use", "id": "toolu_1", "name": "execute", "input": {}}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "input_json_delta", "partial_json": "{\"input\":\"Insert("}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "input_json_delta", "partial_json": "document)\"}"}}`,
		`{"type": "content_block_stop", "index": 1}`,
		`{"type": "message_delta", "delta": {"stop_reason": "tool_use"}, "usage": {"output_tokens": 30}}`,
		`{"type": "message_stop"}`,
	)
	turn2 := sse(
		`{"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": "Done."}}`,
		`{"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 2}}`,
	)
	root, seen := fakeServer(t, turn1, turn2)
	var p Provider = ClaudeAt("k", root)
	var events []string
	on := func(ev Event) {
		switch ev.Kind {
		case EventText:
			events = append(events, "text:"+ev.Text)
		case EventThinking:
			events = append(events, "thinking:"+ev.Text)
		case EventToolStart:
			events = append(events, "start:"+ev.Name)
		case EventToolInput:
			events = append(events, "input:"+ev.JSON)
		}
	}
	first, err := p.Stream(jsonv.MustParse(`{"model": "m", "messages": [{"role": "user", "content": "Design a page"}]}`), "", never, on)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, at(first, "stop_reason"), `"tool_use"`)
	eq(t, at(first, "content", 1, "input"), `{"input": "Insert(document)"}`)
	eq(t, at(first, "usage"), `{"input_tokens": 12, "output_tokens": 30}`)
	want := []string{"text:Building the hero.", "start:execute", `input:{"input":"Insert(`, `input:{"input":"Insert(document)"}`}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events %q, want %q", events, want)
	}
	messages := jsonv.MustParse(`[
		{"role": "user", "content": "Design a page"},
		{"role": "assistant", "content": null},
		{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "OK"}, {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}}], "is_error": false}]}
	]`)
	jsonv.Obj(at(messages, 1)).Set("content", at(first, "content"))
	body := jsonv.NewObject()
	body.Set("model", "m")
	body.Set("messages", messages)
	second, err := p.Stream(body, "", never, ignore)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, at(second, "stop_reason"), `"end_turn"`)
	if sent := at(seen.at(t, 1).body, "messages"); !jsonv.Equal(sent, messages) {
		t.Fatalf("the tool result and its screenshot reach the model as built: %s", jsonv.Marshal(sent))
	}
}

func TestEndpointKeepsTheQueryAndDropsTheFragment(t *testing.T) {
	for _, c := range []struct{ base, want string }{
		{" https://x/v1/ ", "https://x/v1/chat/completions"},
		{"https://x/v1?tenant=a#f", "https://x/v1/chat/completions?tenant=a"},
		{"https://x/v1/?", "https://x/v1/chat/completions"},
	} {
		if got := Endpoint(c.base, "/chat/completions"); got != c.want {
			t.Errorf("Endpoint(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestProviderTestReportsTheReplyOrTheError(t *testing.T) {
	ok := sse(`{"choices": [{"index": 0, "delta": {"content": " OK "}, "finish_reason": "stop"}]}`)
	empty := sse(`{"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}`)
	base, seen := fakeServer(t, ok, empty)
	c := NewChat(base, "", true)
	eq(t, Test(c, "p1", "m1"), `{"type": "provider_test", "id": "p1", "ok": true, "model": "m1", "reply": "OK"}`)
	eq(t, at(seen.at(t, 0).body, "max_tokens"), `16`)
	eq(t, at(seen.at(t, 0).body, "messages"), `[{"role": "user", "content": [{"type": "text", "text": "Reply with the single word OK."}]}]`)
	eq(t, Test(c, "p1", "m1"), `{"type": "provider_test", "id": "p1", "ok": false, "model": "m1", "error": "The provider answered with no text. Check the model id."}`)
	got := Test(c, "p1", "m1")
	if !jsonv.IsFalse(at(got, "ok")) || !strings.Contains(jsonv.StrOr(at(got, "error")), "Provider error 500") {
		t.Fatal(string(jsonv.Marshal(got)))
	}
}
