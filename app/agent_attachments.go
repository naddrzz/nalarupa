package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	maxComposerImages    = 10
	maxImageLongSide     = 1568
	maxRawImageFileBytes = 25 << 20
	maxEncodedImageBytes = 4 << 20
	thumbBitmapMaxSide   = 112
)

// PendingCanvasContext is a canvas node or viewport screen pinned in the composer prior to send.
type PendingCanvasContext struct {
	Kind     string      // "node" | "viewport"
	ID       string      // node id when Kind == "node"
	Name     string      // display label
	NodeType string      // e.g. "frame", "text", "viewport"
	W        float64
	H        float64
	Viewport *[4]float64
}

// PendingImageAttachment is a validated, downscaled image staged in the composer tray.
type PendingImageAttachment struct {
	ID        string
	Name      string
	MediaType string
	Data      string // base64 bytes
	Width     int
	Height    int
	Thumb     *ui.Bitmap
}

type decodedAttachmentItem struct {
	Name      string
	MediaType string
	Data      string
	Width     int
	Height    int
	ThumbRGBA *image.RGBA
}

type attachBatchResult struct {
	Items        []decodedAttachmentItem
	InvalidCount int
	FirstErr     string
}

func (a *NalarupaApp) activeModelSupportsVision() bool {
	if a.AgentUI.Backend == "provider" {
		if ap := a.activeProvider(); ap != nil {
			return ap.Vision
		}
	}
	return true
}

func (a *NalarupaApp) hasPendingAttachments() bool {
	return len(a.AgentUI.PendingContexts) > 0 || len(a.AgentUI.PendingImages) > 0
}

func (a *NalarupaApp) releasePendingImageBitmaps() {
	for i := range a.AgentUI.PendingImages {
		if a.AgentUI.PendingImages[i].Thumb != nil {
			a.AgentUI.PendingImages[i].Thumb.ReleasePixels()
			a.AgentUI.PendingImages[i].Thumb = nil
		}
	}
	a.AgentUI.PendingImages = nil
}

func (a *NalarupaApp) clearPendingAttachments() {
	a.AgentUI.PendingContexts = nil
	a.releasePendingImageBitmaps()
}

func (a *NalarupaApp) removePendingContextAt(idx int) {
	if idx < 0 || idx >= len(a.AgentUI.PendingContexts) {
		return
	}
	a.AgentUI.PendingContexts = append(a.AgentUI.PendingContexts[:idx], a.AgentUI.PendingContexts[idx+1:]...)
}

func (a *NalarupaApp) removePendingImageByID(id string) {
	for i := range a.AgentUI.PendingImages {
		if a.AgentUI.PendingImages[i].ID == id {
			if a.AgentUI.PendingImages[i].Thumb != nil {
				a.AgentUI.PendingImages[i].Thumb.ReleasePixels()
				a.AgentUI.PendingImages[i].Thumb = nil
			}
			a.AgentUI.PendingImages = append(a.AgentUI.PendingImages[:i], a.AgentUI.PendingImages[i+1:]...)
			return
		}
	}
}

func (a *NalarupaApp) pendingContextKey(ctx PendingCanvasContext) string {
	if ctx.Kind == "viewport" {
		return "viewport"
	}
	return "node:" + ctx.ID
}

func formatPendingContextLabel(ctx PendingCanvasContext) string {
	if ctx.Kind == "viewport" {
		if ctx.W > 0 && ctx.H > 0 {
			return fmt.Sprintf("Viewport · %s×%s", formatNum(ctx.W), formatNum(ctx.H))
		}
		return "Canvas viewport"
	}
	name := strings.TrimSpace(ctx.Name)
	if name == "" {
		name = ctx.ID
	}
	if len([]rune(name)) > 20 {
		name = string([]rune(name)[:19]) + "…"
	}
	if ctx.W > 0 && ctx.H > 0 {
		return fmt.Sprintf("%s · %s×%s", name, formatNum(ctx.W), formatNum(ctx.H))
	}
	return name
}

func formatChatContextLabel(ctx ChatContextRef) string {
	return formatPendingContextLabel(PendingCanvasContext{
		Kind:     ctx.Kind,
		ID:       ctx.ID,
		Name:     ctx.Name,
		NodeType: ctx.NodeType,
		W:        ctx.W,
		H:        ctx.H,
		Viewport: ctx.Viewport,
	})
}

