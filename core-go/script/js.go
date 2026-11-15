package script

// The JavaScript engine: QuickJS (modernc.org/libquickjs, a pure Go transpilation, so no cgo) behind the
// few calls script nodes and the execute sandbox need. One JS is one runtime with one context, confined to
// the goroutine that made it; only Cancel may be called from another goroutine.

import (
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"modernc.org/libc"
	lib "modernc.org/libquickjs"
)

// Limits are the caps of one runtime.
type Limits struct {
	// Memory caps the JS heap, in bytes.
	Memory int
	// StackSlots caps the call depth. The transpiled engine runs on the goroutine stack and counts call
	// frames, not bytes: see stackSlots for how a byte budget maps to it.
	StackSlots int
}

// stackSlots is the call depth that matches rquickjs's 512 KB native stack: a one-line recursion reaches
// 146 frames under both (measured with the Rust CLI).
const stackSlots = 151

// maxFrames is how many frames a stack trace keeps, as quickjs-ng (rquickjs) does.
const maxFrames = 10

// guard is read by the interrupt handler on every poll: Cancel and Arm write it from Go. It lives in C
// memory, so the handler reaches it through the runtime's opaque pointer.
type guard struct {
	cancel int32
	_      int32
	// deadline is the monotonic time (see since) past which running JavaScript is interrupted; 0: none.
	deadline int64
	// start is the monotonic time the runtime was made, performance.now's origin.
	start int64
}

// epoch is the origin of since; it is set once and never written again.
var epoch = time.Now()

// since is the monotonic nanoseconds since epoch: unlike Unix time, a wall-clock step does not move it.
func since() int64 { return int64(time.Since(epoch)) }

// JS is one QuickJS runtime and its context.
type JS struct {
	tls   *libc.TLS
	rt    uintptr
	ctx   uintptr
	guard uintptr
	// tracker is the C string naming the global the rejection tracker calls, or 0.
	tracker uintptr
}

// Value is a JS value owned by the caller; Free releases it.
type Value struct{ v lib.TJSValue }

func undefined() lib.TJSValue { return lib.TJSValue{Ftag: lib.EJS_TAG_UNDEFINED} }

func jsBool(b bool) lib.TJSValue {
	v := lib.TJSValue{Ftag: lib.EJS_TAG_BOOL}
	if b {
		v.Fu.Fuint641 = 1
	}
	return v
}

// fp is a Go function as the function pointer the transpiled C calls.
func fp(f any) uintptr {
	type iface [2]uintptr
	return (*iface)(unsafe.Pointer(&f))[1]
}

// cptr is an address in C memory (libc's allocator, outside the Go heap) as a Go pointer.
func cptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func interrupt(_ *libc.TLS, _, opaque uintptr) int32 {
	g := (*guard)(cptr(opaque))
	if atomic.LoadInt32(&g.cancel) != 0 {
		return 1
	}
	if d := atomic.LoadInt64(&g.deadline); d != 0 && since() > d {
		return 1
	}
	return 0
}

// NewJS is a fresh runtime with limits l, no deadline and the standard globals only.
func NewJS(l Limits) (*JS, error) {
	tls := libc.NewTLS()
	rt := lib.XJS_NewRuntime(tls)
	if rt == 0 {
		tls.Close()
		return nil, errorString("cannot create a JavaScript runtime")
	}
	js := &JS{tls: tls, rt: rt}
	lib.XJS_SetMemoryLimit(tls, rt, libc.Tsize_t(l.Memory))
	lib.XJS_SetMaxStackSize(tls, rt, libc.Tsize_t(l.StackSlots))
	js.guard = libc.Xcalloc(tls, 1, libc.Tsize_t(unsafe.Sizeof(guard{})))
	if js.guard == 0 {
		js.Close()
		return nil, errorString("out of memory")
	}
	js.g().start = since()
	lib.XJS_SetRuntimeOpaque(tls, rt, js.guard)
	lib.XJS_SetInterruptHandler(tls, rt, fp(interrupt), js.guard)
	if js.ctx = lib.XJS_NewContext(tls, rt); js.ctx == 0 {
		js.Close()
		return nil, errorString("cannot create a JavaScript context")
	}
	if err := js.addGlobals(); err != nil {
		js.Close()
		return nil, err
	}
	return js, nil
}

