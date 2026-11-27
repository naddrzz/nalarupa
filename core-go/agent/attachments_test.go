package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
)

func tinyPNGBase64(t *testing.T, r, g, b uint8) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	col := color.RGBA{R: r, G: g, B: b, A: 255}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetRGBA(x, y, col)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestAgentSendCanvasContextAndImagesAPIAndHistoryReload(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		return textReply("Updated Hero.", "end_turn")
	})
	e := apiEngine(t, url)

	// Create two frames: Hero and Footer. Select Footer, but pin Hero as canvas context!
	out := e.CallTool("execute", engine.Object("input", `
		h = Insert(document, {type: "frame", name: "Hero", x: 0, y: 0, width: 320, height: 180, fill: "#112233"});
		Insert(h, {type: "text", name: "Title", content: "Welcome", fontSize: 24, fill: "#FFFFFF"});
		f = Insert(document, {type: "frame", name: "Footer", x: 400, y: 0, width: 320, height: 120, fill: "#332211"});
	`)).Finish(e)
	if out.Error {
		t.Fatalf("execute failed: %s", out.Text)
	}
	heroID := nodeID(e, "Hero")
	footerID := nodeID(e, "Footer")
	e.Locked(func() { e.Selection = []string{footerID} })

	img1 := tinyPNGBase64(t, 220, 40, 40)
	img2 := tinyPNGBase64(t, 40, 120, 220)

	req := engine.Object(
		"cmd", "agent_send",
		"text", "Redesign Hero in this style",
		"contexts", jsonv.NewArray(engine.Object("kind", "node", "id", heroID)),
		"images", jsonv.NewArray(
			engine.Object("name", "ref1.png", "mediaType", "image/png", "data", img1),
			engine.Object("name", "ref2.png", "mediaType", "image/png", "data", img2),
		),
	)
	if _, err := e.Dispatch(req); err != nil {
		t.Fatalf("agent_send: %v", err)
	}
	waitIdle(t, e)

	reqs := api.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 API request, got %d", len(reqs))
	}
	msgs := jsonv.Items(jsonv.Get(reqs[0].body, "messages"))
	if len(msgs) != 1 {
		t.Fatalf("expected 1 user message, got %d", len(msgs))
	}
	// __prompt_len must be stripped on the wire
	if obj := jsonv.Obj(msgs[0]); obj == nil || obj.Has("__prompt_len") {
		t.Fatalf("expected __prompt_len stripped on wire, got %s", jsonv.Marshal(msgs[0]))
	}
	blocks := jsonv.Items(jsonv.Get(msgs[0], "content"))
	if len(blocks) != 4 {
		t.Fatalf("expected 4 content blocks (1 text + 1 Hero PNG snapshot + 2 user images), got %d", len(blocks))
	}
	if !jsonv.StrIs(jsonv.Get(blocks[0], "type"), "text") {
		t.Fatalf("content[0] must remain text, got %s", jsonv.Marshal(blocks[0]))
	}
	promptText := jsonv.StrOr(jsonv.Get(blocks[0], "text"))
	if !strings.Contains(promptText, "[Attached canvas context]") || !strings.Contains(promptText, heroID) || !strings.Contains(promptText, `"Hero"`) {
		t.Fatalf("missing pinned Hero canvas context in prompt: %s", promptText)
	}
	for i := 1; i <= 3; i++ {
		if !jsonv.StrIs(jsonv.Get(blocks[i], "type"), "image") {
			t.Fatalf("block %d expected image, got %s", i, jsonv.Marshal(blocks[i]))
		}
	}
	// Block 1 is auto-rendered PNG of Hero; blocks 2 & 3 are user images.
	if got := jsonv.StrOr(jsonv.Get(jsonv.Get(blocks[2], "source"), "data")); got != img1 {
		t.Fatalf("block 2 did not carry user img1")
	}
	if got := jsonv.StrOr(jsonv.Get(jsonv.Get(blocks[3], "source"), "data")); got != img2 {
		t.Fatalf("block 3 did not carry user img2")
	}

	// Verify emitted user event carries contexts and ONLY user-attached images (2), and persists across chat reload.
	evs := agentEvents(e)
	userEv := lastOf(evs, "user")
	if userEv == nil {
		t.Fatal("missing user event")
	}
	evCtxs := jsonv.Items(jsonv.Get(userEv, "contexts"))
	if len(evCtxs) != 1 || jsonv.StrOr(jsonv.Get(evCtxs[0], "id")) != heroID {
		t.Fatalf("unexpected user event contexts: %s", jsonv.Marshal(userEv))
	}
	evImgs := jsonv.Items(jsonv.Get(userEv, "images"))
	if len(evImgs) != 2 || jsonv.StrOr(evImgs[0]) != img1 || jsonv.StrOr(evImgs[1]) != img2 {
		t.Fatalf("unexpected user event images: len=%d", len(evImgs))
	}

	var chatID string
	e.Locked(func() { chatID = e.Agent.Chat.ID })
	if err := flushChatSaves(); err != nil {
		t.Fatalf("flushChatSaves: %v", err)
	}
	loaded, err := LoadChat(e, chatID)
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	loadedEvents := jsonv.Items(jsonv.Get(loaded, "events"))
	loadedUser := lastOf(loadedEvents, "user")
	if loadedUser == nil {
		t.Fatal("reloaded chat missing user event")
	}
	if len(jsonv.Items(jsonv.Get(loadedUser, "contexts"))) != 1 || len(jsonv.Items(jsonv.Get(loadedUser, "images"))) != 2 {
		t.Fatalf("reloaded user event lost attachments: %s", jsonv.Marshal(loadedUser))
	}
}

