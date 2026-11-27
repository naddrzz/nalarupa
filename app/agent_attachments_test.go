package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func makeTestImageFile(t *testing.T, dir, name, format string, w, h int, col color.RGBA) (string, []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, col)
		}
	}
	var buf bytes.Buffer
	switch format {
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
	case "jpeg":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
			t.Fatal(err)
		}
	case "gif":
		if err := gif.Encode(&buf, img, nil); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown format %s", format)
	}
	raw := buf.Bytes()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, raw
}

func TestCanvasAttachEntrypointsPillContextMenuShortcutAndPickMode(t *testing.T) {
	app, tt := newTestApp(t, ui.TesterOptions{})
	app.StartTemplate("empty", true)
	if _, err := app.Bridge.ExecuteJS(`
		h = Insert(document, {type:"frame", name:"Hero", x:100, y:120, width:320, height:200, fill:"#224466"});
		Insert(h, {type:"text", name:"Title", content:"Hello", fontSize:24, fill:"#FFFFFF"});
		f = Insert(document, {type:"frame", name:"Footer", x:520, y:120, width:280, height:140, fill:"#442266"});
	`); err != nil {
		t.Fatalf("ExecuteJS: %v", err)
	}
	app.RefreshState()
	tt.Frame()
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1}
	tt.Frame()

	frames := app.Bridge.FramesDirect()
	var heroBox, footerBox Box
	for _, f := range frames {
		switch f.Name {
		case "Hero":
			heroBox = f
		case "Footer":
			footerBox = f
		}
	}
	if heroBox.ID == "" || footerBox.ID == "" {
		t.Fatalf("missing test frames: %+v", frames)
	}

	// 1. Floating "+ Chat" pill near selected node
	app.Select([]string{heroBox.ID})
	tt.Frame()
	if app.RightTab != "design" {
		t.Fatalf("expected initial selection with empty chat to open design tab, got %q", app.RightTab)
	}
	assertAllInteractiveControlsNamed(t, tt, "Canvas selection with + Chat pill")

	pillInfo, ok := tt.Access("Attach selection to Agent Chat")
	if !ok {
		t.Fatal("expected floating '+ Chat' pill to be present on canvas selection")
	}
	// Verify pill stays clear of frame-label (heroBox.Y-18..heroBox.Y-2) and size-label (heroBox.Y+heroBox.H+6..+24)
	canvasRect, hasCanvas := tt.Find("Design canvas")
	if !hasCanvas {
		t.Fatal("missing Design canvas element")
	}
	pillRelY := pillInfo.Bounds.Y - canvasRect.Y
	pillRelBottom := pillRelY + pillInfo.Bounds.H
	frameLblTop := float32(heroBox.Y) - 18
	sizeLblTop := float32(heroBox.Y+heroBox.H) + 6
	if pillRelBottom > frameLblTop && pillRelY < sizeLblTop+18 {
		t.Fatalf("+ Chat pill overlaps frame-label/selection/size-label: pillRel=[%.1f..%.1f] frameLblTop=%.1f sizeLblTop=%.1f",
			pillRelY, pillRelBottom, frameLblTop, sizeLblTop)
	}

	if err := tt.Click("Attach selection to Agent Chat"); err != nil {
		t.Fatalf("click + Chat pill: %v", err)
	}
	if app.RightTab != "agent" {
		t.Fatalf("expected + Chat to open agent tab, got %q", app.RightTab)
	}
	if !tt.Focused("Message the design agent") {
		t.Fatal("expected composer input to be focused after + Chat")
	}
	if len(app.AgentUI.PendingContexts) != 1 || app.AgentUI.PendingContexts[0].ID != heroBox.ID {
		t.Fatalf("expected Hero attached, got %+v", app.AgentUI.PendingContexts)
	}

	// Guard check: changing selection while pending contexts exist must NOT revert RightTab to "design"!
	app.Select([]string{footerBox.ID})
	tt.Frame()
	if app.RightTab != "agent" {
		t.Fatalf("onSelectionChanged reverted RightTab to %q despite pending canvas attachment", app.RightTab)
	}

	// 2. Shortcut Cmd/Ctrl+L attaches current selection (Footer) and deduplicates on repeat
	tt.Key(ui.Cmd, ui.KeyL)
	if len(app.AgentUI.PendingContexts) != 2 || app.AgentUI.PendingContexts[1].ID != footerBox.ID {
		t.Fatalf("expected Footer attached via Cmd+L, got %+v", app.AgentUI.PendingContexts)
	}
	tt.Key(ui.Cmd, ui.KeyL)
	if len(app.AgentUI.PendingContexts) != 2 {
		t.Fatalf("expected Cmd+L deduplication, got %d contexts", len(app.AgentUI.PendingContexts))
	}

	// Cmd/Ctrl+L with empty selection attaches viewport
	app.Select([]string{})
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyL)
	if !slices.ContainsFunc(app.AgentUI.PendingContexts, func(c PendingCanvasContext) bool { return c.Kind == "viewport" }) {
		t.Fatalf("expected Cmd+L on empty selection to attach viewport, got %+v", app.AgentUI.PendingContexts)
	}

	// Clear contexts and test removable chips
	for len(app.AgentUI.PendingContexts) > 0 {
		lbl := formatPendingContextLabel(app.AgentUI.PendingContexts[0])
		if err := tt.Click("Remove canvas context " + lbl); err != nil {
			t.Fatalf("remove chip %q: %v", lbl, err)
		}
	}

	// 3. Composer "+ Canvas" popover -> Pick from canvas + Esc cancel + click pick
	if err := tt.Click("Attach canvas context"); err != nil {
		t.Fatalf("open + Canvas popover: %v", err)
	}
	assertAllInteractiveControlsNamed(t, tt, "+ Canvas popover open")
	if err := tt.Click("Pick from canvas"); err != nil {
		t.Fatalf("click Pick from canvas: %v", err)
	}
	if !app.AgentUI.PickingCanvasCtx {
		t.Fatal("expected PickingCanvasCtx=true")
	}
	if gotCur := app.computeCanvasCursor(); gotCur != ui.CursorCrosshair {
		t.Fatalf("expected CursorCrosshair in pick mode, got %v", gotCur)
	}
	assertAllInteractiveControlsNamed(t, tt, "Pick from canvas active")

	// Esc cancels pick mode
	tt.Key(0, ui.KeyEscape)
	if app.AgentUI.PickingCanvasCtx {
		t.Fatal("expected Esc to cancel PickingCanvasCtx")
	}

	// Re-enter pick mode and click Hero on canvas
	_ = tt.Click("Attach canvas context")
	_ = tt.Click("Pick from canvas")
	heroClickX := canvasRect.X + float32(heroBox.X+heroBox.W/2)
	heroClickY := canvasRect.Y + float32(heroBox.Y+heroBox.H/2)
	tt.ClickAt(heroClickX, heroClickY)
	if app.AgentUI.PickingCanvasCtx {
		t.Fatal("expected click pick to exit pick mode")
	}
	if len(app.AgentUI.PendingContexts) != 1 || app.AgentUI.PendingContexts[0].ID != heroBox.ID {
		t.Fatalf("expected click pick to attach Hero, got %+v", app.AgentUI.PendingContexts)
	}

	// 4. Right-click context menu on canvas: node -> "Attach to Agent Chat", empty -> "Attach viewport screen"
	app.AgentUI.PendingContexts = nil
	tt.Frame()
	footerClickX := canvasRect.X + float32(footerBox.X+footerBox.W/2)
	footerClickY := canvasRect.Y + float32(footerBox.Y+footerBox.H/2)
	tt.RightClickAt(footerClickX, footerClickY)
	menuItems := tt.Menu()
	if !slices.Contains(menuItems, "Attach to Agent Chat") {
		t.Fatalf("expected 'Attach to Agent Chat' on node right-click, got menu=%v", menuItems)
	}
	if err := tt.ChooseMenuItem("Attach to Agent Chat"); err != nil {
		t.Fatalf("choose Attach to Agent Chat: %v", err)
	}
	if len(app.AgentUI.PendingContexts) != 1 || app.AgentUI.PendingContexts[0].ID != footerBox.ID {
		t.Fatalf("expected Footer attached from right-click menu, got %+v", app.AgentUI.PendingContexts)
	}

	// Right-click empty canvas area
	emptyX := canvasRect.X + 20
	emptyY := canvasRect.Y + 20
	tt.RightClickAt(emptyX, emptyY)
	menuItems = tt.Menu()
	if !slices.Contains(menuItems, "Attach viewport screen") {
		t.Fatalf("expected 'Attach viewport screen' on empty canvas right-click, got menu=%v", menuItems)
	}
	if err := tt.ChooseMenuItem("Attach viewport screen"); err != nil {
		t.Fatalf("choose Attach viewport screen: %v", err)
	}
	if !slices.ContainsFunc(app.AgentUI.PendingContexts, func(c PendingCanvasContext) bool { return c.Kind == "viewport" }) {
		t.Fatalf("expected viewport context attached from empty canvas context menu, got %+v", app.AgentUI.PendingContexts)
	}
}

