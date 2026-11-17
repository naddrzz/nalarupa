package main

/*
#include <stdlib.h>
*/
import "C"

import "unsafe"

// On Windows the entry points are Go exports: the JavaScript engine suspends threads with SuspendThread, which
// does not care which stack a thread runs on (see bridge_unix.c for the other systems).

// nr_call runs the JSON command in the len bytes at ptr; free the answer with nr_free.
//
//export nr_call
func nr_call(ptr unsafe.Pointer, n uint32) unsafe.Pointer { return callCore(ptr, n) }

// nr_free frees an answer of nr_call.
//
//export nr_free
func nr_free(p unsafe.Pointer) { C.free(p) }

// nr_render paints the canvas (see renderCore).
//
//export nr_render
func nr_render(w, h uint32, camX, camY, zoom float64, bg uint32, soloPtr unsafe.Pointer, soloLen uint32, flags uint32) unsafe.Pointer {
	return renderCore(w, h, camX, camY, zoom, bg, soloPtr, soloLen, flags)
}