// globals adds what rquickjs's `Context::full` (quickjs-ng) has and this engine lacks; it is called with
// the native performance.now. `DisposableStack` is left out.
const globals = `(function (now) {
  const define = (o, k, v) => Object.defineProperty(o, k, {value: v, writable: true, configurable: true, enumerable: false});
  define(globalThis, "performance", {now, timeOrigin: Date.now() - now()});
  define(globalThis, "queueMicrotask", function queueMicrotask(callback) {
    if (typeof callback !== "function") throw new TypeError("not a function");
    Promise.resolve().then(() => { callback(); });
  });
  define(Array, "fromAsync", async function fromAsync(items, mapfn = undefined, thisArg = undefined) {
    if (mapfn !== undefined && typeof mapfn !== "function") throw new TypeError("not a function");
    if (items == null) throw new TypeError("not an object");
    const out = [];
    if (items[Symbol.asyncIterator] != null || items[Symbol.iterator] != null) {
      let i = 0;
      for await (const v of items) out.push(mapfn ? await mapfn.call(thisArg, v, i++) : v);
    } else {
      const o = Object(items), n = Math.min(Math.max(Math.trunc(Number(o.length)) || 0, 0), Number.MAX_SAFE_INTEGER);
      for (let i = 0; i < n; i++) {
        const v = await o[i];
        out.push(mapfn ? await mapfn.call(thisArg, v, i) : v);
      }
    }
    return out;
  });
  define(Error, "captureStackTrace", function captureStackTrace(object) {
    if (Object(object) !== object) throw new TypeError("not an object");
    const stack = new Error().stack;
    define(object, "stack", stack.slice(stack.indexOf("\n") + 1));
  });
  define(Error, "stackTraceLimit", 10);
  for (const k of ["dispose", "asyncDispose"]) Object.defineProperty(Symbol, k, {value: Symbol("Symbol." + k)});
})`

// addGlobals runs globals.
func (js *JS) addGlobals() error {
	f, err := js.Eval(globals, "native")
	if err != nil {
		return err
	}
	defer js.Free(f)
	name := js.cString("now")
	defer libc.Xfree(js.tls, name)
	now := lib.XJS_NewCFunction2(js.tls, js.ctx, fp(perfNow), name, 0, lib.EJS_CFUNC_generic, 0)
	r, err := js.call(f.v, undefined(), []lib.TJSValue{now})
	js.Free(r)
	return err
}

// perfNow is performance.now: milliseconds since the runtime was made, on the monotonic clock.
func perfNow(tls *libc.TLS, ctx uintptr, _ lib.TJSValue, _ int32, _ uintptr) lib.TJSValue {
	g := (*guard)(cptr(lib.XJS_GetRuntimeOpaque(tls, lib.XJS_GetRuntime(tls, ctx))))
	v := lib.TJSValue{Ftag: lib.EJS_TAG_FLOAT64}
	v.Fu.Fuint641 = math.Float64bits(float64(since()-g.start) / 1e6)
	return v
}

// Close frees the runtime. Every Value taken from it must be freed first.
func (js *JS) Close() {
	if js.rt == 0 {
		return
	}
	if js.ctx != 0 {
		lib.XJS_FreeContext(js.tls, js.ctx)
	}
	lib.XJS_FreeRuntime(js.tls, js.rt)
	if js.guard != 0 {
		libc.Xfree(js.tls, js.guard)
	}
	if js.tracker != 0 {
		libc.Xfree(js.tls, js.tracker)
	}
	js.tls.Close()
	*js = JS{}
}

func (js *JS) g() *guard { return (*guard)(cptr(js.guard)) }

// Arm sets the deadline after which running JavaScript is interrupted (compared on the monotonic clock, so
// pass a time from time.Now); the zero time disarms it.
func (js *JS) Arm(deadline time.Time) {
	d := int64(0)
	if !deadline.IsZero() {
		d = max(int64(deadline.Sub(epoch)), 1)
	}
	atomic.StoreInt64(&js.g().deadline, d)
}

// Expired reports whether the armed deadline has passed.
func (js *JS) Expired() bool {
	d := atomic.LoadInt64(&js.g().deadline)
	return d != 0 && since() > d
}

// Cancel interrupts running JavaScript at its next poll and every later call. It may be called from any
// goroutine while the runtime is open.
func (js *JS) Cancel() { atomic.StoreInt32(&js.g().cancel, 1) }

