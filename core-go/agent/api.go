package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
	"github.com/naddrzz/nalarupa/core-go/script"
)

// apiTools are the tools an API run offers: the canvas tools (a child does not get get_editor_state), with
// `execute` streamed as it is written, plus spawn_agents for a main agent working with parallel designers.
func apiTools(e *engine.Engine, child bool) *jsonv.Array {
	var parallel bool
	e.Locked(func() { parallel = e.Agent.Parallel > 1 })
	tools := jsonv.NewArray()
	tools.Items = []jsonv.Value{}
	for _, t := range jsonv.Items(engine.ToolSpecs()) {
		name := jsonv.Get(t, "name")
		if child && jsonv.StrIs(name, "get_editor_state") {
			continue
		}
		tool := engine.Object("name", name, "description", jsonv.Get(t, "description"), "input_schema", jsonv.Get(t, "inputSchema"))
		if jsonv.StrIs(name, "execute") {
			// Stream the snippet as it is written so the canvas updates live.
			tool.Set("eager_input_streaming", true)
		}
		tools.Items = append(tools.Items, tool)
	}
	if parallel && !child {
		tools.Items = append(tools.Items, SpawnSpec())
	}
	return tools
}

// editorStateNote follows the prompt of a main agent's turn.
const editorStateNote = "\n\nThe result of `get_editor_state` (fresh; call it again only if the document changed):\n"

