package model

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// The ports of openai_responses.rs's tests.

// events is an SSE body of Responses events, each named by its type.
func events(evs ...string) string {
	var b strings.Builder
	for _, e := range evs {
		v := jsonv.MustParse(e)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", jsonv.StrOr(jsonv.Get(v, "type")), jsonv.Marshal(v))
	}
	return b.String()
}

func TestAReplyWithReasoningTextAndAFunctionCallStreamsBack(t *testing.T) {
	reasoning := `{"type": "reasoning", "id": "rs_1", "encrypted_content": "gAAA", "summary": [{"type": "summary_text", "text": "Plan."}]}`
	reply := events(
		`{"type": "response.created", "response": {"id": "resp_1"}}`,
		`{"type": "response.output_item.added", "output_index": 0, "item": {"type": "reasoning", "id": "rs_1"}}`,
		`{"type": "response.reasoning_summary_text.delta", "output_index": 0, "delta": "Plan."}`,
		`{"type": "response.output_item.done", "output_index": 0, "item": `+reasoning+`}`,
		`{"type": "response.output_item.added", "output_index": 1, "item": {"type": "message", "id": "msg_1", "role": "assistant", "content": []}}`,
		`{"type": "response.output_text.delta", "output_index": 1, "delta": "Building "}`,
		`{"type": "response.output_text.delta", "output_index": 1, "delta": "the hero."}`,
		`{"type": "response.output_item.added", "output_index": 2, "item": {"type": "function_call", "id": "fc_9", "call_id": "call_9", "name": "execute", "arguments": ""}}`,
		`{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": "{\"input\":"}`,
		`{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": "\"Insert(document)\"}"}`,
		`{"type": "response.output_item.done", "output_index": 2, "item": {"type": "function_call", "id": "fc_9", "call_id": "call_9", "name": "execute", "arguments": "{\"input\":\"Insert(document)\"}"}}`,
		`{"type": "response.completed", "response": {"status": "completed", "usage": {"input_tokens": 50, "output_tokens": 20}}}`,
	)
	base, seen := fakeServer(t, reply)
	c := NewResponses(base+"/v1", "sk-x", true)
	var got []string
	out := mustStream(t, c, `{"model": "gpt-x", "max_tokens": 1000, "messages": [{"role": "user", "content": "Go"}]}`, record(&got))
	if got[0] != "thinking:Plan." {
		t.Fatal(got)
	}
	if !slices.Contains(got, "start:call_9:execute") {
		t.Fatalf("results reference the call_id, not the item id: %q", got)
	}
	eq(t, at(out, "stop_reason"), `"tool_use"`)
	eq(t, at(out, "usage"), `{"input_tokens": 50, "output_tokens": 20}`)
	eq(t, at(out, "content"), `[
		{"type": "thinking", "thinking": "Plan."},
		{"type": "reasoning_item", "item": `+reasoning+`},
		{"type": "text", "text": "Building the hero."},
		{"type": "tool_use", "id": "call_9", "name": "execute", "input": {"input": "Insert(document)"}}
	]`)
	r := seen.at(t, 0)
	if r.path != "/v1/responses" {
		t.Fatal(r.path)
	}
	eq(t, at(r.body, "stream"), `true`)
	eq(t, at(r.body, "store"), `false`)
}

func TestTheHistoryBecomesInputItemsWithImagesInTheToolOutput(t *testing.T) {
	reasoning := `{"type": "reasoning", "id": "rs_1", "encrypted_content": "gAAA", "summary": []}`
	body := jsonv.MustParse(`{
		"model": "gpt-5.1",
		"max_tokens": 32000,
		"system": [{"type": "text", "text": "You are a design agent.", "cache_control": {"type": "ephemeral"}}],
		"tools": [{"name": "execute", "description": "Run", "input_schema": {"type": "object"}, "eager_input_streaming": true}],
		"output_config": {"effort": "medium"},
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Design a page"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "Plan."},
				{"type": "reasoning_item", "item": ` + reasoning + `},
				{"type": "text", "text": "Building."},
				{"type": "tool_use", "id": "call_9", "name": "execute", "input": {"input": "TakeScreenshot()"}}
			]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_9", "content": [{"type": "text", "text": "OK"}, {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBOR"}}], "is_error": false}]}
		]
	}`)
	req := ResponsesTranslate(body, true, true)
	eq(t, at(req, "instructions"), `"You are a design agent."`)
	eq(t, at(req, "max_output_tokens"), `32000`)
	eq(t, at(req, "reasoning"), `{"effort": "medium"}`)
	eq(t, at(req, "include"), `["reasoning.encrypted_content"]`)
	eq(t, at(req, "tools"), `[{"type": "function", "name": "execute", "description": "Run", "parameters": {"type": "object"}}]`)
	eq(t, at(req, "input"), `[
		{"role": "user", "content": [{"type": "input_text", "text": "Design a page"}]},
		`+reasoning+`,
		{"role": "assistant", "content": [{"type": "output_text", "text": "Building."}]},
		{"type": "function_call", "call_id": "call_9", "name": "execute", "arguments": "{\"input\":\"TakeScreenshot()\"}"},
		{"type": "function_call_output", "call_id": "call_9", "output": [
			{"type": "input_text", "text": "OK"},
			{"type": "input_image", "image_url": "data:image/png;base64,iVBOR"}
		]}
	]`)
	blind := ResponsesTranslate(body, false, false)
	if out := jsonv.StrOr(at(blind, "input", 4, "output")); !strings.Contains(out, "screenshot is not available") {
		t.Fatal(string(jsonv.Marshal(at(blind, "input", 4))))
	}
	if jsonv.Obj(blind).Has("reasoning") {
		t.Fatal("effort goes only to OpenAI's own API")
	}
	// The request keeps serde's key order, so the bytes sent match Rust's.
	want := `{"model":"gpt-5.1","instructions":"You are a design agent.","input":[{"role":"user","content":[{"type":"input_text","text":"Design a page"}]},{"type":"reasoning","id":"rs_1","encrypted_content":"gAAA","summary":[]},{"role":"assistant","content":[{"type":"output_text","text":"Building."}]},{"type":"function_call","call_id":"call_9","name":"execute","arguments":"{\"input\":\"TakeScreenshot()\"}"},{"type":"function_call_output","call_id":"call_9","output":[{"type":"input_text","text":"OK"},{"type":"input_image","image_url":"data:image/png;base64,iVBOR"}]}],"store":false,"tools":[{"type":"function","name":"execute","description":"Run","parameters":{"type":"object"}}],"max_output_tokens":32000,"reasoning":{"effort":"medium"},"include":["reasoning.encrypted_content"]}`
	if got := string(jsonv.Marshal(req)); got != want {
		t.Fatalf("bytes\n got %s\nwant %s", got, want)
	}
}