func (a *NalarupaApp) resolveNodePendingContext(id string) (PendingCanvasContext, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return PendingCanvasContext{}, false
	}
	boxes := a.Bridge.BoxesDirect([]string{id})
	if len(boxes) > 0 {
		b := boxes[0]
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = id
		}
		return PendingCanvasContext{
			Kind:     "node",
			ID:       id,
			Name:     name,
			NodeType: b.Type,
			W:        math.Round(b.W),
			H:        math.Round(b.H),
		}, true
	}
	var nd NodeDetail
	if a.Bridge.TryCall("node", map[string]any{"id": id}, &nd) && nd.Exists {
		name, _ := nd.Node["name"].(string)
		ntype, _ := nd.Node["type"].(string)
		if strings.TrimSpace(name) == "" {
			name = id
		}
		var w, h float64
		if nd.Box != nil {
			w, h = math.Round(nd.Box.W), math.Round(nd.Box.H)
		}
		return PendingCanvasContext{
			Kind:     "node",
			ID:       id,
			Name:     strings.TrimSpace(name),
			NodeType: ntype,
			W:        w,
			H:        h,
		}, true
	}
	return PendingCanvasContext{}, false
}

// AttachNodesToAgentChat pins one or more canvas nodes into the composer attachment tray,
// opens the Agent tab (without letting onSelectionChanged revert to Design), and focuses composer input.
func (a *NalarupaApp) AttachNodesToAgentChat(ids []string) int {
	added := 0
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if slices.ContainsFunc(a.AgentUI.PendingContexts, func(c PendingCanvasContext) bool {
			return c.Kind == "node" && c.ID == id
		}) {
			continue
		}
		ctx, ok := a.resolveNodePendingContext(id)
		if !ok {
			continue
		}
		a.AgentUI.PendingContexts = append(a.AgentUI.PendingContexts, ctx)
		added++
	}
	a.AgentUI.PickingCanvasCtx = false
	a.AgentUI.CanvasAttachOpen = false
	a.RightTab = "agent"
	a.AgentUI.InputHandle.Focus()
	return added
}

// AttachSelectionToAgentChat pins the current canvas selection into the Agent composer.
func (a *NalarupaApp) AttachSelectionToAgentChat() int {
	if len(a.State.Selection) == 0 {
		a.ShowToast("Select a canvas element first, or attach the viewport screen.", false)
		return 0
	}
	return a.AttachNodesToAgentChat(a.State.Selection)
}

// AttachViewportToAgentChat pins the current visible canvas viewport rectangle into the Agent composer.
func (a *NalarupaApp) AttachViewportToAgentChat() {
	z := max(0.02, a.Cam.Zoom)
	w := a.CanvasW / z
	h := a.CanvasH / z
	if w <= 0 || h <= 0 {
		w, h = 1280, 800
	}
	vp := [4]float64{a.Cam.X, a.Cam.Y, w, h}
	item := PendingCanvasContext{
		Kind:     "viewport",
		Name:     "Canvas viewport",
		NodeType: "viewport",
		W:        math.Round(w),
		H:        math.Round(h),
		Viewport: &vp,
	}
	replaced := false
	for i := range a.AgentUI.PendingContexts {
		if a.AgentUI.PendingContexts[i].Kind == "viewport" {
			a.AgentUI.PendingContexts[i] = item
			replaced = true
			break
		}
	}
	if !replaced {
		a.AgentUI.PendingContexts = append(a.AgentUI.PendingContexts, item)
	}
	a.AgentUI.PickingCanvasCtx = false
	a.AgentUI.CanvasAttachOpen = false
	a.RightTab = "agent"
	a.AgentUI.InputHandle.Focus()
}

// TriggerCanvasAttachShortcut handles Ctrl/Cmd+L: attaches selection when present, else current viewport.
func (a *NalarupaApp) TriggerCanvasAttachShortcut() {
	if len(a.State.Selection) > 0 {
		a.AttachSelectionToAgentChat()
		return
	}
	a.AttachViewportToAgentChat()
}

// TriggerAttachImagesDialog opens the native multi-select image picker off the main UI thread.
// It intentionally does not join a.bgWG across blocking mygo.Dialog.Open so quitting while the dialog is open
// never deadlocks Close().
func (a *NalarupaApp) TriggerAttachImagesDialog() {
	if a.AgentUI.pickImageFilesFn != nil {
		a.AgentUI.pickImageFilesFn(func(paths []string) {
			a.AttachImageFiles(paths)
		})
		return
	}
	if a.Window == nil {
		return
	}
	win := a.Window
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:   win,
			Title:    "Attach reference images",
			Multiple: true,
			Filters: []mygo.FileFilter{
				{Name: "Images", Extensions: []string{"png", "jpg", "jpeg", "webp", "gif"}},
			},
		})
		if err != nil || len(paths) == 0 || win.IsDestroyed() {
			return
		}
		a.beginAttachWork(win)
		res := decodeImageAttachmentFiles(paths)
		a.finishAttachWork(win, res)
	}()
}

