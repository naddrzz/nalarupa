package main

import (
	"image"
	"math"
	"sync"
	"time"

	"github.com/naddrzz/nalarupa/core-go/render"
)

type canvasRenderJob struct {
	key          canvasRenderKey
	w, h         int
	camX, camY   float64
	zoom         float64
	bg           [3]uint8
	sharp        bool
	wantOverview bool
}

// CanvasRenderWorker renders canvas bitmaps (both overscanned detail and whole-document overview)
// off the UI thread with single-slot coalescing and zero-copy double-buffered *image.RGBA swaps.
type CanvasRenderWorker struct {
	bridge           *Bridge
	onReady          func()
	testBeforeRender func(job canvasRenderJob)

	mu           sync.Mutex
	docEpoch     uint64
	pending      *canvasRenderJob
	inFlight     bool
	closed       bool
	backBuf      *image.RGBA
	readyBuf     *image.RGBA
	readyKey     canvasRenderKey
	readyMS      float64
	readyValid   bool
	ovBackBuf    *image.RGBA
	ovReadyBuf   *image.RGBA
	ovReadyRect  RectD
	ovReadyVer   uint64
	ovReadyHas   bool
	ovReadyValid bool
	lastOvVer    uint64

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
}

// NewCanvasRenderWorker starts a dedicated render worker goroutine bound to bridge.
// Call Close() during application shutdown to release the goroutine cleanly.
func NewCanvasRenderWorker(bridge *Bridge, onReady func()) *CanvasRenderWorker {
	w := &CanvasRenderWorker{
		bridge:    bridge,
		onReady:   onReady,
		lastOvVer: ^uint64(0),
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go w.loop()
	return w
}

// Submit schedules job to run off the UI thread. Any unstarted older job is replaced so
// the worker always renders the latest camera/document version without building a backlog.
func (w *CanvasRenderWorker) Submit(job canvasRenderJob) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if w.pending != nil && w.pending.wantOverview {
		job.wantOverview = true
	}
	cp := job
	w.pending = &cp
	w.mu.Unlock()

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// TakeReady swaps the most recently completed off-thread detail frame into *dst in O(1) time.
func (w *CanvasRenderWorker) TakeReady(dst **image.RGBA) (canvasRenderKey, float64, bool) {
	if w == nil {
		return canvasRenderKey{}, 0, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.readyValid || w.readyBuf == nil {
		return canvasRenderKey{}, 0, false
	}
	*dst, w.readyBuf = w.readyBuf, *dst
	key := w.readyKey
	ms := w.readyMS
	w.readyValid = false
	return key, ms, true
}

// TakeOverviewReady swaps the most recently completed whole-document overview into *dst in O(1) time.
func (w *CanvasRenderWorker) TakeOverviewReady(dst **image.RGBA) (RectD, uint64, bool, bool) {
	if w == nil {
		return RectD{}, 0, false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ovReadyValid {
		return RectD{}, 0, false, false
	}
	hasImg := w.ovReadyHas && w.ovReadyBuf != nil
	if hasImg {
		*dst, w.ovReadyBuf = w.ovReadyBuf, *dst
	}
	rect := w.ovReadyRect
	ver := w.ovReadyVer
	w.ovReadyValid = false
	return rect, ver, hasImg, true
}

// Busy reports whether a render job is queued or currently executing.
func (w *CanvasRenderWorker) Busy() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	busy := w.inFlight || w.pending != nil
	w.mu.Unlock()
	return busy
}

// HasReady reports whether a completed detail or overview frame is waiting to be swapped onto the GPU.
func (w *CanvasRenderWorker) HasReady() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	ready := (w.readyValid && w.readyBuf != nil) || w.ovReadyValid
	w.mu.Unlock()
	return ready
}

// PeekReadyKey returns the render key of a completed detail frame if one is waiting to be swapped.
func (w *CanvasRenderWorker) PeekReadyKey() (canvasRenderKey, bool) {
	if w == nil {
		return canvasRenderKey{}, false
	}
	w.mu.Lock()
	ok := w.readyValid && w.readyBuf != nil
	key := w.readyKey
	w.mu.Unlock()
	return key, ok
}

// ResetForNewDoc discards any pending or ready frames from a previously open document.
func (w *CanvasRenderWorker) ResetForNewDoc() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.docEpoch++
	w.pending = nil
	w.readyValid = false
	w.ovReadyValid = false
	w.lastOvVer = ^uint64(0)
	w.mu.Unlock()
}