// Canceled reports whether Cancel was called.
func (js *JS) Canceled() bool { return atomic.LoadInt32(&js.g().cancel) != 0 }

// cString is s as a NUL-terminated C string the caller frees.
func (js *JS) cString(s string) uintptr {
	p, err := libc.CString(s)
	if err != nil {
		panic("script: out of memory")
	}
	return p
}

// Eval runs src as sloppy-mode global code named file and returns its completion value. A thrown value is
// returned as an *Exception.
func (js *JS) Eval(src, file string) (Value, error) {
	code, name := js.cString(src), js.cString(file)
	defer libc.Xfree(js.tls, code)
	defer libc.Xfree(js.tls, name)
	v := lib.XJS_Eval(js.tls, js.ctx, code, libc.Tsize_t(len(src)), name, lib.MJS_EVAL_TYPE_GLOBAL)
	if v.Ftag == lib.EJS_TAG_EXCEPTION {
		return Value{}, js.exception()
	}
	return Value{v}, nil
}

// Exec is Eval with the completion value dropped.
func (js *JS) Exec(src, file string) error {
	v, err := js.Eval(src, file)
	js.Free(v)
	return err
}

// Free releases v.
func (js *JS) Free(v Value) { lib.XFreeValue(js.tls, js.ctx, v.v) }

// IsFunction reports whether v can be called.
func (js *JS) IsFunction(v Value) bool { return lib.XJS_IsFunction(js.tls, js.ctx, v.v) != 0 }

// TypeName is rquickjs's name for v's type, as its conversion errors print it.
func (js *JS) TypeName(v Value) string { return js.typeName(v.v) }

// Call calls f with this undefined and string arguments; a thrown value is returned as an *Exception.
func (js *JS) Call(f Value, args ...string) (Value, error) {
	if lib.XJS_IsFunction(js.tls, js.ctx, f.v) == 0 {
		return Value{}, &ConvError{From: js.typeName(f.v), To: "function"}
	}
	vals := make([]lib.TJSValue, len(args))
	for i, a := range args {
		vals[i] = js.newString(a)
	}
	return js.call(f.v, undefined(), vals)
}

// call calls f with args, which it frees.
func (js *JS) call(f, this lib.TJSValue, args []lib.TJSValue) (Value, error) {
	var argv uintptr
	if len(args) > 0 {
		argv = libc.Xmalloc(js.tls, libc.Tsize_t(unsafe.Sizeof(lib.TJSValue{})*uintptr(len(args))))
		if argv == 0 {
			panic("script: out of memory")
		}
		putValues(argv, args...)
	}
	r := lib.XJS_Call(js.tls, js.ctx, f, this, int32(len(args)), argv)
	for _, a := range args {
		lib.XFreeValue(js.tls, js.ctx, a)
	}
	if argv != 0 {
		libc.Xfree(js.tls, argv)
	}
	if r.Ftag == lib.EJS_TAG_EXCEPTION {
		return Value{}, js.exception()
	}
	return Value{r}, nil
}

// putValues writes vals to the C array at argv. One at a time: copying a slice literal into the array made
// the Go 1.25 compiler crash (disjointTypes).
func putValues(argv uintptr, vals ...lib.TJSValue) {
	dst := unsafe.Slice((*lib.TJSValue)(cptr(argv)), len(vals))
	for i, v := range vals {
		dst[i] = v
	}
}

func (js *JS) newString(s string) lib.TJSValue {
	p := js.cString(s)
	defer libc.Xfree(js.tls, p)
	return lib.XJS_NewStringLen(js.tls, js.ctx, p, libc.Tsize_t(len(s)))
}

// String is v as a Go string when v is a JS string, otherwise a *ConvError (rquickjs's `FromJs<String>`).
func (js *JS) String(v Value) (string, error) {
	if !isString(v.v) {
		return "", &ConvError{From: js.typeName(v.v), To: "string"}
	}
	s, _ := js.toString(v.v)
	return s, nil
}

func isString(v lib.TJSValue) bool {
	return v.Ftag == lib.EJS_TAG_STRING || v.Ftag == lib.EJS_TAG_STRING_ROPE
}