func TestAgentSendDeletedPinnedNodeEmitsToastAndOmitsStaleNode(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		return textReply("OK", "end_turn")
	})
	e := apiEngine(t, url)
	out := e.CallTool("execute", engine.Object("input", `Insert(document, {type: "frame", name: "Kept", x: 0, y: 0, width: 100, height: 80, fill: "#eee"})`)).Finish(e)
	if out.Error {
		t.Fatal(out.Text)
	}
	keptID := nodeID(e, "Kept")

	req := engine.Object(
		"cmd", "agent_send",
		"text", "Inspect these",
		"contexts", jsonv.NewArray(
			engine.Object("kind", "node", "id", "deleted_node_99"),
			engine.Object("kind", "node", "id", keptID),
		),
	)
	if _, err := e.Dispatch(req); err != nil {
		t.Fatalf("agent_send: %v", err)
	}
	waitIdle(t, e)

	var allEvs []jsonv.Value
	e.Locked(func() { allEvs = e.TakeEvents() })
	sawToast := false
	for _, ev := range allEvs {
		if jsonv.StrIs(jsonv.Get(ev, "type"), "toast") && strings.Contains(jsonv.StrOr(jsonv.Get(ev, "text")), "deleted_node_99") {
			sawToast = true
		}
	}
	if !sawToast {
		t.Fatalf("expected toast mentioning deleted_node_99, got events: %s", jsonv.Marshal(jsonv.NewArray(allEvs...)))
	}
	reqs := api.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 API request, got %d", len(reqs))
	}
	txt := jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(jsonv.Items(jsonv.Get(reqs[0].body, "messages"))[0], "content"))[0], "text"))
	if strings.Contains(txt, "deleted_node_99") {
		t.Fatalf("stale deleted node leaked into prompt: %s", txt)
	}
	if !strings.Contains(txt, keptID) {
		t.Fatalf("valid node missing from prompt: %s", txt)
	}
}

