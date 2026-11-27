package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Chat is a client for the OpenAI Chat Completions wire format (`POST <base>/chat/completions`), which most
// "OpenAI-compatible" servers speak (openai_chat.rs `Client`). Requests come in Claude Messages shape and are
// translated; the stream is translated back into the same events and content blocks the Claude client returns.
type Chat struct {
	url string
	key string
	// vision: the model can see images, so screenshots are sent, or replaced by a note.
	vision bool
	// openai: OpenAI's own API, which renames a few fields for its reasoning models.
	openai bool
	// canReason: non-localhost OpenAI / OpenAI-compatible endpoints that may support reasoning effort.
	canReason bool
	stall     time.Duration
}

// NewChat is a client for base, the server's API root as the provider documents it (e.g.
// `https://api.openai.com/v1`, `http://localhost:11434/v1`); a blank key sends no Authorization header (local
// servers).
func NewChat(base, key string, vision bool) *Chat {
	if strings.TrimSpace(key) == "" {
		key = ""
	}
	isOpenAI := strings.Contains(base, "://api.openai.com")
	return &Chat{url: Endpoint(base, "/chat/completions"), key: key, vision: vision,
		openai: isOpenAI, canReason: supportsReasoningEndpoint(base), stall: stallLimit}
}

// Stream sends one request and returns `{content, stop_reason, usage}`; beta is ignored.
func (c *Chat) Stream(body jsonv.Value, _ string, cancel func() bool, on func(Event)) (jsonv.Value, error) {
	stall := effectiveStall(c.stall, body)
	req := chatTranslateOpts(body, c.vision, c.openai, c.openai || c.canReason)
	// Usage comes only when asked for; a server that rejects the option is asked once more without it. Only a 400
	// before the stream (nothing shown yet), never an error in the middle of one, so no reply is repeated.
	usageOpt := true
	transientRetries := 0
	newIdemKey := func() string {
		return fmt.Sprintf("nr-%x-%x", time.Now().UnixNano(), doc.RandomU64())
	}
	idemKey := newIdemKey()
	for attemptIdx := 0; attemptIdx < 6; attemptIdx++ {
		emitted := false
		out, err := c.attempt(jsonv.Clone(req), usageOpt, idemKey, stall, cancel, func(ev Event) {
			emitted = true
			on(ev)
		})
		if err == nil {
			return out, nil
		}
		if !emitted && usageOpt && strings.HasPrefix(err.Error(), "Provider error 400:") && strings.Contains(err.Error(), "stream_options") {
			usageOpt = false
			idemKey = newIdemKey()
			continue
		}
		if !emitted && shouldRetryReasoning400(err, !c.openai) {
			if obj := jsonv.Obj(req); obj != nil && obj.Has("reasoning_effort") {
				if cur, ok := jsonv.Str(jsonv.Get(obj, "reasoning_effort")); ok && cur == "xhigh" {
					obj.Set("reasoning_effort", "high")
					idemKey = newIdemKey()
					continue
				}
				obj.Remove("reasoning_effort")
				idemKey = newIdemKey()
				continue
			}
		}
		if !emitted && transientRetries < 2 && isRetryablePreStreamError(err) {
			if !sleepCancelable(retryBackoff(c.stall, transientRetries), cancel) {
				return nil, errors.New("stopped")
			}
			transientRetries++
			continue
		}
		return out, err
	}
	return nil, errors.New("request attempts exhausted")
}