func TestMultiImageAttachPickerPasteDropDownscaleCapAndRemove(t *testing.T) {
	app, tt := newTestApp(t, ui.TesterOptions{})
	app.StartTemplate("empty", true)
	app.RightTab = "agent"
	tt.Frame()

	dir := t.TempDir()
	pngPath, pngRaw := makeTestImageFile(t, dir, "one.png", "png", 120, 80, color.RGBA{220, 40, 40, 255})
	jpgPath, _ := makeTestImageFile(t, dir, "two.jpg", "jpeg", 160, 120, color.RGBA{40, 180, 40, 255})
	gifPath, _ := makeTestImageFile(t, dir, "three.gif", "gif", 64, 64, color.RGBA{40, 40, 220, 255})
	bigPath, _ := makeTestImageFile(t, dir, "huge.png", "png", 2400, 1200, color.RGBA{200, 150, 20, 255})
	badPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(badPath, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Multi-select file picker via "+ Image" button (with 1 invalid file + 1 oversized image -> auto-downscaled)
	app.AgentUI.pickImageFilesFn = func(onFiles func([]string)) {
		onFiles([]string{pngPath, badPath, bigPath})
	}
	if err := tt.Click("Attach images"); err != nil {
		t.Fatalf("click Attach images: %v", err)
	}
	if len(app.AgentUI.PendingImages) != 2 {
		t.Fatalf("expected 2 valid images kept (1 invalid skipped), got %d", len(app.AgentUI.PendingImages))
	}
	if app.Toast == nil || !strings.Contains(app.Toast.Text, "notes.txt") {
		t.Fatalf("expected toast noting invalid notes.txt, got %+v", app.Toast)
	}
	// Verify oversized 2400x1200 image was downscaled to 1568x784
	bigImg := app.AgentUI.PendingImages[1]
	if bigImg.Width != maxImageLongSide || bigImg.Height != 784 {
		t.Fatalf("expected huge.png downscaled to 1568x784, got %dx%d", bigImg.Width, bigImg.Height)
	}

	// 2. Clipboard paste: transfer.PNG into agent-input
	if err := tt.Click("Message the design agent"); err != nil {
		t.Fatalf("focus agent-input: %v", err)
	}
	tt.SetClipboardData(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, pngRaw))))
	tt.Command("paste")
	if len(app.AgentUI.PendingImages) != 3 {
		t.Fatalf("expected PNG paste to append 3rd image, got %d", len(app.AgentUI.PendingImages))
	}

	// 3. Clipboard paste: transfer.FileList into agent-input
	fileData, err := transfer.FileData(jpgPath, gifPath)
	if err != nil {
		t.Fatalf("transfer.FileData: %v", err)
	}
	tt.SetClipboardData(fileData)
	tt.Command("paste")
	if len(app.AgentUI.PendingImages) != 5 {
		t.Fatalf("expected FileList paste to append 2 images (total 5), got %d", len(app.AgentUI.PendingImages))
	}

	// 4. Plain text paste still inserts text normally
	tt.SetClipboard("Keep clean layout")
	tt.Command("paste")
	if !strings.Contains(app.AgentUI.Draft, "Keep clean layout") {
		t.Fatalf("expected plain text paste into Draft, got %q", app.AgentUI.Draft)
	}

	// 5. Drag-and-drop multiple files onto the Agent panel (testing max 10 cap)
	panelRect, ok := tt.Find("Message the design agent")
	if !ok {
		t.Fatal("missing agent-input bounds for drop target")
	}
	var dropBatch []string
	for i := range 7 {
		p, _ := makeTestImageFile(t, dir, fmt.Sprintf("drop-%d.png", i), "png", 32, 32, color.RGBA{uint8(i * 30), 100, 200, 255})
		dropBatch = append(dropBatch, p)
	}
	if !tt.DropFiles(panelRect.X+20, panelRect.Y+10, dropBatch...) {
		t.Fatal("expected Agent panel to accept DroppedFiles")
	}
	if len(app.AgentUI.PendingImages) != maxComposerImages {
		t.Fatalf("expected cap at %d images, got %d", maxComposerImages, len(app.AgentUI.PendingImages))
	}
	if app.Toast == nil || !strings.Contains(app.Toast.Text, "10-image limit") {
		t.Fatalf("expected toast mentioning 10-image limit, got %+v", app.Toast)
	}

	// 6. Remove one image thumbnail via UI button
	rmName := app.AgentUI.PendingImages[0].Name
	if err := tt.Click("Remove image " + rmName); err != nil {
		t.Fatalf("click remove image %q: %v", rmName, err)
	}
	if len(app.AgentUI.PendingImages) != maxComposerImages-1 {
		t.Fatalf("expected %d images after removal, got %d", maxComposerImages-1, len(app.AgentUI.PendingImages))
	}
	assertAllInteractiveControlsNamed(t, tt, "Composer with image thumbnails tray")
}