func TestAgentSendViewportContextPinnedBoundsAndVisionFalseProvider(t *testing.T) {
	// Configure an OpenAI-chat BYOK provider with vision: false to verify:
	// 1. Canvas viewport context sends pinned bounds & visible nodes as text, WITHOUT generating PNG snapshot.
	// 2. Explicit user images still do not drop the user message when translated with vision: false.
	var lastReq jsonv.Value
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		w.Header().Set("content-type", "text/event-stream")
		return "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Noted.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	})
	e := newEngine(t)
	e.Locked(func() {
		e.Settings.Set("agent_backend", "provider")
		e.Settings.Set("agent_provider", "blind_p")
		e.Settings.Set("provider_model", "text-only-1")
		e.Settings.Set("providers", jsonv.NewArray(engine.Object(
			"id", "blind_p",
			"name", "Blind",
			"format", "chat",
			"base_url", url+"/v1",
			"api_key", "k",
			"models", engine.Strings([]string{"text-only-1"}),
			"vision", false,
		)))
	})
	out := e.CallTool("execute", engine.Object("input", `Insert(document, {type: "frame", name: "InView", x: 20, y: 30, width: 200, height: 150, fill: "#224466"})`)).Finish(e)
	if out.Error {
		t.Fatal(out.Text)
	}
	// Move live viewport far away after pinning [0, 0, 400, 300]
	e.Locked(func() {
		vp := [4]float64{5000, 5000, 400, 300}
		e.Viewport = &vp
	})
	img := tinyPNGBase64(t, 10, 200, 10)
	req := engine.Object(
		"cmd", "agent_send",
		"text", "",
		"contexts", jsonv.NewArray(engine.Object(
			"kind", "viewport",
			"viewport", jsonv.NewArray(jsonv.Float(0), jsonv.Float(0), jsonv.Float(400), jsonv.Float(300)),
		)),
		"images", jsonv.NewArray(engine.Object("name", "a.png", "mediaType", "image/png", "data", img)),
	)
	if _, err := e.Dispatch(req); err != nil {
		t.Fatalf("agent_send: %v", err)
	}
	waitIdle(t, e)

	reqs := api.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	lastReq = reqs[0].body
	raw := string(jsonv.Marshal(lastReq))
	if strings.Contains(raw, `"image_url"`) {
		t.Fatalf("vision:false provider must not receive image_url blocks: %s", raw)
	}
	if !strings.Contains(raw, "InView") || !strings.Contains(raw, "400×300") {
		t.Fatalf("expected pinned viewport bounds and visible node InView in request: %s", raw)
	}
	if !strings.Contains(raw, "cannot see images") {
		t.Fatalf("expected fallback blind-image note in request: %s", raw)
	}
}

func TestClaudeCodeMultimodalStreamJSONAndPlainText(t *testing.T) {
	e, log := ccEngine(t, "ok")

	// 1. Plain text turn (0 images) -> NO --input-format flag, plain text stdin
	if err := Send(e, "Plain prompt", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)
	all := runs(t, log)
	if len(all) != 1 {
		t.Fatalf("expected 1 claude run, got %d", len(all))
	}
	args0 := argsOf(all[0])
	if slices.Contains(args0, "--input-format") {
		t.Fatalf("text-only claude-code run must not pass --input-format: %v", args0)
	}
	stdin0 := jsonv.StrOr(jsonv.Get(all[0], "stdin"))
	if !strings.HasPrefix(stdin0, "Plain prompt") {
		t.Fatalf("expected plain text stdin, got %q", stdin0)
	}

	// 2. Multimodal turn (2 images) -> --input-format stream-json + JSONL envelope on stdin
	img1 := tinyPNGBase64(t, 255, 0, 0)
	img2 := tinyPNGBase64(t, 0, 255, 0)
	err := SendWithAttachments(e, "Use these references", nil, []engine.AttachmentImage{
		{Name: "r1.png", MediaType: "image/png", Data: img1},
		{Name: "r2.png", MediaType: "image/png", Data: img2},
	}, 1, "split", 1, "layout")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)
	all = runs(t, log)
	if len(all) != 2 {
		t.Fatalf("expected 2 claude runs, got %d", len(all))
	}
	args1 := argsOf(all[1])
	idx := slices.Index(args1, "--input-format")
	if idx < 0 || idx+1 >= len(args1) || args1[idx+1] != "stream-json" {
		t.Fatalf("expected --input-format stream-json in args: %v", args1)
	}
	stdin1 := jsonv.StrOr(jsonv.Get(all[1], "stdin"))
	if !strings.HasSuffix(stdin1, "\n") {
		t.Fatalf("stream-json stdin must terminate with newline")
	}
	parsed, perr := jsonv.Parse([]byte(strings.TrimSpace(stdin1)))
	if perr != nil {
		t.Fatalf("stream-json stdin is not valid JSON: %v (%q)", perr, stdin1)
	}
	if !jsonv.StrIs(jsonv.Get(parsed, "type"), "user") ||
		!jsonv.StrIs(jsonv.Get(jsonv.Get(parsed, "message"), "role"), "user") {
		t.Fatalf("unexpected stream-json envelope: %s", jsonv.Marshal(parsed))
	}
	content := jsonv.Items(jsonv.Get(jsonv.Get(parsed, "message"), "content"))
	if len(content) != 3 {
		t.Fatalf("expected 3 blocks (1 text + 2 images), got %d", len(content))
	}
	if !jsonv.StrIs(jsonv.Get(content[0], "type"), "text") ||
		!strings.Contains(jsonv.StrOr(jsonv.Get(content[0], "text")), "Use these references") {
		t.Fatalf("unexpected text block: %s", jsonv.Marshal(content[0]))
	}
	if jsonv.StrOr(jsonv.Get(jsonv.Get(content[1], "source"), "data")) != img1 ||
		jsonv.StrOr(jsonv.Get(jsonv.Get(content[2], "source"), "data")) != img2 {
		t.Fatalf("image blocks did not match attached images")
	}
}

