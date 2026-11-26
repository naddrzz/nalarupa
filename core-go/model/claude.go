package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// claudeURL is the Anthropic Messages endpoint.
const claudeURL = "https://api.anthropic.com/v1/messages"

// stallLimit: a stream (or the wait for its headers) silent this long fails as stalled.
const (
	stallLimit           = 240 * time.Second
	highEffortStallLimit = 420 * time.Second
)

// EventKind is what an Event reports.
type EventKind uint8

const (
	// EventText is a piece of reply text (Text).
	EventText EventKind = iota
	// EventThinking is a piece of thinking (Text).
	EventThinking
	// EventToolStart: a tool call started streaming (ID, Name).
	EventToolStart
	// EventToolInput is the accumulated (partial, possibly invalid) JSON of a tool call's input so far (ID, Name,
	// JSON).
	EventToolInput
)

// Event is claude.rs `Event`: what a stream reports while it runs.
type Event struct {
	Kind     EventKind
	Text     string
	ID, Name string
	JSON     string
}

// Claude is the streaming Claude Messages API client (claude.rs `Client`). It rebuilds the assistant content
// blocks exactly as sent (thinking signatures included), so they can be appended to the history unchanged, and
// reports partial tool input while the model is still writing it.
type Claude struct {
	key   string
	url   string
	stall time.Duration
}

// NewClaude is a client for the Anthropic API.
func NewClaude(key string) *Claude {
	return &Claude{key: key, url: claudeURL, stall: stallLimit}
}

// ClaudeAt is a client for the Claude API at base (a gateway's root, with or without `/v1`, keeping any query),
// else (a blank base) the Anthropic API.
func ClaudeAt(key, base string) *Claude {
	b := strings.TrimSpace(base)
	if b == "" {
		return NewClaude(key)
	}
	b, _, _ = strings.Cut(b, "#")
	root, query, _ := strings.Cut(b, "?")
	u := trimAllSuffix(strings.TrimRight(root, "/"), "/v1") + "/v1/messages"
	if query != "" {
		u += "?" + query
	}
	c := NewClaude(key)
	c.url = u
	return c
}

// trimAllSuffix is Rust's `trim_end_matches` with a string pattern: every trailing repeat of suffix goes.
func trimAllSuffix(s, suffix string) string {
	for strings.HasSuffix(s, suffix) {
		s = s[:len(s)-len(suffix)]
	}
	return s
}

// Stream POSTs /v1/messages with `stream: true` and returns `{content, stop_reason, usage}`. beta is the
// `anthropic-beta` header ("" sends none).
func (c *Claude) Stream(body jsonv.Value, beta string, cancel func() bool, on func(Event)) (jsonv.Value, error) {
	// Rust takes the body by value: edits here never reach the caller's copy.
	body = setKey(jsonv.Clone(body), "stream", true)
	// A chat that ran on another provider before: its reasoning items and unsigned thinking mean nothing here
	// (Claude only takes back thinking it signed), and a message left empty is dropped (the API rejects empty
	// content and merges the user turns around it).
	if messages := jsonv.Arr(jsonv.Get(body, "messages")); messages != nil {
		for _, m := range messages.Items {
			if blocks := jsonv.Arr(jsonv.Get(m, "content")); blocks != nil {
				blocks.Items = slices.DeleteFunc(blocks.Items, func(b jsonv.Value) bool {
					t := jsonv.Get(b, "type")
					return jsonv.StrIs(t, "reasoning_item") || (jsonv.StrIs(t, "thinking") && !has(b, "signature"))
				})
			}
		}
		messages.Items = slices.DeleteFunc(messages.Items, func(m jsonv.Value) bool {
			c := jsonv.Arr(jsonv.Get(m, "content"))
			return c != nil && len(c.Items) == 0
		})
	}
	headers := [][2]string{{"anthropic-version", "2023-06-01"}, {"content-type", "application/json"}}
	// A keyless provider (a local Anthropic-compatible server) sends no key header at all.
	if strings.TrimSpace(c.key) != "" {
		headers = append(headers, [2]string{"x-api-key", c.key})
	}
	if beta != "" {
		headers = append(headers, [2]string{"anthropic-beta", beta})
	}
	stall := effectiveStall(c.stall, body)
	for attempt := 0; attempt < 2; attempt++ {
		emitted := false
		out, err := c.attempt(body, headers, stall, cancel, func(ev Event) {
			emitted = true
			on(ev)
		})
		if err == nil {
			return out, nil
		}
		if attempt == 0 && !emitted && isRetryablePreStreamError(err) {
			if !sleepCancelable(retryBackoff(c.stall, attempt), cancel) {
				return nil, errors.New("stopped")
			}
			continue
		}
		return nil, err
	}
	return nil, errors.New("request attempts exhausted")
}