// AttachImageFiles reads, validates, and downscales image files (off-UI-thread when live Window exists).
func (a *NalarupaApp) AttachImageFiles(paths []string) {
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		if t := strings.TrimSpace(p); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return
	}
	if a.Window == nil {
		a.applyAttachBatchResult(decodeImageAttachmentFiles(clean))
		return
	}
	win := a.Window
	a.beginAttachWork(nil)
	a.bgWG.Add(1)
	go func() {
		defer a.bgWG.Done()
		res := decodeImageAttachmentFiles(clean)
		a.finishAttachWork(win, res)
	}()
}

// AttachImageBytes validates and downscales raw image bytes (e.g. from clipboard PNG paste).
func (a *NalarupaApp) AttachImageBytes(name string, raw []byte) {
	if len(raw) == 0 {
		return
	}
	cp := append([]byte(nil), raw...)
	if a.Window == nil {
		a.applyAttachBatchResult(decodeImageAttachmentSingle(name, cp))
		return
	}
	win := a.Window
	a.beginAttachWork(nil)
	a.bgWG.Add(1)
	go func() {
		defer a.bgWG.Done()
		res := decodeImageAttachmentSingle(name, cp)
		a.finishAttachWork(win, res)
	}()
}

func (a *NalarupaApp) attachLoading() bool {
	a.AgentUI.attachMu.Lock()
	loading := a.AgentUI.attachPending > 0
	a.AgentUI.attachMu.Unlock()
	return loading
}

func (a *NalarupaApp) beginAttachWork(notifyWin *mygo.Window) {
	a.AgentUI.attachMu.Lock()
	a.AgentUI.attachPending++
	a.AgentUI.attachMu.Unlock()
	if notifyWin != nil && !notifyWin.IsDestroyed() {
		notifyWin.Update(func() {})
	}
}

func (a *NalarupaApp) finishAttachWork(win *mygo.Window, res attachBatchResult) {
	a.AgentUI.attachMu.Lock()
	if a.AgentUI.attachPending > 0 {
		a.AgentUI.attachPending--
	}
	a.AgentUI.attachBatches = append(a.AgentUI.attachBatches, res)
	a.AgentUI.attachMu.Unlock()
	if win != nil && !win.IsDestroyed() {
		win.Update(func() {
			a.drainAttachDone()
		})
	}
}

func (a *NalarupaApp) drainAttachDone() {
	a.AgentUI.attachMu.Lock()
	if len(a.AgentUI.attachBatches) == 0 {
		a.AgentUI.attachMu.Unlock()
		return
	}
	batches := a.AgentUI.attachBatches
	a.AgentUI.attachBatches = nil
	a.AgentUI.attachMu.Unlock()
	for _, res := range batches {
		a.applyAttachBatchResult(res)
	}
}

func (a *NalarupaApp) applyAttachBatchResult(res attachBatchResult) {
	added := 0
	overLimit := 0
	for _, item := range res.Items {
		if len(a.AgentUI.PendingImages) >= maxComposerImages {
			overLimit++
			continue
		}
		a.AgentUI.NextAttachSeq++
		var thumb *ui.Bitmap
		if item.ThumbRGBA != nil {
			thumb = ui.NewBitmap(item.ThumbRGBA)
		}
		a.AgentUI.PendingImages = append(a.AgentUI.PendingImages, PendingImageAttachment{
			ID:        fmt.Sprintf("img-%d", a.AgentUI.NextAttachSeq),
			Name:      item.Name,
			MediaType: item.MediaType,
			Data:      item.Data,
			Width:     item.Width,
			Height:    item.Height,
			Thumb:     thumb,
		})
		added++
	}
	if added > 0 {
		a.RightTab = "agent"
		a.AgentUI.InputHandle.Focus()
	}

	if res.InvalidCount > 0 || overLimit > 0 {
		var parts []string
		if added > 0 {
			unit := "image"
			if added != 1 {
				unit = "images"
			}
			parts = append(parts, fmt.Sprintf("Attached %d %s.", added, unit))
		}
		if res.InvalidCount > 0 {
			if res.InvalidCount == 1 && res.FirstErr != "" {
				parts = append(parts, res.FirstErr)
			} else {
				parts = append(parts, fmt.Sprintf("Skipped %d unsupported or unreadable file(s) (use PNG, JPEG, WebP, or GIF).", res.InvalidCount))
			}
		}
		if overLimit > 0 {
			parts = append(parts, fmt.Sprintf("Skipped %d image(s) over the %d-image limit.", overLimit, maxComposerImages))
		}
		a.ShowToast(strings.Join(parts, " "), added == 0)
	}
}