func TestQueuedAttachmentsCoalesceAndStopDiscards(t *testing.T) {
	gate := make(chan struct{})
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		if n == 0 {
			<-gate
			return textReply("First done.", "end_turn")
		}
		return textReply("Coalesced done.", "end_turn")
	})
	e := apiEngine(t, url)
	out := e.CallTool("execute", engine.Object("input", `
		Insert(document, {type: "frame", name: "CardA", x: 0, y: 0, width: 120, height: 80, fill: "#aaa"});
		Insert(document, {type: "frame", name: "CardB", x: 200, y: 0, width: 120, height: 80, fill: "#bbb"});
	`)).Finish(e)
	if out.Error {
		t.Fatal(out.Text)
	}
	idA := nodeID(e, "CardA")
	idB := nodeID(e, "CardB")
	imgA := tinyPNGBase64(t, 12, 34, 56)
	imgB := tinyPNGBase64(t, 78, 90, 12)

	if err := Send(e, "Initial turn", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	api.wait(t)

	// Queue two messages with contexts and images while agent is running
	if err := SendWithAttachments(e, "Follow up 1",
		[]engine.CanvasContextRef{{Kind: "node", ID: idA}},
		[]engine.AttachmentImage{{Name: "a.png", MediaType: "image/png", Data: imgA}},
		1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	if err := SendWithAttachments(e, "Follow up 2",
		[]engine.CanvasContextRef{{Kind: "node", ID: idA}, {Kind: "node", ID: idB}},
		[]engine.AttachmentImage{{Name: "b.png", MediaType: "image/png", Data: imgB}},
		1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}

	close(gate)
	waitIdle(t, e)

	reqs := api.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (initial + coalesced queued), got %d", len(reqs))
	}
	msgs := jsonv.Items(jsonv.Get(reqs[1].body, "messages"))
	lastUser := msgs[len(msgs)-1]
	blocks := jsonv.Items(jsonv.Get(lastUser, "content"))
	// 1 text + 2 deduped canvas snapshots (CardA, CardB) + 2 user images (imgA, imgB) = 5 blocks
	if len(blocks) != 5 {
		t.Fatalf("expected 5 blocks in coalesced turn, got %d", len(blocks))
	}
	txt := jsonv.StrOr(jsonv.Get(blocks[0], "text"))
	if !strings.Contains(txt, "Follow up 1\n\nFollow up 2") || !strings.Contains(txt, idA) || !strings.Contains(txt, idB) {
		t.Fatalf("coalesced prompt missing expected text/contexts: %s", txt)
	}

	// Verify Stop clears queued attachments and emits dequeued
	e.Locked(func() { e.Agent.Running = true; e.Agent.Cancel = engine.NewFlag() })
	_ = SendWithAttachments(e, "Discard me", nil, []engine.AttachmentImage{{Name: "x.png", MediaType: "image/png", Data: imgA}}, 1, "split", 1, "layout")
	Stop(e)
	e.Locked(func() {
		if len(e.Agent.Queue) != 0 {
			t.Fatalf("Stop should clear queue, got %d", len(e.Agent.Queue))
		}
		e.Agent.Running = false
	})
	if lastOf(agentEvents(e), "dequeued") == nil {
		t.Fatal("expected dequeued event after Stop")
	}
}

