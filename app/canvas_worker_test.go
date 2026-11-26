package main

import (
	"image"
	"testing"
	"time"
)

func TestCanvasRenderWorkerCoalescesAndSwaps(t *testing.T) {
	b := NewBridge(t.TempDir())
	defer b.Shutdown()

	readyCh := make(chan struct{}, 8)
	w := NewCanvasRenderWorker(b, func() {
		select {
		case readyCh <- struct{}{}:
		default:
		}
	})
	defer w.Close()

	key := canvasRenderKey{version: 1, pw: 120, ph: 80, zoom: 1, sharp: true}
	w.Submit(canvasRenderJob{key: key, w: 120, h: 80, zoom: 1, bg: CanvasBG, sharp: true})

	select {
	case <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async canvas render")
	}

	var dst *image.RGBA
	gotKey, ms, ok := w.TakeReady(&dst)
	if !ok || dst == nil {
		t.Fatalf("TakeReady failed: ok=%v dst=%v", ok, dst)
	}
	if gotKey != key {
		t.Fatalf("gotKey = %+v, want %+v", gotKey, key)
	}
	if dst.Bounds().Dx() != 120 || dst.Bounds().Dy() != 80 || ms < 0 {
		t.Fatalf("unexpected buffer %v ms=%v", dst.Bounds(), ms)
	}
	if _, _, okAgain := w.TakeReady(&dst); okAgain {
		t.Fatal("expected second TakeReady to return false when no new frame rendered")
	}
}