func (c *Chat) attempt(body jsonv.Value, usageOption bool, idemKey string, stall time.Duration, cancel func() bool, on func(Event)) (jsonv.Value, error) {
	headers := [][2]string{{"content-type", "application/json"}}
	if idemKey != "" {
		headers = append(headers, [2]string{"idempotency-key", idemKey})
	}
	if c.key != "" {
		headers = append(headers, [2]string{"authorization", "Bearer " + c.key})
	}
	body = setKey(body, "stream", true)
	if usageOption {
		body = setKey(body, "stream_options", obj("include_usage", true))
	}
	// A reply is finished by [DONE] or a finish reason; a socket that just closes means it was cut.
	ended := false
	var text, thinking, finish string
	// Tool calls by their stream index.
	type call struct{ id, name, args string }
	var calls []call
	var usage jsonv.Value = jsonv.NewObject()
	err := post(c.url, headers, body, stall, "the provider", cancel, providerError, func(data string) (bool, error) {
		if strings.TrimSpace(data) == "[DONE]" {
			ended = true
			return false, nil
		}
		ev, err := jsonv.Parse([]byte(data))
		if err != nil {
			return false, fmt.Errorf("bad stream event: %v", err)
		}
		if msg, ok := jsonv.Str(jsonv.Get(jsonv.Get(ev, "error"), "message")); ok {
			return false, fmt.Errorf("Provider stream error: %s", msg)
		}
		if parsedUsage, ok := extractChatUsage(ev); ok {
			if u := jsonv.Obj(parsedUsage); u != nil {
				uObj := jsonv.Obj(usage)
				for _, k := range []string{"input_tokens", "output_tokens"} {
					if v, has := u.Get(k); has && v != nil {
						uObj.Set(k, jsonv.Clone(v))
					} else if !uObj.Has(k) {
						uObj.Set(k, nil)
					}
				}
			}
		}
		choices := jsonv.Items(jsonv.Get(ev, "choices"))
		if len(choices) == 0 {
			return true, nil
		}
		choice := choices[0]
		if f, ok := jsonv.Str(jsonv.Get(choice, "finish_reason")); ok {
			finish = f
			ended = true
		}
		d := jsonv.Get(choice, "delta")
		// Compatible servers stream reasoning under several names; it is shown, never sent back.
		for _, k := range []string{"reasoning_content", "reasoning"} {
			if t := jsonv.StrOr(jsonv.Get(d, k)); t != "" {
				thinking += t
				on(Event{Kind: EventThinking, Text: t})
			}
		}
		if t := jsonv.StrOr(jsonv.Get(d, "content")); t != "" {
			text += t
			on(Event{Kind: EventText, Text: t})
		}
		for _, tc := range jsonv.Items(jsonv.Get(d, "tool_calls")) {
			i, ok := u64(jsonv.Get(tc, "index"))
			if !ok {
				i = uint64(len(calls))
			}
			for uint64(len(calls)) <= i {
				calls = append(calls, call{})
			}
			cl := &calls[i]
			fresh := cl.name == ""
			// A call keeps the first id it was shown under: one arriving late must not rename it mid-stream.
			if id := jsonv.StrOr(jsonv.Get(tc, "id")); id != "" && cl.id == "" {
				cl.id = id
			}
			fn := jsonv.Get(tc, "function")
			if name := jsonv.StrOr(jsonv.Get(fn, "name")); name != "" {
				cl.name = name
			}
			if cl.id == "" {
				cl.id = fmt.Sprintf("call_%d_%d", doc.RandomU64()&0xffff_ffff, i)
			}
			if fresh && cl.name != "" {
				on(Event{Kind: EventToolStart, ID: cl.id, Name: cl.name})
			}
			if a := jsonv.StrOr(jsonv.Get(fn, "arguments")); a != "" {
				cl.args += a
				on(Event{Kind: EventToolInput, ID: cl.id, Name: cl.name, JSON: cl.args})
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ended {
		return nil, errors.New("The provider's stream ended before the reply finished (no finish reason). Try again.")
	}
	var content []jsonv.Value
	if thinking != "" {
		content = append(content, obj("type", "thinking", "thinking", thinking))
	}
	if text != "" {
		content = append(content, obj("type", "text", "text", text))
	}
	named := 0
	for _, cl := range calls {
		if cl.name != "" {
			content = append(content, obj("type", "tool_use", "id", cl.id, "name", cl.name, "input", ToolInput(cl.args)))
			named++
		}
	}
	// Whether tools run is decided by the calls collected, not by the finish reason (servers differ). A reply cut
	// off by the token limit stays max_tokens even with calls in it: the last one may be truncated.
	stop := "end_turn"
	if finish == "length" {
		stop = "max_tokens"
	} else if named > 0 {
		stop = "tool_use"
	}
	return obj("content", arr(content), "stop_reason", stop, "usage", usage), nil
}

// systemText is a Claude request's system prompt as one string: text parts joined by a blank line.
func systemText(v jsonv.Value) string {
	switch x := v.(type) {
	case string:
		return x
	case *jsonv.Array:
		var parts []string
		for _, p := range x.Items {
			if t, ok := jsonv.Str(jsonv.Get(p, "text")); ok {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// blocksOf is a message's content as blocks: a string is one text block.
func blocksOf(v jsonv.Value) []jsonv.Value {
	switch x := v.(type) {
	case string:
		return []jsonv.Value{obj("type", "text", "text", x)}
	case *jsonv.Array:
		return x.Items
	}
	return nil
}

// textsOf is the text of every text block in blocks.
func textsOf(blocks []jsonv.Value) []string {
	var out []string
	for _, b := range blocks {
		if jsonv.StrIs(jsonv.Get(b, "type"), "text") {
			if t, ok := jsonv.Str(jsonv.Get(b, "text")); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// get is serde's `v[k]` as an owned value (a clone: the request built must not share the caller's values).
func get(v jsonv.Value, k string) jsonv.Value { return jsonv.Clone(jsonv.Get(v, k)) }

const (
	shotFollows = "\n[The screenshot of this call follows in the next message.]"
	shotBlind   = "\n[A screenshot was taken, but this model cannot see images: the screenshot is not available.]"
)

func userImageBlind(n int) string {
	if n == 1 {
		return "[User attached 1 image, but this model cannot see images: the image is not available.]"
	}
	return fmt.Sprintf("[User attached %d images, but this model cannot see images: images are not available.]", n)
}

func extractChatUsage(ev jsonv.Value) (jsonv.Value, bool) {
	candidates := []jsonv.Value{jsonv.Get(ev, "usage")}
	if choices := jsonv.Items(jsonv.Get(ev, "choices")); len(choices) > 0 {
		candidates = append(candidates, jsonv.Get(choices[0], "usage"))
	}
	candidates = append(candidates, jsonv.Get(jsonv.Get(ev, "x_groq"), "usage"))
	for _, cand := range candidates {
		u := jsonv.Obj(cand)
		if u == nil {
			continue
		}
		inp := jsonv.Get(u, "prompt_tokens")
		if inp == nil {
			inp = jsonv.Get(u, "input_tokens")
		}
		out := jsonv.Get(u, "completion_tokens")
		if out == nil {
			out = jsonv.Get(u, "output_tokens")
		}
		if inp != nil || out != nil {
			return obj("input_tokens", jsonv.Clone(inp), "output_tokens", jsonv.Clone(out)), true
		}
	}
	return nil, false
}

// ChatTranslate is a Claude Messages request as a Chat Completions request (openai_chat.rs `translate`).
// Claude-only fields (prompt caching, thinking, betas) are dropped; tool results become `tool` messages, and
// their screenshots one user message after all of a turn's tool replies (tool messages are text-only), or a note
// when the model cannot see images.
func ChatTranslate(body jsonv.Value, vision, openai bool) jsonv.Value {
	return chatTranslateOpts(body, vision, openai, openai)
}

func chatTranslateOpts(body jsonv.Value, vision, openai, includeReasoning bool) jsonv.Value {
	var messages []jsonv.Value
	if system := systemText(jsonv.Get(body, "system")); system != "" {
		messages = append(messages, obj("role", "system", "content", system))
	}
	for _, m := range jsonv.Items(jsonv.Get(body, "messages")) {
		blocks := blocksOf(jsonv.Get(m, "content"))
		if jsonv.StrIs(jsonv.Get(m, "role"), "assistant") {
			text := strings.Join(textsOf(blocks), "")
			var calls []jsonv.Value
			for _, b := range blocks {
				if jsonv.StrIs(jsonv.Get(b, "type"), "tool_use") {
					calls = append(calls, obj("id", get(b, "id"), "type", "function",
						"function", obj("name", get(b, "name"), "arguments", string(jsonv.Marshal(jsonv.Get(b, "input"))))))
				}
			}
			var content jsonv.Value
			if text != "" {
				content = text
			}
			msg := obj("role", "assistant", "content", content)
			if len(calls) > 0 {
				msg.Set("tool_calls", arr(calls))
			}
			messages = append(messages, msg)
			continue
		}
		var parts, images []jsonv.Value
		droppedUserImages := 0
		for _, b := range blocks {
			switch jsonv.StrOr(jsonv.Get(b, "type")) {
			case "tool_result":
				inner := blocksOf(jsonv.Get(b, "content"))
				text := strings.Join(textsOf(inner), "\n")
				var shots []jsonv.Value
				for _, p := range inner {
					if jsonv.StrIs(jsonv.Get(p, "type"), "image") {
						shots = append(shots, imagePart(p))
					}
				}
				if len(shots) > 0 {
					if vision {
						text += shotFollows
						images = append(images, shots...)
					} else {
						text += shotBlind
					}
				}
				messages = append(messages, obj("role", "tool", "tool_call_id", get(b, "tool_use_id"), "content", text))
			case "text":
				parts = append(parts, obj("type", "text", "text", get(b, "text")))
			case "image":
				if vision {
					parts = append(parts, imagePart(b))
				} else {
					droppedUserImages++
				}
			}
		}
		if droppedUserImages > 0 {
			parts = append(parts, obj("type", "text", "text", userImageBlind(droppedUserImages)))
		}
		// Nothing may sit between an assistant's tool calls and their replies: images come after all of them.
		parts = append(parts, images...)
		if len(parts) > 0 {
			messages = append(messages, obj("role", "user", "content", arr(parts)))
		}
	}
	out := obj("model", get(body, "model"), "messages", arr(messages))
	if tools := jsonv.Items(jsonv.Get(body, "tools")); len(tools) > 0 {
		var list []jsonv.Value
		for _, t := range tools {
			list = append(list, obj("type", "function", "function",
				obj("name", get(t, "name"), "description", get(t, "description"), "parameters", get(t, "input_schema"))))
		}
		out.Set("tools", arr(list))
	}
	if n, ok := u64(jsonv.Get(body, "max_tokens")); ok {
		k := "max_tokens"
		if openai {
			k = "max_completion_tokens"
		}
		out.Set(k, jsonv.Number(fmt.Sprint(n)))
	}
	if effort, ok := jsonv.Str(jsonv.Get(jsonv.Get(body, "output_config"), "effort")); ok && includeReasoning && ReasoningModel(jsonv.StrOr(jsonv.Get(body, "model"))) {
		if norm := normalizeOpenAIEffort(effort); norm != "" {
			out.Set("reasoning_effort", norm)
		}
	}
	return out
}

func normalizeOpenAIEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "max":
		return "xhigh"
	case "low", "medium", "high", "xhigh", "minimal":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return strings.ToLower(strings.TrimSpace(effort))
	}
}

func supportsReasoningEndpoint(base string) bool {
	if strings.Contains(base, "://api.openai.com") {
		return true
	}
	trimmed := strings.TrimSpace(base)
	if idx := strings.Index(trimmed, "://"); idx >= 0 {
		trimmed = trimmed[idx+3:]
	}
	if idx := strings.IndexAny(trimmed, "/?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	host := strings.ToLower(trimmed)
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end >= 0 {
			host = host[1:end]
		}
	} else if colon := strings.LastIndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return false
	default:
		return true
	}
}

func shouldRetryReasoning400(err error, thirdParty bool) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	is400 := strings.HasPrefix(msg, "Provider error 400:") || strings.HasPrefix(msg, "Provider error 422:")
	if !is400 {
		return false
	}
	low := strings.ToLower(msg)
	if strings.Contains(low, "reasoning") || strings.Contains(low, "effort") {
		return true
	}
	if thirdParty {
		// Compatible servers often reject unknown/extra reasoning_effort fields with generic 400/422 errors.
		return !strings.Contains(low, "model") && !strings.Contains(low, "context") && !strings.Contains(low, "token")
	}
	return false
}

// ReasoningModel reports models across OpenAI and OpenAI-compatible gateways that accept reasoning effort.
func ReasoningModel(model string) bool {
	m := strings.ToLower(model[strings.LastIndexByte(model, '/')+1:])
	if len(m) > 1 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9' {
		return true
	}
	for _, p := range []string{
		"gpt-5", "gpt-6", "gpt-7",
		"deepseek-r1", "deepseek-reasoner", "deepseek-v3",
		"qwq", "qwen3", "qwen4", "qwen-max", "qwen-plus",
		"gemini-2.5", "gemini-3", "gemini-4",
		"grok-3", "grok-4",
		"glm-4", "glm-5", "kimi-k2", "minimax",
	} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	if strings.HasPrefix(m, "claude-") && (strings.Contains(m, "thinking") || strings.Contains(m, "opus") || strings.Contains(m, "sonnet") || strings.Contains(m, "fable")) {
		return true
	}
	return false
}

// dataURL is an image block's source as a data URL.
func dataURL(b jsonv.Value) string {
	src := jsonv.Get(b, "source")
	mt, ok := jsonv.Str(jsonv.Get(src, "media_type"))
	if !ok {
		mt = "image/png"
	}
	return "data:" + mt + ";base64," + jsonv.StrOr(jsonv.Get(src, "data"))
}

func imagePart(b jsonv.Value) jsonv.Value {
	return obj("type", "image_url", "image_url", obj("url", dataURL(b)))
}

// providerError is the failure for an HTTP error status (openai_chat.rs `api_error`). A rejected key can only be
// fixed in the provider's settings.
func providerError(status int, msg string) string {
	if status == 401 || status == 403 {
		return fmt.Sprintf("The provider rejected this key (%s). Check it in Agent settings (gear icon).", msg)
	}
	return fmt.Sprintf("Provider error %d: %s", status, msg)
}
