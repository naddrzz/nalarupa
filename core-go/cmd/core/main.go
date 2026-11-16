// Command core is the Nalarupa core as the C shared library the app loads through bun:ffi (lib.rs): `nr_call`
// answers a JSON command, `nr_free` frees that answer and `nr_render` paints the canvas. Build it with
// build.ps1 or build.sh (`go build -buildmode=c-shared`) into out/nalarupa_core.dll (libnalarupa_core.so).
//
// Ownership is Rust's: every `nr_call` answer is C memory the caller frees with `nr_free`; the render pixels are a
// C buffer the core reuses, valid until the next `nr_render`, never freed by the caller. No Go pointer is handed
// to C. A panic answers `internal error (panic)` (a null render) instead of taking the app down.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"bytes"
	"strings"
	"unicode/utf8"
	"unsafe"

	// The editor operations join the dispatcher.
	_ "github.com/naddrzz/nalarupa/core-go/editor"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func main() {}

// respond is `nr_call`'s answer to req: the envelope `{"ok":true,"data":…}` or `{"ok":false,"error":…}`.
func respond(req []byte) (out string) {
	defer func() {
		if recover() != nil {
			out = `{"ok":false,"error":"internal error (panic)"}`
		}
	}()
	var env *jsonv.Object
	if v, err := jsonv.Parse(req); err != nil {
		env = engine.Object("ok", false, "error", "bad request: "+err.Error())
	} else if data, err := engine.Global().Dispatch(v); err != nil {
		env = engine.Object("ok", false, "error", err.Error())
	} else {
		env = engine.Object("ok", true, "data", data)
	}
	// A C string ends at its first NUL.
	return strings.ReplaceAll(string(jsonv.Marshal(env)), "\x00", "")
}

// nr_call runs the JSON command in the len bytes at ptr; free the answer with nr_free.
//
//export nr_call
func nr_call(ptr unsafe.Pointer, n uint32) unsafe.Pointer {
	var req []byte
	if ptr != nil {
		req = bytes.Clone(unsafe.Slice((*byte)(ptr), n))
	}
	return unsafe.Pointer(C.CString(respond(req)))
}

// nr_free frees an answer of nr_call.
//
//export nr_free
func nr_free(p unsafe.Pointer) {
	C.free(p)
}

// pixels is the render buffer, in C memory so the caller may keep its pointer; it only grows. Guarded by the
// engine lock.
var pixels struct {
	buf  unsafe.Pointer
	size int
}

// nr_render paints the viewport (camera in document units) as w·h·4 RGBA bytes on bg (0xRRGGBB), only the
// top-level node solo when its UTF-8 bytes are given (presentation mode). flags bit 0 is sharp: never answer a
// zoom change with the scaled cached view. The pixels stay valid until the next call; a panic gives null.
//
//export nr_render
func nr_render(w, h uint32, camX, camY, zoom float64, bg uint32, soloPtr unsafe.Pointer, soloLen uint32, flags uint32) (out unsafe.Pointer) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	solo := ""
	if soloPtr != nil && soloLen > 0 {
		if b := unsafe.Slice((*byte)(soloPtr), soloLen); utf8.Valid(b) {
			solo = string(b)
		}
	}
	e := engine.Global()
	e.Lock()
	defer e.Unlock()
	px := e.Render(w, h, camX, camY, zoom, [3]uint8{uint8(bg >> 16), uint8(bg >> 8), uint8(bg)}, solo, flags&1 != 0)
	if len(px) > pixels.size {
		C.free(pixels.buf)
		pixels.buf, pixels.size = C.malloc(C.size_t(len(px))), 0
		if pixels.buf == nil {
			return nil
		}
		pixels.size = len(px)
	}
	copy(unsafe.Slice((*byte)(pixels.buf), len(px)), px)
	return pixels.buf
}