// runAPI runs p's turn over a model API: the streamed reply, its tool calls and their results, until the model
// ends its turn.
func runAPI(e *engine.Engine, p engine.Profile, prompt string, child bool, name string, client model.Provider, contextLimit int) (*jsonv.Object, error) {
	var effort, state string
	var fast bool
	var cancel *engine.Flag
	var messages []jsonv.Value
	if name == "" {
		name = DefaultModel
	}
	e.Locked(func() {
		var ok bool
		if effort, ok = e.Setting("agent_effort"); !ok {
			effort = "high"
		}
		if contextLimit <= 0 {
			contextLimit = e.ContextWindowLimit(e.Backend(), name)
		}
		fast = fastFor(name, jsonv.IsTrue(jsonv.Get(e.Settings, "agent_fast")))
		cancel, state = e.Agent.Cancel, e.EditorState()
		if !child {
			messages = append(messages, e.Agent.Messages...)
		}
	})
	user := prompt
	if !child {
		user = prompt + editorStateNote + state
	}
	messages = append(messages, engine.Object("role", "user", "content", jsonv.NewArray(engine.Object("type", "text", "text", user))))

	modern := false
	for _, m := range []string{"claude-opus-5", "claude-opus-6", "claude-fable-", "claude-sonnet-5", "claude-sonnet-6", "claude-haiku-5", "claude-haiku-6"} {
		modern = modern || strings.HasPrefix(name, m)
	}
	sysText := Instructions("api")
	system := jsonv.NewArray(engine.Object("type", "text", "text", sysText, "cache_control", engine.Object("type", "ephemeral")))
	tools := apiTools(e, child)
	baseOverhead := EstimateBaseOverheadTokens(sysText, tools)
	usage := engine.Object("input", jsonv.Int(0), "output", jsonv.Int(0), "cacheRead", jsonv.Int(0), "cacheWrite", jsonv.Int(0), "turns", jsonv.Int(0))
	save := func() {
		if !child {
			kept := append([]jsonv.Value(nil), messages...)
			e.Locked(func() { e.Agent.Messages = kept })
		}
	}

	notifiedCompaction := false
	for turn := 0; turn < maxTurns; turn++ {
		if cancel.IsSet() {
			break
		}
		var compacted bool
		var dropped int
		messages, compacted, dropped = EnforceContextWindow(messages, baseOverhead, contextLimit)
		if compacted && !notifiedCompaction && dropped > 0 {
			notifiedCompaction = true
			emit(e, p, "note", engine.Object("text", fmt.Sprintf("Context window limit (%dk tokens): %d earlier message(s) compacted cleanly.", contextLimit/1000, dropped)))
		}
		estInput := EstimateContextTokens(baseOverhead, messages)
		if turn == 0 {
			emitContextUsage(e, p, child, estInput, contextLimit)
		}
		maxOut := ClampMaxOutputTokens(contextLimit, estInput)
		if contextLimit > 0 && (maxOut < 64 || estInput >= contextLimit) {
			if turn > 0 {
				save()
			}
			return usage, fmt.Errorf("prompt exceeds configured context window (%d estimated input tokens, limit %d); shorten the prompt or increase the context window limit in Agent settings", estInput, contextLimit)
		}
		body := engine.Object("model", name, "max_tokens", jsonv.Int(int64(maxOut)), "system", system, "tools", tools,
			"messages", jsonv.NewArray(messages...), "output_config", engine.Object("effort", effort))
		var betas []string
		if modern {
			body.Set("fallbacks", "default")
			body.Set("thinking", engine.Object("type", "adaptive", "display", "updates"))
			betas = append(betas, "server-side-fallback-2026-07-01", "thinking-display-updates-2026-08-18")
		}
		if fast {
			body.Set("speed", "fast")
			// Opus 5.5 has fast mode generally; Opus 5 and 4.8 still need the research-preview header.
			if name != "claude-opus-5-5" {
				betas = append(betas, "fast-mode-2026-02-01")
			}
		}
		lastPreview := map[string]time.Time{}
		lastProgress := time.Now()
		resp, err := client.Stream(body, strings.Join(betas, ","), cancel.IsSet, func(ev model.Event) {
			switch ev.Kind {
			case model.EventText:
				emit(e, p, "text_delta", engine.Object("text", ev.Text))
			case model.EventThinking:
				emit(e, p, "thinking_delta", engine.Object("text", ev.Text))
			case model.EventToolStart:
				emit(e, p, "tool_start", engine.Object("id", ev.ID, "name", ev.Name))
			case model.EventToolInput:
				if ev.Name != "execute" {
					return
				}
				if t, ok := lastPreview[ev.ID]; ok && time.Since(t) < streamThrottle {
					return
				}
				if src, ok := script.PartialString(ev.JSON, "input"); ok {
					e.Locked(func() { e.StreamUpdate(ev.ID, p.ID, src) })
					if time.Since(lastProgress) >= progressThrottle {
						emit(e, p, "tool_progress", engine.Object("id", ev.ID, "lines", jsonv.Int(int64(len(lines(src))))))
						lastProgress = time.Now()
					}
				}
				lastPreview[ev.ID] = time.Now()
			}
		})
		if err != nil {
			if err.Error() == "stopped" {
				break
			}
			if turn > 0 {
				save()
			}
			return usage, err
		}
		respUsage := jsonv.Get(resp, "usage")
		for _, k := range [][2]string{{"input", "input_tokens"}, {"output", "output_tokens"}, {"cacheRead", "cache_read_input_tokens"}, {"cacheWrite", "cache_creation_input_tokens"}} {
			usage.Set(k[0], u64(asU64(jsonv.Get(usage, k[0]))+asU64(jsonv.Get(respUsage, k[1]))))
		}
		usage.Set("turns", u64(asU64(jsonv.Get(usage, "turns"))+1))
		stop := jsonv.StrOr(jsonv.Get(resp, "stop_reason"))
		content := jsonv.Clone(jsonv.Get(resp, "content"))
		blocks := jsonv.Items(content)
		stepIn := asU64(jsonv.Get(respUsage, "input_tokens")) +
			asU64(jsonv.Get(respUsage, "cache_read_input_tokens")) +
			asU64(jsonv.Get(respUsage, "cache_creation_input_tokens"))
		stepOut := asU64(jsonv.Get(respUsage, "output_tokens"))
		curCtxUsed := int(stepIn + stepOut)
		if curCtxUsed <= 0 {
			curCtxUsed = estInput + estimateMessageTokens(engine.Object("role", "assistant", "content", content))
		}
		emitContextUsage(e, p, child, curCtxUsed, contextLimit)
		emit(e, p, "message_end", engine.Object("t", u64(engine.NowMs())))
		if stop == "refusal" {
			text := "Claude declined this request."
			if why, ok := jsonv.Str(jsonv.Get(resp, "refusal")); ok {
				text = "The model declined this request: " + why
			}
			emit(e, p, "error", engine.Object("text", text))
			break
		}
		// Appended unchanged so thinking blocks stay valid for the next request.
		messages = append(messages, engine.Object("role", "assistant", "content", content))
		var calls []jsonv.Value
		for _, b := range blocks {
			if jsonv.StrIs(jsonv.Get(b, "type"), "tool_use") {
				calls = append(calls, b)
			}
		}
		cutBlock := cutOffCall(stop, blocks)
		cut, hasCut := "", cutBlock != nil
		if hasCut {
			// A truncated tool input must not run.
			cut = jsonv.StrOr(jsonv.Get(cutBlock, "id"))
			e.Locked(func() { e.StreamEnd(cut) })
			emit(e, p, "note", engine.Object("text", "The reply hit the output limit mid-tool-call; the agent was told to continue in smaller steps."))
		}
		// Every tool_use needs a tool_result after it, or the API rejects all later requests.
		if len(calls) == 0 || !(stop == "tool_use" || stop == "max_tokens") {
			break
		}
		hasSpawnInBatch := false
		for _, b := range calls {
			if jsonv.StrIs(jsonv.Get(b, "name"), "spawn_agents") {
				hasSpawnInBatch = true
				break
			}
		}
		results := jsonv.NewArray()
		for _, b := range calls {
			id, tool := jsonv.StrOr(jsonv.Get(b, "id")), jsonv.StrOr(jsonv.Get(b, "name"))
			input := jsonv.Get(b, "input")
			var out engine.ToolOut
			if raw, invalid := jsonv.Str(jsonv.Get(input, "__invalid_json")); hasCut && cut == id {
				out = engine.ToolOut{Text: "The reply was cut off by the output limit; this call did not run. Continue in smaller steps.", Error: true}
			} else if invalid {
				e.Locked(func() { e.StreamEnd(id) })
				out = engine.ToolOut{Text: string(jsonv.Marshal(engine.Object("INVALID_JSON", raw))), Error: true}
			} else if tool == "spawn_agents" {
				out = SpawnChildren(e, p.ID, input)
			} else {
				args := setKey(jsonv.Clone(input), "__stream", id)
				args = setKey(args, "__agent", p.ID)
				out = e.CallTool(tool, args)
			}
			// Outside the engine lock: screenshots rasterize here.
			auditSnap := out.Audit
			out = out.Finish(e)
			out.Text = AppendDesignAuditWithSnapshot(e, auditSnap, tool, out.Text, out.Error)
			if !child && tool == "execute" && !out.Error && !hasSpawnInBatch {
				needSpawnHint := false
				e.Locked(func() {
					needSpawnHint = e.Agent.Parallel > 1 && e.Agent.Spawned == 0 && len(e.Agent.TurnScreenTeam) >= 2
				})
				if needSpawnHint {
					out.Text += "\n\n[Parallel team ready] Multiple screen frames exist and 0 designers have been spawned yet — call `spawn_agents` now to delegate the extra screen frame(s) to your parallel designers."
				}
			}
			emit(e, p, "tool_done", toolDoneEvent(id, tool, input, out.Text, out.Images, out.Error))
			parts := jsonv.NewArray(engine.Object("type", "text", "text", out.Text))
			for _, img := range out.Images {
				parts.Items = append(parts.Items, engine.Object("type", "image", "source", engine.Object("type", "base64", "media_type", "image/png", "data", img)))
			}
			results.Items = append(results.Items, engine.Object("type", "tool_result", "tool_use_id", id, "content", parts, "is_error", out.Error))
		}
		messages = append(messages, engine.Object("role", "user", "content", results))
		save()
	}
	save()
	return usage, nil
}