func decodeImageAttachmentFiles(paths []string) attachBatchResult {
	var res attachBatchResult
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			res.InvalidCount++
			if res.FirstErr == "" {
				res.FirstErr = fmt.Sprintf("Could not read %s: %v", filepath.Base(p), err)
			}
			continue
		}
		item, err := decodeImageAttachmentBytes(filepath.Base(p), raw)
		if err != nil {
			res.InvalidCount++
			if res.FirstErr == "" {
				res.FirstErr = fmt.Sprintf("%s: %v", filepath.Base(p), err)
			}
			continue
		}
		res.Items = append(res.Items, item)
	}
	return res
}

func decodeImageAttachmentSingle(name string, raw []byte) attachBatchResult {
	item, err := decodeImageAttachmentBytes(name, raw)
	if err != nil {
		return attachBatchResult{InvalidCount: 1, FirstErr: err.Error()}
	}
	return attachBatchResult{Items: []decodedAttachmentItem{item}}
}

func sniffImageFormat(raw []byte) (string, bool) {
	if len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png", true
	}
	if len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff {
		return "image/jpeg", true
	}
	if len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a") {
		return "image/gif", true
	}
	if len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP" {
		return "image/webp", true
	}
	return "", false
}

func decodeImageByMediaType(mediaType string, raw []byte) (image.Image, error) {
	r := bytes.NewReader(raw)
	switch mediaType {
	case "image/png":
		return png.Decode(r)
	case "image/jpeg":
		return jpeg.Decode(r)
	case "image/gif":
		return gif.Decode(r)
	case "image/webp":
		return webp.Decode(r)
	default:
		return nil, errors.New("unsupported image format (use PNG, JPEG, WebP, or GIF)")
	}
}

func scaleImageRGBA(src image.Image, maxSide int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}
	nw, nh := sw, sh
	if maxSide > 0 && max(sw, sh) > maxSide {
		scale := float64(maxSide) / float64(max(sw, sh))
		nw = max(1, int(math.Round(float64(sw)*scale)))
		nh = max(1, int(math.Round(float64(sh)*scale)))
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	if nw == sw && nh == sh {
		xdraw.Draw(dst, dst.Bounds(), src, sb.Min, xdraw.Src)
	} else {
		xdraw.BiLinear.Scale(dst, dst.Bounds(), src, sb, xdraw.Over, nil)
	}
	return dst
}

func decodeImageAttachmentBytes(name string, raw []byte) (decodedAttachmentItem, error) {
	if len(raw) == 0 {
		return decodedAttachmentItem{}, errors.New("image file is empty")
	}
	if len(raw) > maxRawImageFileBytes {
		return decodedAttachmentItem{}, errors.New("image exceeds 25 MB limit")
	}
	mediaType, ok := sniffImageFormat(raw)
	if !ok {
		return decodedAttachmentItem{}, errors.New("unsupported image format (use PNG, JPEG, WebP, or GIF)")
	}
	img, err := decodeImageByMediaType(mediaType, raw)
	if err != nil {
		return decodedAttachmentItem{}, fmt.Errorf("invalid image: %w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return decodedAttachmentItem{}, errors.New("image has zero dimensions")
	}

	outBytes := raw
	outMediaType := mediaType
	if max(w, h) > maxImageLongSide || len(raw) > maxEncodedImageBytes {
		scaled := scaleImageRGBA(img, maxImageLongSide)
		w, h = scaled.Bounds().Dx(), scaled.Bounds().Dy()
		var buf bytes.Buffer
		if mediaType == "image/jpeg" {
			if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 88}); err != nil {
				return decodedAttachmentItem{}, err
			}
			outMediaType = "image/jpeg"
		} else {
			if err := png.Encode(&buf, scaled); err != nil {
				return decodedAttachmentItem{}, err
			}
			outMediaType = "image/png"
		}
		outBytes = buf.Bytes()
		img = scaled
	}
	if len(outBytes) > maxEncodedImageBytes {
		// Re-encode large opaque/noisy images as JPEG, stepping down quality and dimensions until <= 4 MB.
		curMax := max(w, h)
		dims := []int{curMax, 1280, 1024, 800}
		qualities := []int{86, 74, 62}
		compressed := false
		prevDim := -1
		for _, dim := range dims {
			if dim > curMax || dim == prevDim {
				continue
			}
			prevDim = dim
			candidate := scaleImageRGBA(img, dim)
			for _, q := range qualities {
				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, candidate, &jpeg.Options{Quality: q}); err != nil {
					return decodedAttachmentItem{}, err
				}
				if buf.Len() <= maxEncodedImageBytes {
					outBytes = buf.Bytes()
					outMediaType = "image/jpeg"
					w, h = candidate.Bounds().Dx(), candidate.Bounds().Dy()
					img = candidate
					compressed = true
					break
				}
			}
			if compressed {
				break
			}
		}
		if !compressed {
			return decodedAttachmentItem{}, errors.New("image exceeds 4 MB encoded limit even after compression")
		}
	}

	if strings.TrimSpace(name) == "" {
		ext := ".png"
		switch outMediaType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/webp":
			ext = ".webp"
		case "image/gif":
			ext = ".gif"
		}
		name = "image" + ext
	}

	return decodedAttachmentItem{
		Name:      name,
		MediaType: outMediaType,
		Data:      base64.StdEncoding.EncodeToString(outBytes),
		Width:     w,
		Height:    h,
		ThumbRGBA: scaleImageRGBA(img, thumbBitmapMaxSide),
	}, nil
}