func TestAnIncompleteOrFailedResponseReadsClearly(t *testing.T) {
	empty := `{"model": "m", "messages": []}`
	cut := events(`{"type": "response.incomplete", "response": {"status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"}, "usage": {"input_tokens": 1, "output_tokens": 2}}}`)
	failed := events(`{"type": "response.failed", "response": {"status": "failed", "error": {"code": "server_error", "message": "The server had an error"}}}`)
	base, _ := fakeServer(t, cut, failed)
	c := NewResponses(base, "", true)
	eq(t, at(mustStream(t, c, empty, ignore), "stop_reason"), `"max_tokens"`)
	if _, err := c.Stream(jsonv.MustParse(empty), "", never, ignore); err == nil || !strings.Contains(err.Error(), "The server had an error") {
		t.Fatal(err)
	}
	// Text after a call stays after it, so the replayed history keeps the model's order.
	ordered := events(
		`{"type": "response.output_text.delta", "output_index": 0, "delta": "First."}`,
		`{"type": "response.output_item.added", "output_index": 1, "item": {"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "execute", "arguments": "{}"}}`,
		`{"type": "response.output_text.delta", "output_index": 2, "delta": "Then."}`,
		`{"type": "response.completed", "response": {"status": "completed"}}`,
	)
	base, _ = fakeServer(t, ordered)
	out := mustStream(t, NewResponses(base, "", true), empty, ignore)
	var kinds []string
	for _, b := range jsonv.Items(at(out, "content")) {
		k, ok := jsonv.Str(at(b, "text"))
		if !ok {
			k = jsonv.StrOr(at(b, "name"))
		}
		kinds = append(kinds, k)
	}
	if want := []string{"First.", "execute", "Then."}; !slices.Equal(kinds, want) {
		t.Fatalf("%q", kinds)
	}
	// Cut off by the token limit with a call in it: max_tokens, so the agent does not run a truncated call.
	cutCall := events(
		`{"type": "response.output_item.added", "output_index": 0, "item": {"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "execute", "arguments": ""}}`,
		`{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": "{\"input\": \"Ins"}`,
		`{"type": "response.incomplete", "response": {"status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"}}}`,
	)
	refused := events(
		`{"type": "response.refusal.delta", "output_index": 0, "delta": "I can't help with that."}`,
		`{"type": "response.completed", "response": {"status": "completed"}}`,
	)
	base, _ = fakeServer(t, cutCall, refused)
	c = NewResponses(base, "", true)
	eq(t, at(mustStream(t, c, empty, ignore), "stop_reason"), `"max_tokens"`)
	out = mustStream(t, c, empty, ignore)
	eq(t, at(out, "stop_reason"), `"refusal"`)
	eq(t, at(out, "refusal"), `"I can't help with that."`)
	// An empty text delta opens no text block.
	base, _ = fakeServer(t, events(
		`{"type": "response.output_text.delta", "output_index": 0, "delta": ""}`,
		`{"type": "response.completed", "response": {"status": "completed"}}`,
	))
	eq(t, at(mustStream(t, NewResponses(base, "", true), empty, ignore), "content"), `[]`)
	// Text, then the connection closes without response.completed: a cut-off reply, not a finished one.
	base, _ = fakeServer(t, events(`{"type": "response.output_text.delta", "output_index": 0, "delta": "Half"}`))
	if _, err := NewResponses(base, "", true).Stream(jsonv.MustParse(empty), "", never, ignore); err == nil || !strings.Contains(err.Error(), "ended before") {
		t.Fatal(err)
	}
}
