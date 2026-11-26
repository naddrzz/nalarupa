package main

import (
	"image"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestCanvasRenderWorkerCoalescesAndSwaps(t *testing.T) {
	b := NewBridge(t.TempDir())
	defer b.Shutdown()

	var mu sync.Mutex
	var renderedZooms []float64
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	readyCh := make(chan struct{}, 8)

	w := NewCanvasRenderWorker(b, func() {
		select {
		case readyCh <- struct{}{}:
		default:
		}
	})
	defer w.Close()

	w.testBeforeRender = func(job canvasRenderJob) {
		mu.Lock()
		idx := len(renderedZooms)
		renderedZooms = append(renderedZooms, job.zoom)
		mu.Unlock()
		if idx == 0 {
			close(firstStarted)
			<-releaseFirst
		}
	}

	ver := b.VersionDirect()
	key1 := canvasRenderKey{version: ver, pw: 120, ph: 80, zoom: 1.0, sharp: true}
	w.Submit(canvasRenderJob{key: key1, w: 120, h: 80, zoom: 1.0, bg: CanvasBG, sharp: true})

	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first worker job to start")
	}

	// Enqueue 3 rapid intermediate zoom jobs while job #1 is in flight; single-slot pending must coalesce to 2.0!
	for _, z := range []float64{1.2, 1.5, 2.0} {
		kz := canvasRenderKey{version: ver, pw: 120, ph: 80, zoom: z, sharp: true}
		w.Submit(canvasRenderJob{key: kz, w: 120, h: 80, zoom: z, bg: CanvasBG, sharp: true})
	}
	close(releaseFirst)

	// Drain until the coalesced final 2.0 frame completes.
	var dst *image.RGBA
	var gotKey canvasRenderKey
	var gotMS float64
	deadline := time.After(5 * time.Second)
	for gotKey.zoom != 2.0 {
		select {
		case <-readyCh:
			if k, ms, ok := w.TakeReady(&dst); ok {
				gotKey, gotMS = k, ms
			}
		case <-deadline:
			t.Fatalf("timed out waiting for coalesced 2.0 frame; last gotKey=%+v", gotKey)
		}
	}

	wantFinal := canvasRenderKey{version: ver, pw: 120, ph: 80, zoom: 2.0, sharp: true}
	if gotKey != wantFinal {
		t.Fatalf("gotKey = %+v, want %+v", gotKey, wantFinal)
	}
	if dst == nil || dst.Bounds().Dx() != 120 || dst.Bounds().Dy() != 80 || gotMS < 0 {
		t.Fatalf("unexpected buffer %v ms=%v", dst, gotMS)
	}
	mu.Lock()
	gotZooms := slices.Clone(renderedZooms)
	mu.Unlock()
	if !slices.Equal(gotZooms, []float64{1.0, 2.0}) {
		t.Fatalf("expected single-slot coalescing to run only [1.0, 2.0], got %v", gotZooms)
	}
	if _, _, okAgain := w.TakeReady(&dst); okAgain {
		t.Fatal("expected second TakeReady to return false when no new frame rendered")
	}
}