func TestAttachmentsEndToEndSendUserBubbleHistoryReloadAndNonVisionWarning(t *testing.T) {
	var mu sync.Mutex
	var seenBodies []jsonv.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if v, err := jsonv.Parse(raw); err == nil {
			mu.Lock()
			seenBodies = append(seenBodies, v)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":5}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Applied your reference!\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":10}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	app, tt := newTestApp(t, ui.TesterOptions{})
	app.StartTemplate("empty", true)
	if _, err := app.Bridge.ExecuteJS(`
		Insert(document, {type:"frame", name:"HeroCard", x:0, y:0, width:360, height:200, fill:"#1E293B"});
		Insert(document, {type:"frame", name:"OtherCard", x:440, y:0, width:360, height:200, fill:"#334155"});
	`); err != nil {
		t.Fatal(err)
	}
	app.RefreshState()
	frames := app.Bridge.FramesDirect()
	heroID, otherID := frames[0].ID, frames[1].ID

	// 1. Non-vision BYOK provider shows clear non-blocking warning when attachments exist
	app.RightTab = "agent"
	app.AgentUI.Backend = "provider"
	app.AgentUI.ActiveProvID = "nv"
	app.AgentUI.ProviderModel = "text-only"
	app.AgentUI.Providers = []ProviderConfig{{
		ID:      "nv",
		Name:    "TextOnly",
		Format:  "chat",
		BaseURL: srv.URL + "/v1",
		Models:  []string{"text-only"},
		Vision:  false,
	}}
	dir := t.TempDir()
	img1Path, _ := makeTestImageFile(t, dir, "keep.png", "png", 80, 60, color.RGBA{255, 80, 80, 255})
	img2Path, _ := makeTestImageFile(t, dir, "dropme.png", "png", 80, 60, color.RGBA{80, 80, 255, 255})
	app.AttachNodesToAgentChat([]string{heroID})
	app.AttachImageFiles([]string{img1Path, img2Path})
	tt.Frame()
	if !tt.HasText("Active provider has Vision off — attached images will not be seen by the model.") {
		t.Fatalf("expected non-vision warning banner, texts=%v", tt.Texts())
	}
	assertAllInteractiveControlsNamed(t, tt, "Composer with non-vision warning")

	// 2. Switch to vision Anthropic API, remove 2nd image, change canvas selection to OtherCard, and send
	app.setSetting("agent_backend", "api")
	app.setSetting("anthropic_api_key", "test-key")
	app.setSetting("anthropic_base_url", srv.URL)
	tt.Frame()
	if err := tt.Click("Remove image dropme.png"); err != nil {
		t.Fatalf("remove dropme.png: %v", err)
	}
	if len(app.AgentUI.PendingImages) != 1 {
		t.Fatalf("expected 1 remaining image, got %d", len(app.AgentUI.PendingImages))
	}
	keepB64 := app.AgentUI.PendingImages[0].Data
	app.Select([]string{otherID})
	app.AgentUI.Draft = "Match the red accent"
	tt.Frame()

	if err := tt.Click("Send"); err != nil {
		t.Fatalf("click Send: %v", err)
	}

	// Wait for agent turn to finish and poll events into Store
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		app.PollNow(float64(time.Now().UnixMilli()))
		if !app.State.AgentRunning && len(app.Store.Items) >= 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	tt.Frame()

	if len(app.AgentUI.PendingContexts) != 0 || len(app.AgentUI.PendingImages) != 0 || app.AgentUI.Draft != "" {
		t.Fatalf("expected draft & pending attachments cleared after send")
	}

	// Verify model received pinned HeroCard + its PNG snapshot + ONLY the remaining 1 user image
	mu.Lock()
	gotBodies := append([]jsonv.Value(nil), seenBodies...)
	mu.Unlock()
	if len(gotBodies) != 1 {
		t.Fatalf("expected 1 API request, got %d", len(gotBodies))
	}
	content := jsonv.Items(jsonv.Get(jsonv.Items(jsonv.Get(gotBodies[0], "messages"))[0], "content"))
	if len(content) != 3 {
		t.Fatalf("expected 3 content blocks (text + HeroCard PNG + 1 remaining user image), got %d", len(content))
	}
	if !strings.Contains(jsonv.StrOr(jsonv.Get(content[0], "text")), heroID) {
		t.Fatalf("expected pinned HeroCard id %s in prompt", heroID)
	}
	if jsonv.StrOr(jsonv.Get(jsonv.Get(content[2], "source"), "data")) != keepB64 {
		t.Fatal("model did not receive the remaining user image")
	}

	// Verify user bubble in live transcript has text, context badge, and 1 image thumbnail
	var userItem *ChatItem
	for i := range app.Store.Items {
		if app.Store.Items[i].Kind == "user" {
			userItem = &app.Store.Items[i]
			break
		}
	}
	if userItem == nil || len(userItem.Contexts) != 1 || userItem.Contexts[0].ID != heroID || len(userItem.Images) != 1 || userItem.Images[0] != keepB64 {
		t.Fatalf("unexpected live user ChatItem: %+v", userItem)
	}
	if !tt.HasText("HeroCard · 360×200") || !tt.HasText("Match the red accent") {
		t.Fatalf("user bubble missing context badge or text: texts=%v", tt.Texts())
	}

	// 3. Reload from History via chat_list + chat_load -> RestoreChat
	app.refreshChatHistory()
	if len(app.AgentUI.Chats) == 0 {
		t.Fatal("expected saved chat in History")
	}
	var loaded struct {
		ID     string           `json:"id"`
		Title  string           `json:"title"`
		Events []map[string]any `json:"events"`
	}
	if err := app.Bridge.Call("chat_load", map[string]any{"id": app.AgentUI.Chats[0].ID}, &loaded); err != nil {
		t.Fatalf("chat_load: %v", err)
	}
	app.Store.RestoreChat(loaded.Events)
	tt.Frame()

	var restoredUser *ChatItem
	for i := range app.Store.Items {
		if app.Store.Items[i].Kind == "user" {
			restoredUser = &app.Store.Items[i]
			break
		}
	}
	if restoredUser == nil || restoredUser.Text != "Match the red accent" ||
		len(restoredUser.Contexts) != 1 || restoredUser.Contexts[0].ID != heroID ||
		len(restoredUser.Images) != 1 || restoredUser.Images[0] != keepB64 {
		t.Fatalf("restored chat user item lost attachments: %+v", restoredUser)
	}
	if !tt.HasText("HeroCard · 360×200") {
		t.Fatalf("restored chat UI missing context chip")
	}

	// 4. Old v1 session without attachment fields restores cleanly
	app.Store.RestoreChat([]map[string]any{
		{"type": "agent", "kind": "user", "text": "Legacy v1 prompt", "at": float64(1000)},
	})
	if len(app.Store.Items) != 1 || app.Store.Items[0].Text != "Legacy v1 prompt" || len(app.Store.Items[0].Contexts) != 0 || len(app.Store.Items[0].Images) != 0 {
		t.Fatalf("legacy v1 session did not restore cleanly: %+v", app.Store.Items)
	}

	// 5. On agent_send error, toast is shown and draft + pending contexts + pending images are preserved
	app.AgentUI.Draft = "Keep this draft"
	app.AttachNodesToAgentChat([]string{heroID})
	app.AgentUI.PendingImages = []PendingImageAttachment{{
		ID:        "bad-img-1",
		Name:      "corrupt.png",
		MediaType: "image/png",
		Data:      "%%%not-valid-base64%%%",
	}}
	app.AgentUI.LastSubmitAt = time.Time{}
	if app.submitAgentPrompt(app.AgentUI.Draft, false) {
		t.Fatal("expected submitAgentPrompt to fail on invalid base64 payload")
	}
	if app.Toast == nil || !app.Toast.Danger {
		t.Fatalf("expected danger toast on send error, got %+v", app.Toast)
	}
	if app.AgentUI.Draft != "Keep this draft" || len(app.AgentUI.PendingContexts) != 1 || len(app.AgentUI.PendingImages) != 1 {
		t.Fatalf("expected draft & attachments preserved on send error: draft=%q ctx=%d img=%d",
			app.AgentUI.Draft, len(app.AgentUI.PendingContexts), len(app.AgentUI.PendingImages))
	}
	app.clearPendingAttachments()
}