// toString is JS ToString(v); false when it throws (the exception is dropped).
func (js *JS) toString(v lib.TJSValue) (string, bool) {
	n := libc.Xmalloc(js.tls, libc.Tsize_t(unsafe.Sizeof(libc.Tsize_t(0))))
	if n == 0 {
		panic("script: out of memory")
	}
	defer libc.Xfree(js.tls, n)
	p := lib.XJS_ToCStringLen2(js.tls, js.ctx, n, v, 0)
	if p == 0 {
		lib.XFreeValue(js.tls, js.ctx, lib.XJS_GetException(js.tls, js.ctx))
		return "", false
	}
	defer lib.XJS_FreeCString(js.tls, js.ctx, p)
	return string(libc.GoBytes(p, int(*(*libc.Tsize_t)(cptr(n))))), true
}

// typeName is rquickjs's name for v's type, as its conversion errors print it.
func (js *JS) typeName(v lib.TJSValue) string {
	switch v.Ftag {
	case lib.EJS_TAG_UNDEFINED:
		return "undefined"
	case lib.EJS_TAG_NULL:
		return "null"
	case lib.EJS_TAG_BOOL:
		return "bool"
	case lib.EJS_TAG_INT:
		return "int"
	case lib.EJS_TAG_FLOAT64:
		return "float"
	case lib.EJS_TAG_STRING, lib.EJS_TAG_STRING_ROPE:
		return "string"
	case lib.EJS_TAG_SYMBOL:
		return "symbol"
	case lib.EJS_TAG_BIG_INT, lib.EJS_TAG_SHORT_BIG_INT:
		return "big_int"
	case lib.EJS_TAG_OBJECT:
		switch {
		case lib.XJS_IsFunction(js.tls, js.ctx, v) != 0:
			return "function"
		case lib.XJS_IsArray(js.tls, js.ctx, v) > 0:
			return "array"
		}
		return "object"
	}
	return "unknown"
}

// strProp is v's property k when it is a string.
func (js *JS) strProp(v lib.TJSValue, k string) (string, bool) {
	name := js.cString(k)
	defer libc.Xfree(js.tls, name)
	p := lib.XJS_GetPropertyStr(js.tls, js.ctx, v, name)
	defer lib.XFreeValue(js.tls, js.ctx, p)
	if p.Ftag == lib.EJS_TAG_EXCEPTION {
		lib.XFreeValue(js.tls, js.ctx, lib.XJS_GetException(js.tls, js.ctx))
		return "", false
	}
	if !isString(p) {
		return "", false
	}
	return js.toString(p)
}

// coercedProp is v's property k as rquickjs's `Option<Coerced<String>>` reads it: JS ToString of the value,
// "" when it is undefined or null or ToString throws.
func (js *JS) coercedProp(v lib.TJSValue, k string) string {
	name := js.cString(k)
	defer libc.Xfree(js.tls, name)
	p := lib.XJS_GetPropertyStr(js.tls, js.ctx, v, name)
	defer lib.XFreeValue(js.tls, js.ctx, p)
	switch p.Ftag {
	case lib.EJS_TAG_EXCEPTION:
		lib.XFreeValue(js.tls, js.ctx, lib.XJS_GetException(js.tls, js.ctx))
		return ""
	case lib.EJS_TAG_UNDEFINED, lib.EJS_TAG_NULL:
		return ""
	}
	s, _ := js.toString(p)
	return s
}

// RunJob runs one pending job (a promise reaction or an async continuation): false when there was none. A
// job that throws returns its exception.
func (js *JS) RunJob() (bool, error) {
	pctx := libc.Xcalloc(js.tls, 1, libc.Tsize_t(unsafe.Sizeof(uintptr(0))))
	if pctx == 0 {
		panic("script: out of memory")
	}
	defer libc.Xfree(js.tls, pctx)
	switch r := lib.XJS_ExecutePendingJob(js.tls, js.rt, pctx); {
	case r == 0:
		return false, nil
	case r < 0:
		return true, js.exception()
	}
	return true, nil
}

// HeapSize is the bytes the runtime has allocated (QuickJS's malloc_size).
func (js *JS) HeapSize() int64 {
	var u lib.TJSMemoryUsage
	p := libc.Xcalloc(js.tls, 1, libc.Tsize_t(unsafe.Sizeof(u)))
	if p == 0 {
		panic("script: out of memory")
	}
	defer libc.Xfree(js.tls, p)
	lib.XJS_ComputeMemoryUsage(js.tls, js.rt, p)
	return int64((*lib.TJSMemoryUsage)(cptr(p)).Fmalloc_size)
}

