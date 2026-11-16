package model

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Responses is a client for the OpenAI Responses wire format (`POST <base>/responses`), needed for OpenAI models
// that only call tools through it (openai_responses.rs `Client`). Requests come in Claude Messages shape and are
// translated; the stream is translated back into the same events and content blocks the Claude client returns.
// The full history is sent every turn (`store: false`); reasoning items ride along in the history as
// `{"type": "reasoning_item", "item": …}` blocks and are sent back unchanged.
type Responses struct {
	url string
	key string
	// vision: the model can see images, so screenshots are sent, or replaced by a note.
	vision bool
	// openai: OpenAI's own API, which takes the effort as `reasoning.effort`.
	openai bool
	stall  time.Duration
}

// NewResponses is a client for base, the API root (e.g. `https://api.openai.com/v1`); a blank key sends no
// Authorization header.
func NewResponses(base, key string, vision bool) *Responses {
	if strings.TrimSpace(key) == "" {
		key = ""
	}
	return &Responses{url: Endpoint(base, "/responses"), key: key, vision: vision,
		openai: strings.Contains(base, "://api.openai.com"), stall: stallLimit}
}

// Stream sends one request and returns `{content, stop_reason, usage}` (and `refusal` for a refusal); beta is
// ignored.
func (c *Responses) Stream(body jsonv.Value, _ string, cancel func() bool, on func(Event)) (jsonv.Value, error) {
	headers := [][2]string{{"content-type", "application/json"}}
	if c.key != "" {
		headers = append(headers, [2]string{"authorization", "Bearer " + c.key})
	}
	req := setKey(ResponsesTranslate(body, c.vision, c.openai), "stream", true)
	var thinking, text, refusal string
	// Text by output index, so text written after a call stays after it when the turn is rebuilt.
	type segment struct {
		idx  uint64
		text string
	}
	var texts []segment
	var reasoning []jsonv.Value
	// Function calls by output index.
	type call struct {
		idx            uint64
		id, name, args string
	}
	var calls []call
	stop := "end_turn"
	var usage jsonv.Value = jsonv.NewObject()
	// Finished only by response.completed or .incomplete; a socket that just closes means the reply was cut.
	ended := false
	find := func(idx uint64) *call {
		for i := range calls {
			if calls[i].idx == idx {
				return &calls[i]
			}
		}
		return nil
	}
	err := post(c.url, headers, req, c.stall, "the provider", cancel, providerError, func(data string) (bool, error) {
		ev, err := jsonv.Parse([]byte(data))
		if err != nil {
			return false, fmt.Errorf("bad stream event: %v", err)
		}
		idx := u64Or0(jsonv.Get(ev, "output_index"))
		item := jsonv.Get(ev, "item")
		switch typ := jsonv.StrOr(jsonv.Get(ev, "type")); {
		case typ == "response.reasoning_summary_text.delta" || typ == "response.reasoning_text.delta":
			t := jsonv.StrOr(jsonv.Get(ev, "delta"))
			thinking += t
			on(Event{Kind: EventThinking, Text: t})
		case typ == "response.refusal.delta":
			refusal += jsonv.StrOr(jsonv.Get(ev, "delta"))
		case typ == "response.output_text.delta":
			t := jsonv.StrOr(jsonv.Get(ev, "delta"))
			text += t
			if n := len(texts); n > 0 && texts[n-1].idx == idx {
				texts[n-1].text += t
			} else if t != "" {
				texts = append(texts, segment{idx, t})
			}
			on(Event{Kind: EventText, Text: t})
		case typ == "response.output_item.added" && jsonv.StrIs(jsonv.Get(item, "type"), "function_call"):
			// Results must reference `call_id`; the item's own `id` (`fc_…`) is a different thing.
			id, ok := jsonv.Str(jsonv.Get(item, "call_id"))
			if !ok {
				id = jsonv.StrOr(jsonv.Get(item, "id"))
			}
			name := jsonv.StrOr(jsonv.Get(item, "name"))
			on(Event{Kind: EventToolStart, ID: id, Name: name})
			calls = append(calls, call{idx, id, name, jsonv.StrOr(jsonv.Get(item, "arguments"))})
		case typ == "response.function_call_arguments.delta":
			if cl := find(idx); cl != nil {
				cl.args += jsonv.StrOr(jsonv.Get(ev, "delta"))
				on(Event{Kind: EventToolInput, ID: cl.id, Name: cl.name, JSON: cl.args})
			}
		case typ == "response.output_item.done":
			switch jsonv.StrOr(jsonv.Get(item, "type")) {
			case "function_call":
				if cl := find(idx); cl != nil {
					if a, ok := jsonv.Str(jsonv.Get(item, "arguments")); ok {
						cl.args = a
					}
				}
			case "reasoning":
				reasoning = append(reasoning, jsonv.Clone(item))
			}
		case typ == "response.completed":
			usage = usageOf(jsonv.Get(ev, "response"))
			ended = true
			return false, nil
		case typ == "response.incomplete":
			r := jsonv.Get(ev, "response")
			usage = usageOf(r)
			reason, ok := jsonv.Str(jsonv.Get(jsonv.Get(r, "incomplete_details"), "reason"))
			if !ok {
				reason = "unknown"
			}
			if reason != "max_output_tokens" {
				return false, fmt.Errorf("The provider stopped the reply early (%s).", reason)
			}
			stop = "max_tokens"
			ended = true
			return false, nil
		case typ == "response.failed":
			msg, ok := jsonv.Str(jsonv.Get(jsonv.Get(jsonv.Get(ev, "response"), "error"), "message"))
			if !ok {
				msg = "the response failed"
			}
			return false, fmt.Errorf("Provider error: %s", msg)
		case typ == "error":
			msg, ok := jsonv.Str(jsonv.Get(ev, "message"))
			if !ok {
				if msg, ok = jsonv.Str(jsonv.Get(jsonv.Get(ev, "error"), "message")); !ok {
					msg = "unknown"
				}
			}
			return false, fmt.Errorf("Provider stream error: %s", msg)
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ended {
		return nil, errors.New("The provider's stream ended before the reply finished. Try again.")
	}
	// A refusal is not an empty success: stop_reason `refusal`, as the Claude API reports it, so the agent keeps
	// the prompt in its history and shows the model's reason.
	if text == "" && len(calls) == 0 && refusal != "" {
		return obj("content", arr(nil), "stop_reason", "refusal", "refusal", strings.TrimSpace(refusal), "usage", usage), nil
	}
	var content []jsonv.Value
	if thinking != "" {
		content = append(content, obj("type", "thinking", "thinking", thinking))
	}
	for _, item := range reasoning {
		content = append(content, obj("type", "reasoning_item", "item", item))
	}
	// A reply cut off by the token limit stays max_tokens even with calls in it: the last one may be truncated.
	if len(calls) > 0 && stop == "end_turn" {
		stop = "tool_use"
	}
	// Text segments and calls in the order the model wrote them (their output index).
	type part struct {
		idx   uint64
		block jsonv.Value
	}
	var parts []part
	for _, s := range texts {
		parts = append(parts, part{s.idx, obj("type", "text", "text", s.text)})
	}
	for _, cl := range calls {
		parts = append(parts, part{cl.idx, obj("type", "tool_use", "id", cl.id, "name", cl.name, "input", ToolInput(cl.args))})
	}
	slices.SortStableFunc(parts, func(a, b part) int { return cmp.Compare(a.idx, b.idx) })
	for _, p := range parts {
		content = append(content, p.block)
	}
	return obj("content", arr(content), "stop_reason", stop, "usage", usage), nil
}

func usageOf(r jsonv.Value) jsonv.Value {
	u := jsonv.Get(r, "usage")
	return obj("input_tokens", get(u, "input_tokens"), "output_tokens", get(u, "output_tokens"))
}

// ResponsesTranslate is a Claude Messages request as a Responses request (openai_responses.rs `translate`):
// system becomes `instructions`, the history input items (reasoning items sent back unchanged, tool calls as
// `function_call`, tool results as `function_call_output` with their screenshots as `input_image`, or a note when
// the model cannot see images). Claude-only fields are dropped.
func ResponsesTranslate(body jsonv.Value, vision, openai bool) jsonv.Value {
	var input []jsonv.Value
	reasons := false
	for _, m := range jsonv.Items(jsonv.Get(body, "messages")) {
		blocks := blocksOf(jsonv.Get(m, "content"))
		if jsonv.StrIs(jsonv.Get(m, "role"), "assistant") {
			for _, b := range blocks {
				switch jsonv.StrOr(jsonv.Get(b, "type")) {
				case "reasoning_item":
					input = append(input, get(b, "item"))
					reasons = true
				case "text":
					input = append(input, obj("role", "assistant", "content", arr([]jsonv.Value{obj("type", "output_text", "text", get(b, "text"))})))
				case "tool_use":
					input = append(input, obj("type", "function_call", "call_id", get(b, "id"), "name", get(b, "name"),
						"arguments", string(jsonv.Marshal(jsonv.Get(b, "input")))))
				}
			}
			continue
		}
		var parts []jsonv.Value
		for _, b := range blocks {
			switch jsonv.StrOr(jsonv.Get(b, "type")) {
			case "tool_result":
				inner := blocksOf(jsonv.Get(b, "content"))
				var output jsonv.Value
				if vision {
					var out []jsonv.Value
					for _, p := range inner {
						switch jsonv.StrOr(jsonv.Get(p, "type")) {
						case "text":
							out = append(out, obj("type", "input_text", "text", get(p, "text")))
						case "image":
							out = append(out, obj("type", "input_image", "image_url", dataURL(p)))
						}
					}
					output = arr(out)
				} else {
					text := strings.Join(textsOf(inner), "\n")
					if slices.ContainsFunc(inner, func(p jsonv.Value) bool { return jsonv.StrIs(jsonv.Get(p, "type"), "image") }) {
						text += shotBlind
					}
					output = text
				}
				input = append(input, obj("type", "function_call_output", "call_id", get(b, "tool_use_id"), "output", output))
			case "text":
				parts = append(parts, obj("type", "input_text", "text", get(b, "text")))
			case "image":
				if vision {
					parts = append(parts, obj("type", "input_image", "image_url", dataURL(b)))
				}
			}
		}
		if len(parts) > 0 {
			input = append(input, obj("role", "user", "content", arr(parts)))
		}
	}
	out := obj("model", get(body, "model"))
	if system := systemText(jsonv.Get(body, "system")); system != "" {
		out.Set("instructions", system)
	}
	out.Set("input", arr(input))
	// No server-side state: the full history goes every turn, so local servers that store nothing work too.
	out.Set("store", false)
	if tools := jsonv.Items(jsonv.Get(body, "tools")); len(tools) > 0 {
		var list []jsonv.Value
		for _, t := range tools {
			list = append(list, obj("type", "function", "name", get(t, "name"), "description", get(t, "description"), "parameters", get(t, "input_schema")))
		}
		out.Set("tools", arr(list))
	}
	if n, ok := u64(jsonv.Get(body, "max_tokens")); ok {
		out.Set("max_output_tokens", jsonv.Number(fmt.Sprint(n)))
	}
	if effort, ok := jsonv.Str(jsonv.Get(jsonv.Get(body, "output_config"), "effort")); ok && openai && ReasoningModel(jsonv.StrOr(jsonv.Get(body, "model"))) {
		out.Set("reasoning", obj("effort", effort))
		reasons = true
	}
	if reasons {
		// Reasoning items come back encrypted (store: false), to be sent on the next turn.
		out.Set("include", arr([]jsonv.Value{"reasoning.encrypted_content"}))
	}
	return out
}