func TestContextCompactionStripsOlderUserImagesAndPreservesLatestUntilPass4(t *testing.T) {
	imgOld := strings.Repeat("A", 1200)
	imgLatest := strings.Repeat("B", 1200)

	u1Prompt := "Old brief"
	u1 := engine.Object(
		"role", "user",
		"content", jsonv.NewArray(
			engine.Object("type", "text", "text", u1Prompt+editorStateNote+"old state"),
			engine.Object("type", "image", "source", engine.Object("type", "base64", "media_type", "image/png", "data", imgOld)),
			engine.Object("type", "image", "source", engine.Object("type", "base64", "media_type", "image/png", "data", imgOld)),
		),
		"__prompt_len", jsonv.Int(int64(len(u1Prompt))),
	)
	a1 := engine.Object("role", "assistant", "content", jsonv.NewArray(
		engine.Object("type", "text", "text", "Done with old brief."),
	))
	u2Prompt := "Latest brief"
	u2 := engine.Object(
		"role", "user",
		"content", jsonv.NewArray(
			engine.Object("type", "text", "text", u2Prompt+editorStateNote+"fresh state"),
			engine.Object("type", "image", "source", engine.Object("type", "base64", "media_type", "image/png", "data", imgLatest)),
		),
		"__prompt_len", jsonv.Int(int64(len(u2Prompt))),
	)

	// Pass 1 only: tight enough that 3 images (3750 tok) + base (2000) > maxInput,
	// but after stripping 2 old images (leaving 1 latest image = 1250 tok + base 2000 = ~3350 tok) fits!
	msgs := []jsonv.Value{u1, a1, u2}
	trimmed, compacted, dropped := EnforceContextWindow(msgs, 2000, 6000)
	if !compacted || dropped != 0 {
		t.Fatalf("expected Pass 1 image strip without dropping turns, got compacted=%v dropped=%d", compacted, dropped)
	}
	u1Blocks := jsonv.Items(jsonv.Get(trimmed[0], "content"))
	if !jsonv.StrIs(jsonv.Get(u1Blocks[0], "type"), "text") {
		t.Fatalf("u1 content[0] must stay text")
	}
	for _, b := range u1Blocks {
		if jsonv.StrIs(jsonv.Get(b, "type"), "image") {
			t.Fatalf("u1 older user images should be stripped in Pass 1")
		}
	}
	u2Blocks := jsonv.Items(jsonv.Get(trimmed[2], "content"))
	hasLatestImg := false
	for _, b := range u2Blocks {
		if jsonv.StrIs(jsonv.Get(b, "type"), "image") {
			hasLatestImg = true
		}
	}
	if !hasLatestImg {
		t.Fatalf("Pass 1 must keep latest human turn's images intact")
	}

	// Pass 4: single turn with 4 images (~5000 tokens) + 1000 overhead against 4096 context limit must strip in Pass 4
	manyImgs := []jsonv.Value{engine.Object("type", "text", "text", u2Prompt+editorStateNote+"fresh state")}
	for range 4 {
		manyImgs = append(manyImgs, engine.Object("type", "image", "source", engine.Object("type", "base64", "media_type", "image/png", "data", imgLatest)))
	}
	single := []jsonv.Value{engine.Object("role", "user", "content", jsonv.NewArray(manyImgs...), "__prompt_len", jsonv.Int(int64(len(u2Prompt))))}
	trimmed4, compacted4, _ := EnforceContextWindow(single, 1000, 4096)
	if !compacted4 {
		t.Fatal("expected Pass 4 compaction on oversized single-turn images")
	}
	sBlocks := jsonv.Items(jsonv.Get(trimmed4[0], "content"))
	if !jsonv.StrIs(jsonv.Get(sBlocks[0], "type"), "text") {
		t.Fatal("index-0 text must remain after Pass 4")
	}
	for _, b := range sBlocks {
		if jsonv.StrIs(jsonv.Get(b, "type"), "image") {
			t.Fatal("Pass 4 should have stripped images to fit tight window")
		}
	}
}