func TestDecodeImageAttachmentSupportsWebP(t *testing.T) {
	// Valid 1x1 lossless WebP (RIFF/WEBP VP8L)
	webp1x1 := []byte{
		0x52, 0x49, 0x46, 0x46, 0x24, 0x00, 0x00, 0x00,
		0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x4c,
		0x18, 0x00, 0x00, 0x00, 0x2f, 0x00, 0x00, 0x00,
		0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe,
		0x07, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	item, err := decodeImageAttachmentBytes("swatch.webp", webp1x1)
	if err != nil {
		t.Fatalf("decodeImageAttachmentBytes(webp): %v", err)
	}
	if item.MediaType != "image/webp" || item.Width != 1 || item.Height != 1 || item.ThumbRGBA == nil {
		t.Fatalf("unexpected webp decoded item: mt=%q %dx%d thumb=%v", item.MediaType, item.Width, item.Height, item.ThumbRGBA != nil)
	}
}

func TestDecodeImageAttachmentCompressesOverFourMBPNG(t *testing.T) {
	// Build a high-entropy 1500x1500 PNG whose raw PNG bytes exceed maxEncodedImageBytes (4 MB)
	// while remaining <= maxImageLongSide (1568px).
	const side = 1500
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	var state uint32 = 0x12345678
	for i := 0; i < len(img.Pix); i += 4 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		img.Pix[i] = byte(state)
		img.Pix[i+1] = byte(state >> 8)
		img.Pix[i+2] = byte(state >> 16)
		img.Pix[i+3] = 255
	}
	var rawBuf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&rawBuf, img); err != nil {
		t.Fatal(err)
	}
	if rawBuf.Len() <= maxEncodedImageBytes {
		t.Fatalf("expected synthetic PNG > 4MB, got %d bytes", rawBuf.Len())
	}
	item, err := decodeImageAttachmentBytes("noisy.png", rawBuf.Bytes())
	if err != nil {
		t.Fatalf("decodeImageAttachmentBytes(noisy >4MB png): %v", err)
	}
	decoded, err := DecodeBase64Image(item.Data)
	if err != nil {
		t.Fatalf("DecodeBase64Image: %v", err)
	}
	if len(decoded) > maxEncodedImageBytes {
		t.Fatalf("expected compressed output <= %d bytes, got %d", maxEncodedImageBytes, len(decoded))
	}
	if item.MediaType != "image/jpeg" {
		t.Fatalf("expected JPEG fallback for >4MB noisy PNG, got %q", item.MediaType)
	}
}