func (c *Claude) attempt(body jsonv.Value, headers [][2]string, stall time.Duration, cancel func() bool, on func(Event)) (jsonv.Value, error) {
	var blocks []jsonv.Value
	var partial []string
	var stopReason jsonv.Value
	var usage jsonv.Value = jsonv.NewObject()
	err := post(c.url, headers, body, stall, "the Claude API", cancel, claudeError, func(data string) (bool, error) {
		ev, err := jsonv.Parse([]byte(data))
		if err != nil {
			return false, fmt.Errorf("bad stream event: %v", err)
		}
		// Rust indexes with u64 and skips unknown blocks; clamp so a huge index
		// stays out of range instead of wrapping negative.
		idx := int(min(u64Or0(jsonv.Get(ev, "index")), 1<<31))
		switch jsonv.StrOr(jsonv.Get(ev, "type")) {
		case "message_start":
			usage = jsonv.Clone(jsonv.Get(jsonv.Get(ev, "message"), "usage"))
		case "content_block_start":
			if idx >= 1<<31 {
				return false, fmt.Errorf("bad stream event: block index out of range")
			}
			for len(blocks) <= idx {
				blocks = append(blocks, nil)
				partial = append(partial, "")
			}
			block := jsonv.Clone(jsonv.Get(ev, "content_block"))
			if jsonv.StrIs(jsonv.Get(block, "type"), "tool_use") {
				on(Event{Kind: EventToolStart, ID: jsonv.StrOr(jsonv.Get(block, "id")), Name: jsonv.StrOr(jsonv.Get(block, "name"))})
			}
			blocks[idx] = block
		case "content_block_delta":
			if idx >= len(blocks) {
				return true, nil
			}
			block := blocks[idx]
			d := jsonv.Get(ev, "delta")
			switch jsonv.StrOr(jsonv.Get(d, "type")) {
			case "text_delta":
				t := jsonv.StrOr(jsonv.Get(d, "text"))
				blocks[idx] = setKey(block, "text", jsonv.StrOr(jsonv.Get(block, "text"))+t)
				on(Event{Kind: EventText, Text: t})
			case "thinking_delta":
				t := jsonv.StrOr(jsonv.Get(d, "thinking"))
				blocks[idx] = setKey(block, "thinking", jsonv.StrOr(jsonv.Get(block, "thinking"))+t)
				on(Event{Kind: EventThinking, Text: t})
			case "signature_delta":
				blocks[idx] = setKey(block, "signature", jsonv.Clone(jsonv.Get(d, "signature")))
			case "input_json_delta":
				partial[idx] += jsonv.StrOr(jsonv.Get(d, "partial_json"))
				on(Event{Kind: EventToolInput, ID: jsonv.StrOr(jsonv.Get(block, "id")), Name: jsonv.StrOr(jsonv.Get(block, "name")), JSON: partial[idx]})
			}
		case "content_block_stop":
			if idx < len(blocks) && jsonv.StrIs(jsonv.Get(blocks[idx], "type"), "tool_use") {
				// Eager input streaming skips server validation: parse strictly here.
				blocks[idx] = setKey(blocks[idx], "input", ToolInput(partial[idx]))
			}
		case "message_delta":
			stopReason = jsonv.Clone(jsonv.Get(jsonv.Get(ev, "delta"), "stop_reason"))
			if out, ok := u64(jsonv.Get(jsonv.Get(ev, "usage"), "output_tokens")); ok {
				usage = setKey(usage, "output_tokens", jsonv.Number(fmt.Sprint(out)))
			}
		case "error":
			msg, ok := jsonv.Str(jsonv.Get(jsonv.Get(ev, "error"), "message"))
			if !ok {
				msg = "unknown"
			}
			return false, fmt.Errorf("Claude API stream error: %s", msg)
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	content := jsonv.NewArray()
	content.Items = []jsonv.Value{}
	for _, b := range blocks {
		if b != nil {
			content.Items = append(content.Items, b)
		}
	}
	return obj("content", content, "stop_reason", stopReason, "usage", usage), nil
}

// ToolInput is a tool call's streamed input: `{}` when empty, else its JSON, or `{"__invalid_json": raw}` for
// the agent to repair.
func ToolInput(raw string) jsonv.Value {
	if strings.TrimSpace(raw) == "" {
		return jsonv.NewObject()
	}
	v, err := jsonv.Parse([]byte(raw))
	if err != nil {
		return obj("__invalid_json", raw)
	}
	return v
}

// claudeError is the failure for an HTTP error status. A rejected key can only be fixed in the app's settings,
// so say so.
func claudeError(status int, msg string) string {
	if status == 401 {
		return "The Claude API rejected this key. Replace it in Agent settings (gear icon) or switch the backend to Claude Code."
	}
	return fmt.Sprintf("Claude API error %d: %s", status, msg)
}

// ── serde_json's Value operations ────────────────────────────────────────────

// obj is a JSON object of key, value pairs in order (serde's `json!({...})` with `preserve_order`).
func obj(kv ...any) *jsonv.Object {
	o := jsonv.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// arr is a JSON array of items, [] when there are none.
func arr(items []jsonv.Value) *jsonv.Array {
	if items == nil {
		items = []jsonv.Value{}
	}
	return jsonv.NewArray(items...)
}

// setKey is serde's `v[k] = x`: null becomes an object first. It returns v, or the new object for a null; any
// other non-object panics, as serde's IndexMut does.
func setKey(v jsonv.Value, k string, x jsonv.Value) jsonv.Value {
	if v == nil {
		v = jsonv.NewObject()
	}
	o, ok := v.(*jsonv.Object)
	if !ok {
		panic(errors.New("cannot access key " + k + " in a JSON non-object"))
	}
	o.Set(k, x)
	return o
}

// has is serde's `v.get(k).is_some()`.
func has(v jsonv.Value, k string) bool {
	o := jsonv.Obj(v)
	return o != nil && o.Has(k)
}

// u64 is serde's `as_u64`.
func u64(v jsonv.Value) (uint64, bool) {
	n, ok := v.(jsonv.Number)
	if !ok {
		return 0, false
	}
	return n.Uint64()
}

func u64Or0(v jsonv.Value) uint64 {
	n, _ := u64(v)
	return n
}
