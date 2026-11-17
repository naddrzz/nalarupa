//go:build !windows

package main

/*
#include "bridge_unix.h"
*/
import "C"

import "unsafe"

// init starts taking the jobs the C entry points queue (bridge_unix.c), each on its own goroutine: calls from the app's
// thread and from a worker run side by side, as they do on Windows.
func init() {
	C.nr_restore_signals()
	go func() {
		for {
			job := C.nr_next_job()
			go run(job)
		}
	}()
}

func run(job *C.nr_job) {
	var out unsafe.Pointer
	if job.kind == 0 {
		out = callCore(unsafe.Pointer(job.ptr), uint32(job.n))
	} else {
		out = renderCore(uint32(job.w), uint32(job.h), float64(job.cam_x), float64(job.cam_y), float64(job.zoom), uint32(job.bg),
			unsafe.Pointer(job.solo), uint32(job.solo_len), uint32(job.flags))
	}
	C.nr_finish(job, out)
}

// nr_call, nr_free and nr_render call the C entry points, so the tests go through the queue as the app does.

func nr_call(ptr unsafe.Pointer, n uint32) unsafe.Pointer { return C.nr_call(ptr, C.uint32_t(n)) }

func nr_free(p unsafe.Pointer) { C.nr_free(p) }

func nr_render(w, h uint32, camX, camY, zoom float64, bg uint32, soloPtr unsafe.Pointer, soloLen uint32, flags uint32) unsafe.Pointer {
	return C.nr_render(C.uint32_t(w), C.uint32_t(h), C.double(camX), C.double(camY), C.double(zoom), C.uint32_t(bg), soloPtr,
		C.uint32_t(soloLen), C.uint32_t(flags))
}