func TestPickFromCanvasWorksInHandToolAndResetsStaleContextMenuState(t *testing.T) {
	app, tt := newTestApp(t, ui.TesterOptions{})
	app.StartTemplate("empty", true)
	if _, err := app.Bridge.ExecuteJS(`
		Insert(document, {type:"frame", name:"CardOne", x:100, y:100, width:240, height:160, fill:"#334155"});
		Insert(document, {type:"frame", name:"CardTwo", x:420, y:100, width:240, height:160, fill:"#475569"});
	`); err != nil {
		t.Fatal(err)
	}
	app.RefreshState()
	tt.Frame()
	app.Cam = Camera{X: 0, Y: 0, Zoom: 1}
	tt.Frame()

	frames := app.Bridge.FramesDirect()
	cardOne, cardTwo := frames[0], frames[1]
	canvasRect, ok := tt.Find("Design canvas")
	if !ok {
		t.Fatal("missing Design canvas")
	}

	// 1. Hand tool active + Pick from canvas: crosshair cursor and click attaches CardOne instead of panning
	app.SetTool("hand", false)
	app.AgentUI.PickingCanvasCtx = true
	tt.Frame()
	if got := app.computeCanvasCursor(); got != ui.CursorCrosshair {
		t.Fatalf("expected CursorCrosshair in pick mode even with hand tool active, got %v", got)
	}
	if mode := app.nativeGrabCursorMode(); mode != 0 {
		t.Fatalf("expected nativeGrabCursorMode=0 in pick mode, got %d", mode)
	}
	tt.ClickAt(canvasRect.X+float32(cardOne.X+cardOne.W/2), canvasRect.Y+float32(cardOne.Y+cardOne.H/2))
	if app.AgentUI.PickingCanvasCtx {
		t.Fatal("expected pick click under hand tool to complete pick mode")
	}
	if len(app.AgentUI.PendingContexts) != 1 || app.AgentUI.PendingContexts[0].ID != cardOne.ID {
		t.Fatalf("expected CardOne attached under hand tool, got %+v", app.AgentUI.PendingContexts)
	}
	if app.Canvas.Pan != nil {
		t.Fatal("pick click under hand tool must not start a pan gesture")
	}

	// 2. Stale context-menu state clears after right-click -> selection change / left-click / menu choice
	tt.RightClickAt(canvasRect.X+float32(cardTwo.X+cardTwo.W/2), canvasRect.Y+float32(cardTwo.Y+cardTwo.H/2))
	if !app.Canvas.contextHasTarget || app.Canvas.contextTargetID != cardTwo.ID {
		t.Fatalf("expected contextTargetID=%s after right-click, got %q (has=%v)", cardTwo.ID, app.Canvas.contextTargetID, app.Canvas.contextHasTarget)
	}
	tt.CloseMenu()
	app.Select([]string{cardOne.ID})
	if app.Canvas.contextHasTarget || app.Canvas.contextTargetID != "" || app.Canvas.contextClickedEmpty {
		t.Fatalf("expected onSelectionChanged to clear stale context menu state, got target=%q has=%v empty=%v",
			app.Canvas.contextTargetID, app.Canvas.contextHasTarget, app.Canvas.contextClickedEmpty)
	}

	// Right-click empty canvas, then left-click clears contextClickedEmpty
	tt.RightClickAt(canvasRect.X+15, canvasRect.Y+15)
	if !app.Canvas.contextClickedEmpty {
		t.Fatal("expected contextClickedEmpty=true after empty right-click")
	}
	tt.CloseMenu()
	app.SetTool("move", false)
	tt.ClickAt(canvasRect.X+15, canvasRect.Y+15)
	if app.Canvas.contextClickedEmpty || app.Canvas.contextHasTarget {
		t.Fatalf("expected left-click to clear stale context menu state")
	}
}