func decodeChatImageBitmap(b64 string, maxSide int) (*ui.Bitmap, error) {
	raw, err := DecodeBase64Image(b64)
	if err != nil {
		return nil, err
	}
	if maxSide > 0 {
		if bmp, err := ui.DecodeBitmapScaled(raw, maxSide); err == nil && bmp != nil {
			return bmp, nil
		}
	} else {
		if bmp, err := ui.DecodeBitmap(raw); err == nil && bmp != nil {
			return bmp, nil
		}
	}
	mt, ok := sniffImageFormat(raw)
	if !ok {
		return nil, errors.New("unsupported image format")
	}
	img, err := decodeImageByMediaType(mt, raw)
	if err != nil {
		return nil, err
	}
	rgba := scaleImageRGBA(img, maxSide)
	if rgba == nil {
		return nil, errors.New("empty image")
	}
	return ui.NewBitmap(rgba), nil
}

func (a *NalarupaApp) getCachedChatImage(cacheKey, b64 string, maxSide int) *ui.Bitmap {
	if a.AgentUI.ImageCache == nil {
		a.AgentUI.ImageCache = map[string]cachedToolImage{}
	}
	entry, ok := a.AgentUI.ImageCache[cacheKey]
	if !ok {
		entry = cachedToolImage{failed: true}
		if decoded, err := decodeChatImageBitmap(b64, maxSide); err == nil && decoded != nil {
			entry = cachedToolImage{bmp: decoded}
		}
		for len(a.AgentUI.ImageOrder) >= maxCachedToolImages {
			evictKey := a.AgentUI.ImageOrder[0]
			a.AgentUI.ImageOrder = a.AgentUI.ImageOrder[1:]
			if old, exists := a.AgentUI.ImageCache[evictKey]; exists {
				if old.bmp != nil {
					old.bmp.ReleasePixels()
				}
				delete(a.AgentUI.ImageCache, evictKey)
			}
		}
		a.AgentUI.ImageCache[cacheKey] = entry
		a.AgentUI.ImageOrder = append(a.AgentUI.ImageOrder, cacheKey)
	}
	return entry.bmp
}

func attachShortcutHint() string {
	if runtime.GOOS == "darwin" {
		return "⌘L"
	}
	return "Ctrl+L"
}