// setKey is serde's `v[k] = x`: null becomes an object first; any other non-object panics (and fails the run),
// as serde's IndexMut does.
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

func u64(v uint64) jsonv.Number { return jsonv.Number(fmt.Sprint(v)) }

// asU64 is serde's `as_u64().unwrap_or(0)`.
func asU64(v jsonv.Value) uint64 {
	n, _ := v.(jsonv.Number)
	u, _ := n.Uint64()
	return u
}

// cutOffCall is the tool call the output limit cut off. Only the last block can be unfinished; earlier calls
// completed.
func cutOffCall(stop string, blocks []jsonv.Value) jsonv.Value {
	if stop != "max_tokens" || len(blocks) == 0 {
		return nil
	}
	if last := blocks[len(blocks)-1]; jsonv.StrIs(jsonv.Get(last, "type"), "tool_use") {
		return last
	}
	return nil
}

// toolDoneEvent is a finished tool call for the chat. The output is cut for the UI; issues keeps every "Issues
// detected" line.
func toolDoneEvent(id, name string, input jsonv.Value, text string, images []string, isError bool) *jsonv.Object {
	issues := []string{}
	if _, rest, ok := strings.Cut(text, "\n## Issues detected"); ok {
		ls := lines(rest)
		for _, l := range ls[min(1, len(ls)):] {
			item, ok := strings.CutPrefix(l, "- ")
			if !ok {
				break
			}
			issues = append(issues, item)
		}
	}
	// `t` (ms since the epoch, as on `message_end`): the time since the model's message ended is what its tools
	// took.
	return engine.Object("id", id, "name", name, "input", jsonv.Clone(input), "output", truncate(text, 6000), "images", engine.Strings(images[:min(3, len(images))]),
		"error", isError, "issues", engine.Strings(issues), "t", u64(engine.NowMs()))
}

// lines is Rust's `str::lines`: split at `\n` or `\r\n`, with no empty line after a final line ending.
func lines(s string) []string {
	var out []string
	for s != "" {
		l, rest, found := strings.Cut(s, "\n")
		if found {
			l = strings.TrimSuffix(l, "\r")
		}
		out = append(out, l)
		s = rest
	}
	return out
}

// charBoundary is Rust's `is_char_boundary`.
func charBoundary(s string, i int) bool { return i >= len(s) || utf8.RuneStart(s[i]) }

// truncate is s when it fits n bytes, else its first n bytes (back to a char boundary) and an ellipsis.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for !charBoundary(s, cut) {
		cut--
	}
	return s[:cut] + "…"
}

// clipEnds is s unchanged when it fits n bytes, else its first and last bytes around an ellipsis, at most n in
// all (a stack trace names the error first, a debug log ends with it).
func clipEnds(s string, n int) string {
	if len(s) <= n {
		return s
	}
	half := max(n-len("\n…\n"), 0) / 2
	head := half
	for !charBoundary(s, head) {
		head--
	}
	tail := len(s) - half
	for !charBoundary(s, tail) {
		tail++
	}
	return strings.TrimRightFunc(s[:head], unicode.IsSpace) + "\n…\n" + strings.TrimLeftFunc(s[tail:], unicode.IsSpace)
}
