package main

import (
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// ClipboardPayload holds rich HTML (e.g. data-nala-node-clipboard / data-pen-node-clipboard / Figma HTML)
// alongside plain text / SVG markup.
type ClipboardPayload struct {
	HTML string
	Text string
}

var (
	clipMu    sync.Mutex
	localClip ClipboardPayload
	// HeadlessClipboard forces in-process clipboard storage during isolated unit runs without a window.
	HeadlessClipboard bool
)

func buildTransferData(p ClipboardPayload) transfer.Data {
	var reps []transfer.Representation
	if p.HTML != "" {
		reps = append(reps, transfer.Bytes(transfer.HTML, []byte(p.HTML)))
	}
	if p.Text != "" || len(reps) == 0 {
		reps = append(reps, transfer.Bytes(transfer.Text, []byte(p.Text)))
	}
	return transfer.New(transfer.NewItem(reps...))
}

// WriteClipboard writes HTML and plain text via ui.Services (or OS clipboard) and mirrors in-process for headless unit tests.
func WriteClipboard(svc ui.Services, p ClipboardPayload) {
	clipMu.Lock()
	localClip = p
	headless := HeadlessClipboard
	clipMu.Unlock()

	data := buildTransferData(p)
	svc.WriteClipboardData(data)
	if headless {
		return
	}
	if svc == (ui.Services{}) {
		_ = mygo.Clipboard.Write(data)
	}
}

// ReadClipboard reads HTML and plain text via ui.Services (or OS clipboard).
// When a live window or Tester clipboard is attached (svc != zero), it returns the OS/Tester clipboard
// directly without falling back to stale in-memory data when the clipboard is empty or non-text.
func ReadClipboard(svc ui.Services) ClipboardPayload {
	clipMu.Lock()
	fallback := localClip
	headless := HeadlessClipboard
	clipMu.Unlock()

	if svc != (ui.Services{}) {
		d := svc.ReadClipboardData(transfer.HTML, transfer.Text)
		var html, text string
		if b, err := d.Read(transfer.HTML); err == nil {
			html = string(b)
		}
		if b, err := d.Read(transfer.Text); err == nil {
			text = string(b)
		}
		return ClipboardPayload{HTML: html, Text: text}
	}
	if headless {
		return fallback
	}
	return ClipboardPayload{
		HTML: mygo.Clipboard.ReadHTML(),
		Text: mygo.Clipboard.ReadText(),
	}
}