func TestNonVisionModelTranslationNeverDropsImageOnlyUserMessage(t *testing.T) {
	body := jsonv.MustParse(`{
		"model": "gpt-text",
		"max_tokens": 512,
		"messages": [
			{"role": "user", "content": [
				{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBOR"}}
			]}
		]
	}`)

	chatReq := model.ChatTranslate(body, false, false)
	chatMsgs := jsonv.Items(jsonv.Get(chatReq, "messages"))
	if len(chatMsgs) != 1 {
		t.Fatalf("ChatTranslate(vision=false) dropped image-only user message: %s", jsonv.Marshal(chatReq))
	}
	chatParts := jsonv.Items(jsonv.Get(chatMsgs[0], "content"))
	if len(chatParts) != 1 || !jsonv.StrIs(jsonv.Get(chatParts[0], "type"), "text") {
		t.Fatalf("expected fallback text part in ChatTranslate: %s", jsonv.Marshal(chatMsgs[0]))
	}

	respReq := model.ResponsesTranslate(body, false, false)
	respIn := jsonv.Items(jsonv.Get(respReq, "input"))
	if len(respIn) != 1 {
		t.Fatalf("ResponsesTranslate(vision=false) dropped image-only user message: %s", jsonv.Marshal(respReq))
	}
	respParts := jsonv.Items(jsonv.Get(respIn[0], "content"))
	if len(respParts) != 1 || !jsonv.StrIs(jsonv.Get(respParts[0], "type"), "input_text") {
		t.Fatalf("expected fallback input_text part in ResponsesTranslate: %s", jsonv.Marshal(respIn[0]))
	}
}

func TestAgentSendRejectsOverTenImages(t *testing.T) {
	e := newEngine(t)
	img := tinyPNGBase64(t, 1, 2, 3)
	arr := jsonv.NewArray()
	for range MaxAttachedImages + 1 {
		arr.Items = append(arr.Items, engine.Object("name", "x.png", "mediaType", "image/png", "data", img))
	}
	if _, err := e.Dispatch(engine.Object("cmd", "agent_send", "text", "too many", "images", arr)); err == nil {
		t.Fatal("expected error when attaching >10 images")
	}
}

func TestDeletedOnlyPinnedNodeAbortsEmptyTurnWithoutCallingModel(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		return textReply("Should never be called", "end_turn")
	})
	e := apiEngine(t, url)

	if _, err := e.Dispatch(engine.Object(
		"cmd", "agent_send",
		"text", "",
		"contexts", jsonv.NewArray(engine.Object("kind", "node", "id", "ghost_node_404")),
	)); err != nil {
		t.Fatalf("agent_send: %v", err)
	}
	waitIdle(t, e)

	if len(api.requests()) != 0 {
		t.Fatalf("expected 0 API requests when only deleted nodes were attached, got %d", len(api.requests()))
	}
	var allEvs []jsonv.Value
	e.Locked(func() {
		if e.Agent.Running {
			t.Fatal("Agent.Running should be released after empty deleted-node abort")
		}
		allEvs = e.TakeEvents()
	})
	sawToast := false
	for _, ev := range allEvs {
		if jsonv.StrIs(jsonv.Get(ev, "type"), "toast") && strings.Contains(jsonv.StrOr(jsonv.Get(ev, "text")), "ghost_node_404") {
			sawToast = true
		}
		if jsonv.StrIs(jsonv.Get(ev, "type"), "agent") && jsonv.StrIs(jsonv.Get(ev, "kind"), "user") {
			t.Fatalf("unexpected user event emitted for aborted empty turn: %s", jsonv.Marshal(ev))
		}
	}
	if !sawToast {
		t.Fatal("expected toast naming deleted node ghost_node_404")
	}
}

func TestQueuedAttachmentsRejectOverTenImagesTotal(t *testing.T) {
	e := newEngine(t)
	e.Locked(func() { e.Agent.Running = true; e.Agent.Cancel = engine.NewFlag() })
	img := engine.AttachmentImage{Name: "a.png", MediaType: "image/png", Data: tinyPNGBase64(t, 5, 10, 15)}

	seven := make([]engine.AttachmentImage, 7)
	for i := range seven {
		seven[i] = img
	}
	if err := SendWithAttachments(e, "batch 1", nil, seven, 1, "split", 1, "layout"); err != nil {
		t.Fatalf("first queued batch of 7 failed: %v", err)
	}
	four := make([]engine.AttachmentImage, 4)
	for i := range four {
		four[i] = img
	}
	err := SendWithAttachments(e, "batch 2", nil, four, 1, "split", 1, "layout")
	if err == nil || !strings.Contains(err.Error(), "At most 10 images") {
		t.Fatalf("expected clear error rejecting queued total 11 > 10, got %v", err)
	}
	e.Locked(func() {
		if len(e.Agent.Queue) != 1 || len(e.Agent.Queue[0].Images) != 7 {
			t.Fatalf("existing queued images should remain untouched: %+v", e.Agent.Queue)
		}
		e.Agent.Running = false
		e.Agent.Queue = nil
	})
}