// TrackRejections makes every promise rejection, and every later handling of one, call the global
// function name(promise, reason, handled) when it is defined.
func (js *JS) TrackRejections(name string) {
	if js.tracker != 0 {
		libc.Xfree(js.tls, js.tracker)
	}
	js.tracker = js.cString(name)
	lib.XJS_SetHostPromiseRejectionTracker(js.tls, js.rt, fp(track), js.tracker)
}

func track(tls *libc.TLS, ctx uintptr, promise, reason lib.TJSValue, handled int32, name uintptr) {
	global := lib.XJS_GetGlobalObject(tls, ctx)
	defer lib.XFreeValue(tls, ctx, global)
	f := lib.XJS_GetPropertyStr(tls, ctx, global, name)
	defer lib.XFreeValue(tls, ctx, f)
	if lib.XJS_IsFunction(tls, ctx, f) == 0 {
		if f.Ftag == lib.EJS_TAG_EXCEPTION {
			lib.XFreeValue(tls, ctx, lib.XJS_GetException(tls, ctx))
		}
		return
	}
	argv := libc.Xmalloc(tls, libc.Tsize_t(unsafe.Sizeof(lib.TJSValue{})*3))
	if argv == 0 {
		return
	}
	defer libc.Xfree(tls, argv)
	putValues(argv, promise, reason, jsBool(handled != 0))
	r := lib.XJS_Call(tls, ctx, f, undefined(), 3, argv)
	if r.Ftag == lib.EJS_TAG_EXCEPTION {
		lib.XFreeValue(tls, ctx, lib.XJS_GetException(tls, ctx))
		return
	}
	lib.XFreeValue(tls, ctx, r)
}

// Exception is a thrown value.
type Exception struct {
	// IsError: the value is an Error object, and Name, Message and Stack are its properties ("" when not
	// strings; Name is then "Error").
	IsError              bool
	Name, Message, Stack string
	// Text is a thrown non-Error value as JS ToString makes it ("" when that throws).
	Text string
}

func (e *Exception) Error() string {
	if !e.IsError {
		return e.Text
	}
	return e.Name + ": " + e.Message
}

// exception takes the pending exception.
func (js *JS) exception() *Exception {
	v := lib.XJS_GetException(js.tls, js.ctx)
	defer lib.XFreeValue(js.tls, js.ctx, v)
	if lib.XJS_IsError(js.tls, js.ctx, v) == 0 {
		s, _ := js.toString(v)
		return &Exception{Text: s}
	}
	e := &Exception{IsError: true, Name: "Error"}
	if s, ok := js.strProp(v, "name"); ok {
		e.Name = s
	}
	e.Message = js.coercedProp(v, "message")
	e.Stack = js.coercedProp(v, "stack")
	ngCompat(e)
	return e
}

// ngCompat words an error as quickjs-ng (rquickjs's engine) does where this engine words it differently,
// and keeps the first maxFrames frames of its stack, as quickjs-ng does.
func ngCompat(e *Exception) {
	switch {
	case e.Name == "InternalError" && e.Message == "stack overflow":
		e.Name, e.Message = "RangeError", "Maximum call stack size exceeded"
	case e.Name == "ReferenceError" && strings.HasPrefix(e.Message, "'"):
		for _, tail := range []string{"' is not defined", "' is not initialized"} {
			if rest, ok := strings.CutSuffix(e.Message[1:], tail); ok {
				e.Message = rest + tail[1:]
			}
		}
	}
	lines := strings.SplitAfter(e.Stack, "\n")
	if len(lines) > maxFrames && lines[maxFrames] != "" {
		e.Stack = strings.Join(lines[:maxFrames], "")
	}
}

// ConvError is rquickjs's conversion error: a value was not of the wanted type.
type ConvError struct{ From, To string }

func (e *ConvError) Error() string {
	return "Error converting from js '" + e.From + "' into type '" + e.To + "'"
}

type errorString string

func (e errorString) Error() string { return string(e) }

// parseUsize is Rust's `str::parse::<usize>`: decimal digits with an optional leading `+`.
func parseUsize(s string) (int, bool) {
	s = strings.TrimPrefix(s, "+")
	if s == "" || s[0] == '+' || s[0] == '-' {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 63)
	return int(n), err == nil
}
