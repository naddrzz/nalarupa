package qjs

// Natives: Go functions JavaScript calls by a global name, as rquickjs's `Function::new` closures over
// `Rest<Coerced<String>>`. This file holds the cgo export, so its preamble only declares.

/*
#include <stdint.h>
#include <stdlib.h>
#include "wrap.h"
*/
import "C"

import (
	"errors"
	"runtime/cgo"
	"unsafe"
)

// Native is a Go function JavaScript calls: its arguments as JS ToString makes them, its result a string. An
// error is thrown as a new Error with that message.
type Native func(args []string) (string, error)

// Define makes the global name call f.
func (r *Runtime) Define(name string, f Native) error {
	r.natives = append(r.natives, f)
	n := C.CString(name)
	defer C.free(unsafe.Pointer(n))
	if C.qjs_define_native(r.ctx, n, C.int(len(r.natives)-1)) != 0 {
		r.Free(r.Exception())
		return errors.New("cannot define " + name)
	}
	return nil
}

// cstr is s for a C call that copies it and keeps no pointer (s holds no Go pointers).
func cstr(s string) (*C.char, C.size_t) {
	return (*C.char)(unsafe.Pointer(unsafe.StringData(s))), C.size_t(len(s))
}

// qjsGoNative runs native index of the Runtime behind h. A Go panic is kept for the Runtime call that entered
// JavaScript to raise again, and becomes an uncatchable error here, so no JavaScript runs past it and the panic
// never unwinds through C.
//
//export qjsGoNative
func qjsGoNative(ctx *C.JSContext, h C.uintptr_t, index, argc C.int, argv *C.JSValue) (out C.JSValue) {
	r := cgo.Handle(h).Value().(*Runtime)
	defer func() {
		if p := recover(); p != nil {
			r.panicked, r.hasPanic = p, true
			out = C.qjs_throw_panic(ctx)
		}
	}()
	vals := unsafe.Slice(argv, int(argc))
	args := make([]string, len(vals))
	for i, v := range vals {
		s, ok := r.ToString(Value{v})
		if !ok {
			return C.qjs_exception()
		}
		args[i] = s
	}
	res, err := r.natives[index](args)
	if err != nil {
		msg := err.Error()
		p, n := cstr(msg)
		return C.qjs_throw_message(ctx, p, n)
	}
	p, n := cstr(res)
	return C.qjs_new_string(ctx, p, n)
}

// repanic raises a panic a native kept, after dropping the uncatchable error it left pending.
func (r *Runtime) repanic() {
	if !r.hasPanic {
		return
	}
	p := r.panicked
	r.panicked, r.hasPanic = nil, false
	r.Free(r.Exception())
	panic(p)
}

// Global is the global property name. A throwing getter gives TagException.
func (r *Runtime) Global(name string) Value {
	n := C.CString(name)
	defer C.free(unsafe.Pointer(n))
	v := Value{C.qjs_global(r.ctx, n)}
	r.afterJS(v)
	return v
}