func TestCaptureCanvasContextsTallNarrowViewportComponentDescendantAndBudgetAlignment(t *testing.T) {
	e := newEngine(t)
	out := e.CallTool("execute", engine.Object("input", `
		comp = Insert(document, {type:"frame", name:"PrimaryBtn", reusable:true, x:0, y:0, width:160, height:48, fill:"#0F766E"});
		lbl = Insert(comp, {type:"text", name:"BtnLabel", content:"Save", fontSize:16, fill:"#FFFFFF"});
		inst = Insert(document, {type:"ref", ref:comp, name:"SaveInstance", x:200, y:0});
		Update(inst + "/" + lbl, {content:"Publish Now"});
	`)).Finish(e)
	if out.Error {
		t.Fatalf("execute: %s", out.Text)
	}
	instID := nodeID(e, "SaveInstance")
	lblID := nodeID(e, "BtnLabel")
	descID := instID + "/" + lblID

	// 1. Tall narrow viewport (100x1600): pw should scale proportionally (~80x1280, not clamped to 64x... or cropped)
	vp := [4]float64{0, 0, 100, 1600}
	capVP := e.CaptureCanvasContexts([]engine.CanvasContextRef{{Kind: "viewport", Viewport: &vp}}, true)
	if len(capVP.Snapshots) != 1 {
		t.Fatalf("expected 1 viewport snapshot, got %d", len(capVP.Snapshots))
	}
	snap := capVP.Snapshots[0]
	if snap.Height != 1280 || snap.Width != 80 {
		t.Fatalf("expected proportional tall narrow viewport snapshot 80x1280, got %dx%d", snap.Width, snap.Height)
	}
	// Extra-narrow viewport (20x1600 -> 16x1280, verifying min clamp is 1 not 64)
	vpThin := [4]float64{0, 0, 20, 1600}
	capThin := e.CaptureCanvasContexts([]engine.CanvasContextRef{{Kind: "viewport", Viewport: &vpThin}}, true)
	if len(capThin.Snapshots) != 1 || capThin.Snapshots[0].Width != 16 || capThin.Snapshots[0].Height != 1280 {
		t.Fatalf("expected 16x1280 thin viewport snapshot, got %+v", capThin.Snapshots)
	}

	// 2. Ref instance includes _inherited properties, and slash-path descendant emits compact JSON with overridden content
	capComp := e.CaptureCanvasContexts([]engine.CanvasContextRef{
		{Kind: "node", ID: instID},
		{Kind: "node", ID: descID},
	}, false)
	if !strings.Contains(capComp.Text, `"_inherited"`) || !strings.Contains(capComp.Text, "#0F766E") {
		t.Fatalf("expected ref instance compact JSON to include inherited properties: %s", capComp.Text)
	}
	if !strings.Contains(capComp.Text, descID) || !strings.Contains(capComp.Text, "Publish Now") {
		t.Fatalf("expected slash-path descendant compact JSON with overridden text: %s", capComp.Text)
	}

	// 3. Budget alignment: create 6 chunky nodes exceeding maxContextTotalTextBytes (14000) and verify ALL 6 headers & image #1..#6 stay present!
	var refs []engine.CanvasContextRef
	var bigIDs []string
	for i := 1; i <= 6; i++ {
		name := "BigCard" + string(rune('0'+i))
		js := `f = Insert(document, {type:"frame", name:"` + name + `", x:` + string(rune('0'+i)) + `00, y:300, width:240, height:120, fill:"#111111"});`
		for j := 1; j <= 10; j++ {
			js += `Insert(f, {type:"text", name:"Row", content:"` + strings.Repeat("spec ", 65) + `", fontSize:12, fill:"#ffffff"});`
		}
		if res := e.CallTool("execute", engine.Object("input", js)).Finish(e); res.Error {
			t.Fatalf("insert %s: %s", name, res.Text)
		}
		bid := nodeID(e, name)
		bigIDs = append(bigIDs, bid)
		refs = append(refs, engine.CanvasContextRef{Kind: "node", ID: bid})
	}
	capMany := e.CaptureCanvasContexts(refs, true)
	if len(capMany.Snapshots) != 6 {
		t.Fatalf("expected 6 snapshots, got %d", len(capMany.Snapshots))
	}
	for i, bid := range bigIDs {
		wantHeader := "### Node `" + bid + "`"
		wantImg := "attached as image #" + string(rune('1'+i))
		if !strings.Contains(capMany.Text, wantHeader) || !strings.Contains(capMany.Text, wantImg) {
			t.Fatalf("missing header %q or snapshot marker %q in budget-trimmed text", wantHeader, wantImg)
		}
	}
}