func TestAgentStoreQueueCoalescingWithAttachments(t *testing.T) {
	store := NewAgentStore()
	store.ApplyEvents([]map[string]any{
		{
			"type":     "agent",
			"kind":     "queued",
			"text":     "Part 1",
			"contexts": []any{map[string]any{"kind": "node", "id": "n1", "name": "Header", "nodeType": "frame"}},
			"images":   []any{"imgA"},
		},
		{
			"type":     "agent",
			"kind":     "queued",
			"text":     "Part 2",
			"contexts": []any{map[string]any{"kind": "node", "id": "n2", "name": "Body", "nodeType": "frame"}},
			"images":   []any{"imgB"},
		},
	}, 100)
	if len(store.Items) != 2 || store.Items[0].Kind != "queued" || len(store.Items[0].Contexts) != 1 || len(store.Items[0].Images) != 1 {
		t.Fatalf("expected 2 queued items with attachments, got %+v", store.Items)
	}
	store.ApplyEvents([]map[string]any{
		{
			"type":      "agent",
			"kind":      "user",
			"text":      "Part 1\n\nPart 2",
			"fromQueue": true,
			"contexts": []any{
				map[string]any{"kind": "node", "id": "n1", "name": "Header", "nodeType": "frame"},
				map[string]any{"kind": "node", "id": "n2", "name": "Body", "nodeType": "frame"},
			},
			"images": []any{"imgA", "imgB"},
		},
	}, 200)
	if len(store.Items) != 1 || store.Items[0].Kind != "user" || len(store.Items[0].Contexts) != 2 || len(store.Items[0].Images) != 2 {
		t.Fatalf("expected queued items replaced by single coalesced user item, got %+v", store.Items)
	}
}

