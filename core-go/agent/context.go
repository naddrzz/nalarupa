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
// Returns <= 0 when estimatedInput already exhausts contextLimit.
func ClampMaxOutputTokens(contextLimit, estimatedInput int) int {
	if contextLimit <= 0 {
		return defaultMaxOutTokens
	}
	rawRem := contextLimit - estimatedInput
	if rawRem <= 0 {
		return 0
	}
	quarter := max(minOutTokensFloor, contextLimit/4)
	desired := min(defaultMaxOutTokens, quarter)
	guard := min(256, rawRem/4)
	avail := max(1, rawRem-guard)
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
		isOlderUser := jsonv.StrIs(jsonv.Get(mc, "role"), "user") && i != lastHumanIdx
		keptBlocks := make([]jsonv.Value, 0, len(blocks.Items))
		omittedUserImg := false
		for bi, b := range blocks.Items {
			ty := jsonv.StrOr(jsonv.Get(b, "type"))
			if ty == "image" && isOlderUser {
				omittedUserImg = true
				changed = true
				continue
			}
			if ty == "text" && bi == 0 && isOlderUser {
				txt := jsonv.StrOr(jsonv.Get(b, "text"))
				if idx := editorStateNoteIndex(mc, txt); idx >= 0 {
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
			}
			keptBlocks = append(keptBlocks, b)
		}
		if omittedUserImg {
			keptBlocks = append(keptBlocks, engine.Object("type", "text", "text", "[Earlier attached image(s) omitted to fit context window.]"))
			blocks.Items = keptBlocks
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

	// Pass 4: If a single-turn or 1-tool history still exceeds maxInput, strip images and compact oversized
	// tool_result / editor_state blocks inside the remaining messages while keeping user prompts intact.
	blockCompacted := false
	if EstimateContextTokens(baseOverhead, cur) > maxInput {
		cur, blockCompacted = compactOversizedTurnBlocks(cur, baseOverhead, maxInput)
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
	return cur, stripped || dropped > 0 || blockCompacted, dropped
}

func compactOversizedTurnBlocks(messages []jsonv.Value, baseOverhead, maxInput int) ([]jsonv.Value, bool) {
	changed := false
	out := make([]jsonv.Value, len(messages))
	for i, m := range messages {
		out[i] = jsonv.Clone(m)
	}
	// Step 4a: Strip screenshot images and attached editor_state snapshots if needed.
	for i := range out {
		if EstimateContextTokens(baseOverhead, out) <= maxInput {
			return out, changed
		}
		blocks := jsonv.Arr(jsonv.Get(out[i], "content"))
		if blocks == nil {
			continue
		}
		for bi, b := range blocks.Items {
			ty := jsonv.StrOr(jsonv.Get(b, "type"))
			if ty == "tool_result" {
				if trContent := jsonv.Arr(jsonv.Get(b, "content")); trContent != nil {
					kept := make([]jsonv.Value, 0, len(trContent.Items))
					omitted := false
					for _, sub := range trContent.Items {
						if jsonv.StrIs(jsonv.Get(sub, "type"), "image") {
							omitted = true
							changed = true
							continue
						}
						kept = append(kept, sub)
					}
					if omitted {
						kept = append(kept, engine.Object("type", "text", "text", "[Screenshot omitted to fit context window.]"))
						trContent.Items = kept
					}
				}
			} else if ty == "text" && bi == 0 && jsonv.StrIs(jsonv.Get(out[i], "role"), "user") {
				txt := jsonv.StrOr(jsonv.Get(b, "text"))
				if idx := editorStateNoteIndex(out[i], txt); idx >= 0 {
					statePart := txt[idx+len(editorStateNote):]
					if len(statePart) > 1200 {
						trimmed := txt[:idx+len(editorStateNote)] + truncate(statePart, 1200) + "\n[editor_state truncated to fit context window.]"
						if obj := jsonv.Obj(b); obj != nil {
							obj.Set("text", trimmed)
							changed = true
						}
					}
				}
			}
		}
	}
	// Step 4a2: If still over maxInput, strip remaining top-level user image blocks (including latest human turn)
	// while preserving index-0 text and message alternation.
	for i := range out {
		if EstimateContextTokens(baseOverhead, out) <= maxInput {
			return out, changed
		}
		if !jsonv.StrIs(jsonv.Get(out[i], "role"), "user") {
			continue
		}
		blocks := jsonv.Arr(jsonv.Get(out[i], "content"))
		if blocks == nil {
			continue
		}
		kept := make([]jsonv.Value, 0, len(blocks.Items))
		omitted := false
		for _, b := range blocks.Items {
			if jsonv.StrIs(jsonv.Get(b, "type"), "image") {
				omitted = true
				changed = true
				continue
			}
			kept = append(kept, b)
		}
		if omitted {
			kept = append(kept, engine.Object("type", "text", "text", "[Attached image(s) omitted to fit context window.]"))
			blocks.Items = kept
		}
	}
	// Step 4b: Truncate oversized tool_result text blocks so a huge Get(document) output cannot exceed maxInput.
	const truncNotice = "\n[tool_result truncated to fit context window.]"
	noticeToks := estimateStringTokens(truncNotice)
	for i := range out {
		excess := EstimateContextTokens(baseOverhead, out) - maxInput
		if excess <= 0 {
			break
		}
		blocks := jsonv.Arr(jsonv.Get(out[i], "content"))
		if blocks == nil {
			continue
		}
		for _, b := range blocks.Items {
			if !jsonv.StrIs(jsonv.Get(b, "type"), "tool_result") {
				continue
			}
			excess = EstimateContextTokens(baseOverhead, out) - maxInput
			if excess <= 0 {
				break
			}
			trimTextBlock := func(obj *jsonv.Object, key string) {
				s := jsonv.StrOr(jsonv.Get(obj, key))
				curToks := estimateStringTokens(s)
				if curToks <= 256 {
					return
				}
				keepToks := max(128, curToks-excess-noticeToks-8)
				keepBytes := max(384, int(float64(keepToks)*approxCharsPerToken))
				if keepBytes+len(truncNotice) < len(s) {
					obj.Set(key, truncate(s, keepBytes)+truncNotice)
					changed = true
				}
			}
			if bObj := jsonv.Obj(b); bObj != nil {
				if _, isStr := jsonv.Str(jsonv.Get(bObj, "content")); isStr {
					trimTextBlock(bObj, "content")
				} else if trArr := jsonv.Arr(jsonv.Get(bObj, "content")); trArr != nil {
					for _, sub := range trArr.Items {
						excess = EstimateContextTokens(baseOverhead, out) - maxInput
						if excess <= 0 {
							break
						}
						if sObj := jsonv.Obj(sub); sObj != nil && jsonv.StrIs(jsonv.Get(sObj, "type"), "text") {
							trimTextBlock(sObj, "text")
						}
					}
				}
			}
		}
	}
	// Step 4c: If a batch of many small tool_result items still exceeds maxInput in aggregate, compact
	// individual result bodies down to a minimal stub while keeping every tool_result and its tool_use_id intact.
	const compactStub = "[truncated]"
	for i := range out {
		if EstimateContextTokens(baseOverhead, out) <= maxInput {
			break
		}
		blocks := jsonv.Arr(jsonv.Get(out[i], "content"))
		if blocks == nil {
			continue
		}
		for _, b := range blocks.Items {
			if EstimateContextTokens(baseOverhead, out) <= maxInput {
				break
			}
			if !jsonv.StrIs(jsonv.Get(b, "type"), "tool_result") {
				continue
			}
			bObj := jsonv.Obj(b)
			if bObj == nil {
				continue
			}
			if s, isStr := jsonv.Str(jsonv.Get(bObj, "content")); isStr {
				if s != compactStub && len(s) > len(compactStub) {
					bObj.Set("content", compactStub)
					changed = true
				}
				continue
			}
			if trArr := jsonv.Arr(jsonv.Get(bObj, "content")); trArr != nil {
				if len(trArr.Items) == 1 {
					if sObj := jsonv.Obj(trArr.Items[0]); sObj != nil &&
						jsonv.StrIs(jsonv.Get(sObj, "type"), "text") &&
						jsonv.StrIs(jsonv.Get(sObj, "text"), compactStub) {
						continue
					}
				}
				trArr.Items = []jsonv.Value{engine.Object("type", "text", "text", compactStub)}
				changed = true
			}
		}
	}
	return out, changed
}

func editorStateNoteIndex(msg jsonv.Value, txt string) int {
	if pLenF, ok := jsonv.F64(jsonv.Get(msg, "__prompt_len")); ok {
		pLen := int(pLenF)
		if pLen >= 0 && pLen+len(editorStateNote) <= len(txt) && txt[pLen:pLen+len(editorStateNote)] == editorStateNote {
			return pLen
		}
		return -1
	}
	return strings.LastIndex(txt, editorStateNote)
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