func TestVisionFalseProviderHygieneAnthropicAndOpenAI(t *testing.T) {
	for _, format := range []string{"chat", "anthropic"} {
		t.Run(format, func(t *testing.T) {
			api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
				if format == "anthropic" {
					return textReply("No images received.", "end_turn")
				}
				w.Header().Set("content-type", "text/event-stream")
				return "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"No images received.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			})
			e := newEngine(t)
			img := tinyPNGBase64(t, 200, 100, 50)
			e.Locked(func() {
				e.Settings.Set("agent_backend", "provider")
				e.Settings.Set("agent_provider", "nv_"+format)
				e.Settings.Set("provider_model", "blind-model")
				e.Settings.Set("providers", jsonv.NewArray(engine.Object(
					"id", "nv_"+format,
					"name", "Blind-"+format,
					"format", format,
					"base_url", url+"/v1",
					"api_key", "k",
					"models", engine.Strings([]string{"blind-model"}),
					"vision", false,
				)))
				// Seed prior-turn history containing both a user image and a tool_result screenshot image
				e.Agent.Messages = []jsonv.Value{
					engine.Object("role", "user", "content", jsonv.NewArray(
						engine.Object("type", "text", "text", "Old turn"),
						engine.Object("type", "image", "source", engine.Object("type", "base64", "media_type", "image/png", "data", img)),
					)),
					engine.Object("role", "assistant", "content", jsonv.NewArray(
						engine.Object("type", "tool_use", "id", "tu_1", "name", "execute", "input", engine.Object("input", "TakeScreenshot()")),
					)),
					engine.Object("role", "user", "content", jsonv.NewArray(
						engine.Object("type", "tool_result", "tool_use_id", "tu_1", "content", jsonv.NewArray(
							engine.Object("type", "text", "text", "ok"),
							engine.Object("type", "image", "source", engine.Object("type", "base64", "media_type", "image/png", "data", img)),
						)),
					)),
					engine.Object("role", "assistant", "content", jsonv.NewArray(
						engine.Object("type", "text", "text", "Done."),
					)),
				}
			})

			if err := SendWithAttachments(e, "New turn with image", nil, []engine.AttachmentImage{
				{Name: "ref.png", MediaType: "image/png", Data: img},
			}, 1, "split", 1, "layout"); err != nil {
				t.Fatalf("SendWithAttachments: %v", err)
			}
			waitIdle(t, e)

			reqs := api.requests()
			if len(reqs) != 1 {
				t.Fatalf("expected 1 request, got %d", len(reqs))
			}
			wireRaw := string(jsonv.Marshal(reqs[0].body))
			if strings.Contains(wireRaw, img) || strings.Contains(wireRaw, `"type":"image"`) || strings.Contains(wireRaw, `"image_url"`) {
				t.Fatalf("vision:false %s request leaked image payload on wire: %s", format, wireRaw)
			}
			// Newly stored user message in e.Agent.Messages must NOT retain raw base64 image blocks either
			e.Locked(func() {
				lastUser := e.Agent.Messages[len(e.Agent.Messages)-2]
				storedRaw := string(jsonv.Marshal(lastUser))
				if strings.Contains(storedRaw, img) || strings.Contains(storedRaw, `"type":"image"`) {
					t.Fatalf("vision:false %s stored raw image block in e.Agent.Messages: %s", format, storedRaw)
				}
				if !strings.Contains(storedRaw, "cannot see images") {
					t.Fatalf("expected blind-image text note in stored message: %s", storedRaw)
				}
			})
		})
	}
}