func TestComposerBottomStaysSingleRowWithAttachmentsTray(t *testing.T) {
	app := NewNalarupaApp(t.TempDir())
	app.Init()
	defer app.Close()

	app.StartTemplate("mobile", true)
	app.RightTab = "agent"
	app.AgentUI.Backend = "provider"
	app.AgentUI.Effort = "max"
	app.AgentUI.ProviderModel = "qwen4-max"
	app.AgentUI.ActiveProvID = "qwen"
	app.AgentUI.Providers = []ProviderConfig{{
		ID:      "qwen",
		Name:    "Qwen",
		Format:  "chat",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models:  []string{"qwen4-max"},
		Vision:  false,
	}}
	frames := app.Bridge.FramesDirect()
	if len(frames) > 0 {
		app.AttachNodesToAgentChat([]string{frames[0].ID})
	}
	app.AttachViewportToAgentChat()
	dir := t.TempDir()
	p1, _ := makeTestImageFile(t, dir, "a.png", "png", 64, 64, color.RGBA{255, 0, 0, 255})
	p2, _ := makeTestImageFile(t, dir, "b.png", "png", 64, 64, color.RGBA{0, 255, 0, 255})
	app.AttachImageFiles([]string{p1, p2})

	tt := ui.NewTester(app.View, 1280, 800)
	tt.Frame()
	assertAllInteractiveControlsNamed(t, tt, "Composer with attachments tray at 1280x800")

	var modelY, effortY, skillsY, cookY, sendY float32
	foundModel, foundEffort, foundSkills, foundCook, foundSend := false, false, false, false, false
	for _, n := range tt.AccessAll() {
		switch n.Name {
		case "Model: qwen4-max":
			modelY, foundModel = n.Bounds.Y, true
		case "Reasoning effort: Max":
			effortY, foundEffort = n.Bounds.Y, true
		case "Agent skills":
			skillsY, foundSkills = n.Bounds.Y, true
		case "Let it cook: off":
			cookY, foundCook = n.Bounds.Y, true
		case "Send":
			sendY, foundSend = n.Bounds.Y, true
		}
	}
	if !foundModel || !foundEffort || !foundSkills || !foundCook || !foundSend {
		t.Fatalf("missing composer-bottom controls: model=%v effort=%v skills=%v cook=%v send=%v",
			foundModel, foundEffort, foundSkills, foundCook, foundSend)
	}
	for name, y := range map[string]float32{
		"effort": effortY,
		"skills": skillsY,
		"cook":   cookY,
		"send":   sendY,
	} {
		if math.Abs(float64(y-modelY)) > 2.0 {
			t.Fatalf("composer-bottom control %q wrapped onto another row with attachments tray: modelY=%.1f %sY=%.1f", name, modelY, name, y)
		}
	}
}
