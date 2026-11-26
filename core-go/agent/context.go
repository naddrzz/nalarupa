package agent

import (
	"fmt"
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

const (
	approxCharsPerToken = 3.6
	imageTokensApprox   = 1250
	defaultMaxOutTokens = 32000
	minOutTokensFloor   = 1024
)

func estimateStringTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return int(math.Ceil(float64(len(s)) / approxCharsPerToken))
}

func estimateBlockTokens(b jsonv.Value) int {
	if b == nil {
		return 0
	}
	if s, ok := jsonv.Str(b); ok {
		return estimateStringTokens(s) + 4
	}
	ty := jsonv.StrOr(jsonv.Get(b, "type"))
	switch ty {
	case "image":
		return imageTokensApprox
	case "text":
		return estimateStringTokens(jsonv.StrOr(jsonv.Get(b, "text"))) + 6
	case "thinking":
		return estimateStringTokens(jsonv.StrOr(jsonv.Get(b, "thinking"))) + 8
	case "tool_use":
		inRaw := jsonv.Marshal(jsonv.Get(b, "input"))
		return estimateStringTokens(jsonv.StrOr(jsonv.Get(b, "name"))) + estimateStringTokens(string(inRaw)) + 18
	case "tool_result":
		sum := 14
		content := jsonv.Get(b, "content")
		if s, ok := jsonv.Str(content); ok {
			sum += estimateStringTokens(s)
		} else {
			for _, sub := range jsonv.Items(content) {
				sum += estimateBlockTokens(sub)
			}
		}
		return sum
	default:
		raw := jsonv.Marshal(b)
		return estimateStringTokens(string(raw)) + 6
	}
}

func estimateMessageTokens(msg jsonv.Value) int {
	if msg == nil {
		return 0
	}
	sum := 8
	content := jsonv.Get(msg, "content")
	if s, ok := jsonv.Str(content); ok {
		return sum + estimateStringTokens(s)
	}
	for _, b := range jsonv.Items(content) {
		sum += estimateBlockTokens(b)
	}
	return sum
}

// EstimateBaseOverheadTokens estimates the static token cost of system instructions and tool definitions.
func EstimateBaseOverheadTokens(systemText string, tools *jsonv.Array) int {
	toks := estimateStringTokens(systemText) + 24
	if tools != nil {
		raw := jsonv.Marshal(tools)
		toks += estimateStringTokens(string(raw))
	}
	return toks
}

// EstimateContextTokens estimates the total input context tokens of system + tools + messages.
func EstimateContextTokens(baseOverhead int, messages []jsonv.Value) int {
	total := baseOverhead
	for _, m := range messages {
		total += estimateMessageTokens(m)
	}
	return total
}

// ClampMaxOutputTokens ensures max_tokens never exceeds the model's remaining context window on smaller/custom providers.
func ClampMaxOutputTokens(contextLimit, estimatedInput int) int {
	if contextLimit <= 0 {
		return defaultMaxOutTokens
	}
	// Cap completion budget to at most 25% of smaller windows (or 32000 max), and strictly inside remaining window.
	quarter := max(minOutTokensFloor, contextLimit/4)
	desired := min(defaultMaxOutTokens, quarter)
	avail := contextLimit - estimatedInput - 256
	if avail < minOutTokensFloor {
		return min(minOutTokensFloor, max(256, contextLimit/8))
	}
	return min(desired, avail)
}

func isToolResultUserMsg(msg jsonv.Value) bool {
	if !jsonv.StrIs(jsonv.Get(msg, "role"), "user") {
		return false
	}
	for _, b := range jsonv.Items(jsonv.Get(msg, "content")) {
		if jsonv.StrIs(jsonv.Get(b, "type"), "tool_result") {
			return true
		}
	}
	return false
}

func stripStaleImagesAndEditorState(messages []jsonv.Value) ([]jsonv.Value, bool) {
	if len(messages) <= 2 {
		return messages, false
	}
	changed := false
	out := make([]jsonv.Value, len(messages))
	lastHumanIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if jsonv.StrIs(jsonv.Get(messages[i], "role"), "user") && !isToolResultUserMsg(messages[i]) {
			lastHumanIdx = i
			break
		}
	}
	for i, m := range messages {
		// Keep the last 2 messages completely untouched so recent screenshots and active state stay full-fidelity.
		if i >= len(messages)-2 {
			out[i] = m
			continue
		}
		mc := jsonv.Clone(m)
		blocks := jsonv.Arr(jsonv.Get(mc, "content"))
		if blocks == nil {
			out[i] = mc
			continue
		}
		for bi, b := range blocks.Items {
			ty := jsonv.StrOr(jsonv.Get(b, "type"))
			if ty == "text" && i != lastHumanIdx && jsonv.StrIs(jsonv.Get(mc, "role"), "user") {
				txt := jsonv.StrOr(jsonv.Get(b, "text"))
				if idx := strings.Index(txt, editorStateNote); idx >= 0 {
					trimmed := txt[:idx] + "\n\n[Earlier editor_state snapshot omitted; see latest turn for current state.]"
					if obj := jsonv.Obj(b); obj != nil {
						obj.Set("text", trimmed)
						changed = true
					}
				}
			} else if ty == "tool_result" {
				trContent := jsonv.Arr(jsonv.Get(b, "content"))
				if trContent != nil {
					keptSub := make([]jsonv.Value, 0, len(trContent.Items))
					omittedImg := false
					for _, sub := range trContent.Items {
						if jsonv.StrIs(jsonv.Get(sub, "type"), "image") {
							omittedImg = true
							changed = true
							continue
						}
						keptSub = append(keptSub, sub)
					}
					if omittedImg {
						keptSub = append(keptSub, engine.Object("type", "text", "text", "[Earlier screenshot image omitted to fit context window.]"))
						trContent.Items = keptSub
					}
				}
				_ = bi
			}
		}
		out[i] = mc
	}
	return out, changed
}