// Close stops the worker goroutine and waits for it to exit cleanly.
func (w *CanvasRenderWorker) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.pending = nil
	close(w.stop)
	w.mu.Unlock()
	<-w.done
}

func (w *CanvasRenderWorker) loop() {
	defer close(w.done)
	det := render.NewDetachedRenderer()
	var cachedSnap render.SceneSnapshot
	var cachedRelSeq uint64
	var cachedEpoch uint64
	hasCached := false
	var lastZoom, lastCamX, lastCamY float64
	for {
		select {
		case <-w.stop:
			return
		case <-w.wake:
			for {
				w.mu.Lock()
				if w.closed {
					w.mu.Unlock()
					return
				}
				job := w.pending
				w.pending = nil
				if job == nil {
					w.inFlight = false
					w.mu.Unlock()
					break
				}
				w.inFlight = true
				jobEpoch := w.docEpoch
				wantOv := job.wantOverview
				hook := w.testBeforeRender
				w.mu.Unlock()

				if cachedEpoch != jobEpoch {
					cachedSnap = render.SceneSnapshot{}
					cachedRelSeq = 0
					hasCached = false
					cachedEpoch = jobEpoch
				}
				if hook != nil {
					hook(*job)
				}

				preferLockFree := hasCached && (math.Abs(job.zoom-lastZoom)/max(0.01, job.zoom) > 0.004 ||
					math.Hypot(job.camX-lastCamX, job.camY-lastCamY)*job.zoom > 2.0)

				start := time.Now()
				renderedVer, renderedRelSeq, ovRect, ovVer, ovOK := w.bridge.RenderDetachedCachedWithOverview(
					det, &cachedSnap, &cachedRelSeq, &hasCached, preferLockFree,
					&w.backBuf, job.w, job.h, job.camX, job.camY, job.zoom,
					job.bg, "", job.sharp, wantOv, &w.ovBackBuf, 1664,
				)
				elapsedMS := float64(time.Since(start).Microseconds()) / 1000.0
				lastZoom, lastCamX, lastCamY = job.zoom, job.camX, job.camY

				doneKey := job.key
				if renderedVer > 0 || hasCached {
					doneKey.version = renderedVer
				}
				doneKey.releaseSeq = min(job.key.releaseSeq, renderedRelSeq)

				w.mu.Lock()
				closed := w.closed
				sameDoc := w.docEpoch == jobEpoch
				if !closed && sameDoc {
					w.readyBuf, w.backBuf = w.backBuf, w.readyBuf
					w.readyKey = doneKey
					w.readyMS = elapsedMS
					w.readyValid = true
					if ovOK {
						w.ovReadyBuf, w.ovBackBuf = w.ovBackBuf, w.ovReadyBuf
						w.ovReadyRect = ovRect
						w.ovReadyVer = ovVer
						w.ovReadyHas = true
						w.ovReadyValid = true
						w.lastOvVer = ovVer
					} else if wantOv && !w.bridge.HasSlidesDirect() && renderedVer == job.key.version {
						w.ovReadyRect = RectD{}
						w.ovReadyVer = ovVer
						w.ovReadyHas = false
						w.ovReadyValid = true
						w.lastOvVer = ovVer
					}
				}
				w.inFlight = w.pending != nil
				w.mu.Unlock()

				if !closed && sameDoc && w.onReady != nil {
					w.onReady()
				}
			}
		}
	}
}