func (a *NalarupaApp) viewComposerAttachments(c *ui.Context, dragOver bool) {
	reduced := EffectiveReducedMotion(c)

	// 1. Compact attachment trigger strip (+ Canvas popover, + Image button, shortcut/count hint)
	var canvasBtn ui.Element
	ui.Row(c.Key("composer-attach-bar")).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
		canvasBtnLbl := "+ Canvas"
		if a.AgentUI.PickingCanvasCtx {
			canvasBtnLbl = "Picking…"
		} else if len(a.AgentUI.PendingContexts) > 0 {
			canvasBtnLbl = fmt.Sprintf("+ Canvas (%d)", len(a.AgentUI.PendingContexts))
		}
		canvasBtn = ui.ButtonBase(c.Key("attach-canvas-btn")).
			Height(22).
			Padding(0, 7).
			Radius(6).
			Row().
			Gap(4).
			AlignItems(ui.Center).
			Shrink(0).
			Label("Attach canvas context").
			Tooltip("Attach canvas selection, pick node, or viewport (" + attachShortcutHint() + ")").
			Bind(&a.AgentUI.CanvasAttachBtn).
			Cursor(ui.CursorPointer)
		if a.AgentUI.PickingCanvasCtx || len(a.AgentUI.PendingContexts) > 0 {
			canvasBtn.Background(C.AccentSoft).Border(1, C.Accent)
		} else if canvasBtn.Hovered() || a.AgentUI.CanvasAttachOpen {
			canvasBtn.Background(C.Raised).Border(1, C.Border)
		} else {
			canvasBtn.Background(C.Panel2).Border(1, C.Border)
		}
		canvasBtn.Children(func() {
			col := C.Muted
			if a.AgentUI.PickingCanvasCtx || len(a.AgentUI.PendingContexts) > 0 {
				col = C.Accent
			}
			ui.Icon(c, IconSVG("frame")).FontSize(11).TextColor(col).Shrink(0)
			ui.Text(c, canvasBtnLbl).FontSize(10.5).FontWeight(500).TextColor(col).MaxLines(1)
		})
		if canvasBtn.Clicked() {
			a.AgentUI.CanvasAttachOpen = !a.AgentUI.CanvasAttachOpen
		}

		imgBtnLbl := "+ Image"
		if len(a.AgentUI.PendingImages) > 0 {
			imgBtnLbl = fmt.Sprintf("+ Image (%d/%d)", len(a.AgentUI.PendingImages), maxComposerImages)
		}
		imgDisabled := len(a.AgentUI.PendingImages) >= maxComposerImages
		imgBtn := ui.ButtonBase(c.Key("attach-image-btn")).
			Height(22).
			Padding(0, 7).
			Radius(6).
			Row().
			Gap(4).
			AlignItems(ui.Center).
			Shrink(0).
			Disabled(imgDisabled).
			Label("Attach images").
			Tooltip("Attach up to 10 reference images (or paste / drag-and-drop)").
			Cursor(ui.CursorPointer)
		if len(a.AgentUI.PendingImages) > 0 && !imgDisabled {
			imgBtn.Background(C.AccentSoft).Border(1, C.Accent)
		} else if imgBtn.Hovered() && !imgDisabled {
			imgBtn.Background(C.Raised).Border(1, C.Border)
		} else {
			imgBtn.Background(C.Panel2).Border(1, C.Border)
		}
		if imgDisabled {
			imgBtn.Opacity(0.55)
		}
		imgBtn.Children(func() {
			col := C.Muted
			if len(a.AgentUI.PendingImages) > 0 && !imgDisabled {
				col = C.Accent
			}
			ui.Icon(c, IconSVG("image")).FontSize(11).TextColor(col).Shrink(0)
			ui.Text(c, imgBtnLbl).FontSize(10.5).FontWeight(500).TextColor(col).MaxLines(1)
		})
		if imgBtn.Clicked() && !imgDisabled {
			a.TriggerAttachImagesDialog()
		}

		ui.Spacer(c)
		if a.attachLoading() {
			ui.Text(c, "Loading…").FontSize(10).TextColor(C.Ghost).MaxLines(1)
		} else {
			ui.Text(c, attachShortcutHint()).FontSize(10).TextColor(C.Ghost).MaxLines(1)
		}
	})

	if canvasBtn.Valid() {
		cp := AnimatedPopover(c, canvasBtn, &a.AgentUI.CanvasAttachOpen, 0, 1, func() {
			ui.Column(c.Key("attach-canvas-menu")).Width(214).Padding(6).Gap(2).Background(C.Panel2).Radius(8).Children(func() {
				ui.Text(c, "Attach canvas context").FontSize(10.5).FontWeight(600).TextColor(C.Muted).Padding(2, 6)

				selCount := len(a.State.Selection)
				selLabel := "Current selection"
				if selCount > 0 {
					selLabel = fmt.Sprintf("Current selection (%d)", selCount)
				}
				selBtn := ui.ButtonBase(c.Key("ctx-opt-selection")).
					FillWidth().Height(26).Padding(0, 8).Radius(5).Row().Gap(6).AlignItems(ui.Center).
					Disabled(selCount == 0).Label("Attach current selection").Cursor(ui.CursorPointer)
				if selBtn.Hovered() && selCount > 0 {
					selBtn.Background(C.Raised)
				}
				if selCount == 0 {
					selBtn.Opacity(0.5)
				}
				selBtn.Children(func() {
					ui.Icon(c, IconSVG("frame")).FontSize(12).TextColor(C.Accent).Shrink(0)
					ui.Text(c, selLabel).FontSize(11.5).TextColor(C.Text).Grow(1).MaxLines(1)
				})
				if selBtn.Clicked() && selCount > 0 {
					a.AttachSelectionToAgentChat()
				}

				pickBtn := ui.ButtonBase(c.Key("ctx-opt-pick")).
					FillWidth().Height(26).Padding(0, 8).Radius(5).Row().Gap(6).AlignItems(ui.Center).
					Selected(a.AgentUI.PickingCanvasCtx).Label("Pick from canvas").Cursor(ui.CursorPointer)
				if pickBtn.Hovered() || a.AgentUI.PickingCanvasCtx {
					pickBtn.Background(C.Raised)
				}
				pickBtn.Children(func() {
					ui.Icon(c, IconSVG("cursor")).FontSize(12).TextColor(C.Accent).Shrink(0)
					ui.Text(c, "Pick from canvas").FontSize(11.5).TextColor(C.Text).Grow(1).MaxLines(1)
				})
				if pickBtn.Clicked() {
					a.AgentUI.PickingCanvasCtx = !a.AgentUI.PickingCanvasCtx
					a.AgentUI.CanvasAttachOpen = false
					if a.AgentUI.PickingCanvasCtx {
						a.CanvasHandle.Focus()
						a.ShowToast("Click an element on the canvas to attach it (Esc to cancel).", false)
					}
				}

				vpBtn := ui.ButtonBase(c.Key("ctx-opt-viewport")).
					FillWidth().Height(26).Padding(0, 8).Radius(5).Row().Gap(6).AlignItems(ui.Center).
					Label("Attach canvas viewport").Cursor(ui.CursorPointer)
				if vpBtn.Hovered() {
					vpBtn.Background(C.Raised)
				}
				vpBtn.Children(func() {
					ui.Icon(c, IconSVG("maximize")).FontSize(12).TextColor(C.Accent).Shrink(0)
					ui.Text(c, "Canvas viewport").FontSize(11.5).TextColor(C.Text).Grow(1).MaxLines(1)
				})
				if vpBtn.Clicked() {
					a.AttachViewportToAgentChat()
				}
			})
		})
		if cp.Dismissed() {
			a.AgentUI.CanvasAttachBtn.Focus()
		}
	}

	// 2. Pick-from-canvas active banner
	if a.AgentUI.PickingCanvasCtx {
		ui.Row(c.Key("composer-pick-banner")).
			FillWidth().
			Padding(5, 8).
			Gap(6).
			Radius(6).
			AlignItems(ui.Center).
			Background(C.AccentSoft).
			Border(1, C.Accent).
			Children(func() {
				ui.Text(c, "Pick mode: click canvas element (Esc cancels)").FontSize(10.5).TextColor(C.Accent).Grow(1).Wrap()
				cancelBtn := ui.ButtonBase(c.Key("ctx-pick-cancel")).
					Height(20).Padding(0, 6).Radius(4).Center().
					Label("Cancel pick from canvas").Cursor(ui.CursorPointer)
				if cancelBtn.Hovered() {
					cancelBtn.Background(C.Raised)
				} else {
					cancelBtn.Background(C.Panel2)
				}
				cancelBtn.Children(func() {
					ui.Text(c, "Cancel").FontSize(10).TextColor(C.Text)
				})
				if cancelBtn.Clicked() {
					a.AgentUI.PickingCanvasCtx = false
				}
			})
	}

	// 3. Drag-over drop banner
	if dragOver {
		ui.Row(c.Key("composer-drop-hint")).
			FillWidth().
			Padding(6, 8).
			Gap(6).
			Radius(6).
			AlignItems(ui.Center).
			Background(C.AccentSoft).
			Border(1, C.Accent).
			Children(func() {
				ui.Icon(c, IconSVG("image")).FontSize(12).TextColor(C.Accent).Shrink(0)
				ui.Text(c, "Drop images to attach (PNG, JPEG, WebP, GIF)").FontSize(10.5).FontWeight(500).TextColor(C.Accent).Grow(1).Wrap()
			})
	}

	// 4. Removable canvas context chips
	if len(a.AgentUI.PendingContexts) > 0 {
		removeCtxIdx := -1
		ui.Row(c.Key("composer-ctx-chips")).FillWidth().Wrap().Gap(4).Transition(ChipTransition(reduced)).Children(func() {
			for i, ctx := range a.AgentUI.PendingContexts {
				idx := i
				item := ctx
				lbl := formatPendingContextLabel(item)
				iconKey := NodeIconName(item.NodeType, false)
				if item.Kind == "viewport" {
					iconKey = "maximize"
				}
				chip := ui.ButtonBase(c.Key("rm-ctx-" + a.pendingContextKey(item))).
					Height(22).
					Padding(0, 7).
					Radius(11).
					Row().
					Gap(4).
					AlignItems(ui.Center).
					Label("Remove canvas context " + lbl).
					Tooltip("Click to remove " + lbl).
					Cursor(ui.CursorPointer).
					Transition(ColorHoverTransition(reduced))
				pressed := chip.Pressed()
				if chip.Hovered() {
					chip.Background(C.Raised).Border(1, C.Accent)
				} else {
					chip.Background(C.AccentSoft).Border(1, C.Accent)
				}
				ApplyPressScale(chip, pressed, false, reduced)
				chip.Children(func() {
					ui.Icon(c, IconSVG(iconKey)).FontSize(10).TextColor(C.Accent).Shrink(0)
					ui.Text(c, lbl).FontSize(10.5).FontWeight(500).TextColor(C.Accent).MaxLines(1)
					ui.Icon(c, IconSVG("x")).FontSize(10).TextColor(C.Accent).Shrink(0)
				})
				if chip.Clicked() {
					removeCtxIdx = idx
				}
			}
		})
		if removeCtxIdx >= 0 {
			a.removePendingContextAt(removeCtxIdx)
			a.AgentUI.InputHandle.Focus()
		}
	}

	// 5. Removable image thumbnail tray
	if len(a.AgentUI.PendingImages) > 0 {
		removeImgID := ""
		ui.Row(c.Key("composer-img-tray")).FillWidth().Wrap().Gap(6).Transition(ChipTransition(reduced)).Children(func() {
			for _, im := range a.AgentUI.PendingImages {
				item := im
				ui.Box(c.Key("pending-img-" + item.ID)).
					Size(48, 48).
					Radius(6).
					Background(C.Panel2).
					Border(1, C.Border).
					Clip().
					Children(func() {
						if item.Thumb != nil {
							ui.Image(c, item.Thumb).Size(48, 48).Radius(5).Fit(ui.Cover)
						} else {
							ui.Box(c).Fill().Center().Children(func() {
								ui.Icon(c, IconSVG("image")).FontSize(16).TextColor(C.Ghost)
							})
						}
						rm := ui.ButtonBase(c.Key("rm-img-" + item.ID)).
							Absolute().
							Top(2).
							Right(2).
							Size(16, 16).
							Radius(8).
							Center().
							Background(ui.RGBA(18, 18, 22, 0.85)).
							Border(1, C.Border).
							Label("Remove image " + item.Name).
							Tooltip("Remove " + item.Name).
							Cursor(ui.CursorPointer)
						if rm.Hovered() {
							rm.Background(C.Danger)
						}
						rm.Children(func() {
							ui.Icon(c, IconSVG("x")).FontSize(9).TextColor(ui.Hex("#FFFFFF"))
						})
						if rm.Clicked() {
							removeImgID = item.ID
						}
					})
			}
		})
		if removeImgID != "" {
			a.removePendingImageByID(removeImgID)
			a.AgentUI.InputHandle.Focus()
		}
	}

	// 6. Non-blocking warning when active BYOK provider has vision: false
	if !a.activeModelSupportsVision() && a.hasPendingAttachments() {
		warnText := "Active provider has Vision off — canvas context will be sent as text/JSON only."
		if len(a.AgentUI.PendingImages) > 0 {
			warnText = "Active provider has Vision off — attached images will not be seen by the model."
		}
		ui.Row(c.Key("composer-vision-warn")).
			FillWidth().
			Padding(5, 8).
			Gap(6).
			Radius(6).
			AlignItems(ui.Center).
			Background(C.WarnBg).
			Border(1, C.Warn).
			Children(func() {
				ui.Icon(c, IconSVG("alert")).FontSize(12).TextColor(C.Warn).Shrink(0)
				ui.Text(c, warnText).FontSize(10.5).TextColor(C.Warn).Grow(1).Wrap()
			})
	}
}