// EnforceContextWindow trims older context safely when baseOverhead + messages approaches contextLimit,
// preserving strict user/assistant alternation and matched tool_use / tool_result pairs.
func EnforceContextWindow(messages []jsonv.Value, baseOverhead, contextLimit int) ([]jsonv.Value, bool, int) {
	if contextLimit <= 0 || len(messages) == 0 {
		return messages, false, 0
	}
	reserveOut := min(16384, max(minOutTokensFloor, contextLimit/5))
	maxInput := max(1536, contextLimit-reserveOut)
	if EstimateContextTokens(baseOverhead, messages) <= maxInput {
		return messages, false, 0
	}

	// Pass 1: Losslessly strip stale screenshots & superseded editor_state snapshots from older turns.
	cur, stripped := stripStaleImagesAndEditorState(messages)
	if EstimateContextTokens(baseOverhead, cur) <= maxInput {
		return cur, stripped, 0
	}

	// Pass 2: Drop oldest complete human turn groups (each starting at a non-tool_result user message).
	var humanStarts []int
	for i, m := range cur {
		if jsonv.StrIs(jsonv.Get(m, "role"), "user") && !isToolResultUserMsg(m) {
			humanStarts = append(humanStarts, i)
		}
	}
	dropped := 0
	for len(humanStarts) > 1 && EstimateContextTokens(baseOverhead, cur) > maxInput {
		nextStart := humanStarts[1]
		dropped += nextStart
		cur = append([]jsonv.Value(nil), cur[nextStart:]...)
		for k := 1; k < len(humanStarts); k++ {
			humanStarts[k] -= nextStart
		}
		humanStarts = humanStarts[1:]
	}

	// Pass 3: If a single multi-step turn still exceeds maxInput, drop oldest (assistant tool_use, user tool_result)
	// pairs right after cur[0] (the opening human prompt), keeping at least the latest pair intact!
	// Because cur[0] is user and cur[3] is assistant, dropping cur[1..2] preserves strict user -> assistant alternation!
	for len(cur) >= 5 && EstimateContextTokens(baseOverhead, cur) > maxInput {
		if jsonv.StrIs(jsonv.Get(cur[1], "role"), "assistant") && isToolResultUserMsg(cur[2]) {
			dropped += 2
			next := make([]jsonv.Value, 0, len(cur)-2)
			next = append(next, cur[0])
			next = append(next, cur[3:]...)
			cur = next
		} else {
			break
		}
	}

	if dropped > 0 && len(cur) > 0 {
		first := jsonv.Clone(cur[0])
		if blocks := jsonv.Arr(jsonv.Get(first, "content")); blocks != nil && len(blocks.Items) > 0 {
			note := fmt.Sprintf("[Context window guard: %d earlier message(s) compacted to stay within the %d-token context window.]\n\n", dropped, contextLimit)
			if obj := jsonv.Obj(blocks.Items[0]); obj != nil && jsonv.StrIs(jsonv.Get(obj, "type"), "text") {
				prevTxt := jsonv.StrOr(jsonv.Get(obj, "text"))
				if !strings.HasPrefix(prevTxt, "[Context window guard:") {
					obj.Set("text", note+prevTxt)
				}
			}
		}
		cur[0] = first
	}
	return cur, stripped || dropped > 0, dropped
}

func emitContextUsage(e *engine.Engine, _ engine.Profile, child bool, used, limit int) {
	if child || limit <= 0 {
		return
	}
	if used < 0 {
		used = 0
	}
	rem := max(0, limit-used)
	e.Locked(func() {
		e.Agent.ContextUsed = used
		e.Agent.ContextLimit = limit
		e.Emit(engine.Object(
			"type", "agent_context",
			"used", jsonv.Int(int64(used)),
			"limit", jsonv.Int(int64(limit)),
			"remaining", jsonv.Int(int64(rem)),
		))
	})
}
