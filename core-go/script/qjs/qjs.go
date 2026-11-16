// Package qjs is quickjs-ng 0.16.2, the engine rquickjs 0.14 bundles, through cgo: the few calls package script
// needs. The C sources are vendored unchanged from the rquickjs-sys 0.14.0 crate (README.md) and built as its
// build.rs builds them; wrap.c holds the glue.
//
// A Runtime is one JSRuntime with one context, set up as rquickjs's Runtime::new and Context::full set it up. It
// locks its goroutine to the OS thread from New to Close, so C always runs on one thread and stack; only Arm,
// Expired, Cancel and Canceled may be called from another goroutine. Values never outlive their Runtime.
package qjs

/*
#cgo CFLAGS: -D_GNU_SOURCE
#cgo windows CFLAGS: -DWIN32_LEAN_AND_MEAN -std=c11
#cgo !windows LDFLAGS: -lm
#include <stdlib.h>
#include "wrap.h"

static JSValue new_string(JSContext *ctx, _GoString_ s) {
	return JS_NewStringLen(ctx, _GoStringPtr(s), _GoStringLen(s));
}

static JSValue get_prop(JSContext *ctx, JSValue v, _GoString_ k) {
	JSAtom a = JS_NewAtomLen(ctx, _GoStringPtr(k), _GoStringLen(k));
	if (a == JS_ATOM_NULL)
		return JS_EXCEPTION;
	JSValue p = JS_GetProperty(ctx, v, a);
	JS_FreeAtom(ctx, a);
	return p;
}
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// Value tags (JS_TAG_*).
const (
	TagBigInt      = C.JS_TAG_BIG_INT
	TagSymbol      = C.JS_TAG_SYMBOL
	TagString      = C.JS_TAG_STRING
	TagStringRope  = C.JS_TAG_STRING_ROPE
	TagObject      = C.JS_TAG_OBJECT
	TagInt         = C.JS_TAG_INT
	TagBool        = C.JS_TAG_BOOL
	TagNull        = C.JS_TAG_NULL
	TagUndefined   = C.JS_TAG_UNDEFINED
	TagException   = C.JS_TAG_EXCEPTION
	TagShortBigInt = C.JS_TAG_SHORT_BIG_INT
	TagFloat64     = C.JS_TAG_FLOAT64
)

// Value is a JSValue. One returned by a Runtime is owned by the caller, who frees it.
type Value struct{ v C.JSValue }

// Tag is v's JS_TAG_*.
func (v Value) Tag() int { return int(C.qjs_tag(v.v)) }

// Undefined is the undefined value.
func Undefined() Value { return Value{C.qjs_undefined()} }

// Runtime is one JSRuntime and its context.
type Runtime struct {
	ctx   *C.JSContext
	guard *C.qjs_guard
}

// New is a runtime capped at memory bytes of heap and stack bytes of C stack, with the interrupt handler armed by
// Arm and Cancel. It locks the calling goroutine to its thread until Close.
func New(memory, stack int) (*Runtime, error) {
	g := C.qjs_guard_new()
	if g == nil {
		return nil, errors.New("out of memory")
	}
	runtime.LockOSThread()
	ctx := C.qjs_new(C.size_t(memory), C.size_t(stack), g)
	if ctx == nil {
		runtime.UnlockOSThread()
		C.qjs_guard_free(g)
		return nil, errors.New("cannot create a JavaScript runtime")
	}
	return &Runtime{ctx: ctx, guard: g}, nil
}

// Close frees the runtime; every Value taken from it must be freed first. Call it on the goroutine that called New.
func (r *Runtime) Close() {
	if r.ctx == nil {
		return
	}
	C.qjs_free(r.ctx)
	C.qjs_guard_free(r.guard)
	*r = Runtime{}
	runtime.UnlockOSThread()
}

// Now is the engine's monotonic clock, in nanoseconds: the clock Arm's deadline is on.
func Now() int64 { return int64(C.qjs_now()) }

// Arm sets the Now time past which running JavaScript is interrupted; 0 disarms it.
func (r *Runtime) Arm(deadline int64) { C.qjs_arm(r.guard, C.int64_t(deadline)) }

// Expired reports whether the armed deadline has passed.
func (r *Runtime) Expired() bool { return C.qjs_expired(r.guard) != 0 }

// Cancel interrupts running JavaScript at its next poll and every later one.
func (r *Runtime) Cancel() { C.qjs_cancel(r.guard) }

// Canceled reports whether Cancel was called.
func (r *Runtime) Canceled() bool { return C.qjs_canceled(r.guard) != 0 }

// Eval runs src as global code named file, in strict mode when strict. A thrown value gives TagException (see
// Exception).
func (r *Runtime) Eval(src, file string, strict bool) Value {
	code, name := C.CString(src), C.CString(file)
	defer C.free(unsafe.Pointer(code))
	defer C.free(unsafe.Pointer(name))
	flags := C.int(C.JS_EVAL_TYPE_GLOBAL)
	if strict {
		flags |= C.JS_EVAL_FLAG_STRICT
	}
	return Value{C.qjs_eval(r.ctx, code, C.size_t(len(src)), name, flags)}
}

// Call calls f with this and args (which stay owned by the caller).
func (r *Runtime) Call(f, this Value, args ...Value) Value {
	argv := make([]C.JSValue, len(args))
	for i, a := range args {
		argv[i] = a.v
	}
	var p *C.JSValue
	if len(argv) > 0 {
		p = &argv[0]
	}
	return Value{C.qjs_call(r.ctx, f.v, this.v, C.int(len(argv)), p)}
}

// RunJob runs one pending job: 0 when there was none, 1 when one ran, -1 when it threw (see Exception).
func (r *Runtime) RunJob() int { return int(C.qjs_run_job(r.ctx)) }

// Heap is the bytes the runtime has allocated (JS_ComputeMemoryUsage's malloc_size).
func (r *Runtime) Heap() int64 { return int64(C.qjs_heap(r.ctx)) }

// TrackRejections makes every promise rejection, and every later handling of one, call the global function name
// with (promise, reason, handled) when it is one.
func (r *Runtime) TrackRejections(name string) error {
	n := C.CString(name)
	defer C.free(unsafe.Pointer(n))
	if C.qjs_track_rejections(r.ctx, r.guard, n) != 0 {
		return errors.New("out of memory")
	}
	return nil
}

// Exception takes the pending exception.
func (r *Runtime) Exception() Value { return Value{C.JS_GetException(r.ctx)} }

// Free releases v.
func (r *Runtime) Free(v Value) { C.JS_FreeValue(r.ctx, v.v) }

// NewString is s as a JS string.
func (r *Runtime) NewString(s string) Value { return Value{C.new_string(r.ctx, s)} }

// Get is v's property k. A throwing getter gives TagException.
func (r *Runtime) Get(v Value, k string) Value { return Value{C.get_prop(r.ctx, v.v, k)} }

// ToString is JS ToString(v); false when it throws, with the exception pending.
func (r *Runtime) ToString(v Value) (string, bool) {
	var n C.size_t
	p := C.JS_ToCStringLen2(r.ctx, &n, v.v, false)
	if p == nil {
		return "", false
	}
	defer C.JS_FreeCString(r.ctx, p)
	return C.GoStringN(p, C.int(n)), true
}

// IsFunction reports whether v can be called.
func (r *Runtime) IsFunction(v Value) bool { return bool(C.JS_IsFunction(r.ctx, v.v)) }

// IsError reports whether v is an Error object.
func IsError(v Value) bool { return bool(C.JS_IsError(v.v)) }

// IsArray reports whether v is an Array.
func IsArray(v Value) bool { return bool(C.JS_IsArray(v.v)) }
