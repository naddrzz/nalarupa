package script

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func newJS(t *testing.T) *JS {
	t.Helper()
	js, err := NewJS(Limits{Memory: 16 << 20, StackSlots: stackSlots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(js.Close)
	return js
}

func exception(t *testing.T, err error) *Exception {
	t.Helper()
	var ex *Exception
	if !errors.As(err, &ex) {
		t.Fatalf("not an exception: %v", err)
	}
	return ex
}

func evalString(t *testing.T, js *JS, src string) string {
	t.Helper()
	v, err := js.Eval(src, "input")
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	defer js.Free(v)
	s, err := js.String(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEvalIsSloppyGlobalCode(t *testing.T) {
	js := newJS(t)
	if got := evalString(t, js, "x = 4; var y = 'a'; String(x) + y + typeof console + typeof print"); got != "4aundefinedundefined" {
		t.Fatalf("%q", got)
	}
	ex := exception(t, js.Exec("const a = 1;\nnull.boom", "input"))
	if !ex.IsError || ex.Name != "TypeError" || ex.Message != "cannot read property 'boom' of null" || !strings.HasPrefix(ex.Stack, "    at <eval> (input:2:") {
		t.Fatalf("%+v", ex)
	}
	if ex := exception(t, js.Exec("undefinedName", "input")); ex.Message != "undefinedName is not defined" {
		t.Fatalf("quickjs-ng wording: %+v", ex)
	}
	if ex := exception(t, js.Exec("throw 7", "input")); ex.IsError || ex.Text != "7" {
		t.Fatalf("%+v", ex)
	}
	v, err := js.Eval("({})", "input")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.String(v); err == nil || err.Error() != "Error converting from js 'object' into type 'string'" {
		t.Fatal(err)
	}
	if _, err := js.Call(v); err == nil || err.Error() != "Error converting from js 'object' into type 'function'" {
		t.Fatal(err)
	}
	js.Free(v)
}

// The globals rquickjs's Context::full has beyond this engine's.
func TestQuickJSNGGlobals(t *testing.T) {
	js := newJS(t)
	got := evalString(t, js, `[typeof performance, typeof performance.now, typeof performance.timeOrigin, typeof queueMicrotask,
		typeof Array.fromAsync, Array.fromAsync.length, typeof Error.captureStackTrace, Error.stackTraceLimit,
		typeof Symbol.dispose, typeof Symbol.asyncDispose, Symbol.dispose === Symbol.asyncDispose,
		Object.keys(globalThis).length].join(",")`)
	if got != "object,function,number,function,function,1,function,10,symbol,symbol,false,0" {
		t.Fatalf("%q", got)
	}
	// performance.now is monotonic milliseconds since the runtime was made.
	time.Sleep(20 * time.Millisecond)
	if got := evalString(t, js, `const a = performance.now(), b = performance.now(); String(a >= 20 && a < 10000 && b >= a)`); got != "true" {
		t.Fatalf("performance.now: %s", got)
	}
	if err := js.Exec(`var log = []; queueMicrotask(() => log.push("micro"));
Array.fromAsync([1, Promise.resolve(2)], x => x * 10).then(a => log.push(a.join("+")));
Array.fromAsync((async function* () { yield 3; yield 4 })()).then(a => log.push(a.join("+")));
Array.fromAsync({length: 2, 0: "a", 1: "b"}).then(a => log.push(a.join("+")));
const holder = {}; (function where() { Error.captureStackTrace(holder) })();`, "input"); err != nil {
		t.Fatal(err)
	}
	for {
		ran, err := js.RunJob()
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	if got := evalString(t, js, `log.slice().sort().join(",") + "|" + holder.stack.split("\n")[0]`); !strings.HasPrefix(got, "10+20,3+4,a+b,micro|    at where (input:5:") {
		t.Fatalf("%q", got)
	}
	if ex := exception(t, js.Exec("queueMicrotask(1)", "input")); ex.Name != "TypeError" {
		t.Fatalf("%+v", ex)
	}
}

// rquickjs reads message and stack coerced to strings; name must be a string.
func TestExceptionCoercesMessageAndStack(t *testing.T) {
	js := newJS(t)
	ex := exception(t, js.Exec("const e = new Error('m'); e.message = 42; e.stack = {toString() { return 'S' }}; e.name = 7; throw e", "input"))
	if ex.Name != "Error" || ex.Message != "42" || ex.Stack != "S" {
		t.Fatalf("%+v", ex)
	}
	ex = exception(t, js.Exec("const f = new TypeError('m'); f.message = null; f.stack = undefined; throw f", "input"))
	if ex.Name != "TypeError" || ex.Message != "" || ex.Stack != "" {
		t.Fatalf("%+v", ex)
	}
}

func TestCallPassesStrings(t *testing.T) {
	js := newJS(t)
	f, err := js.Eval("(function (a, b) { return a + '|' + b.length })", "input")
	if err != nil {
		t.Fatal(err)
	}
	defer js.Free(f)
	r, err := js.Call(f, "é\x00x", strings.Repeat("z", 70000))
	if err != nil {
		t.Fatal(err)
	}
	defer js.Free(r)
	if s, _ := js.String(r); s != "é\x00x|70000" {
		t.Fatalf("%q", s)
	}
}

func TestDeadlineAndCancel(t *testing.T) {
	js := newJS(t)
	// Not armed: compiling and running are not timed.
	if err := js.Exec("let n = 0; for (let i = 0; i < 1e5; i++) n++", "input"); err != nil {
		t.Fatal(err)
	}
	js.Arm(time.Now().Add(200 * time.Millisecond))
	started := time.Now()
	// The interrupt is uncatchable, so a try/catch around the loop cannot swallow it.
	ex := exception(t, js.Exec("while (true) { try { while (true) {} } catch (err) {} }", "input"))
	if ex.Name != "InternalError" || ex.Message != "interrupted" || !js.Expired() || time.Since(started) > 2*time.Second {
		t.Fatalf("%+v %v", ex, time.Since(started))
	}
	js.Arm(time.Time{})
	if js.Expired() {
		t.Fatal("disarmed")
	}

	js2 := newJS(t)
	canceled := make(chan struct{})
	go func() {
		defer close(canceled)
		time.Sleep(100 * time.Millisecond)
		js2.Cancel()
	}()
	ex = exception(t, js2.Exec("for (;;) {}", "input"))
	<-canceled // Cancel may only be called while the runtime is open
	if ex.Message != "interrupted" || !js2.Canceled() {
		t.Fatalf("%+v", ex)
	}
}

func TestJobsAndRejections(t *testing.T) {
	js := newJS(t)
	js.TrackRejections("__track")
	if err := js.Exec(`var log = []; var rejected = new Map();
function __track(p, reason, handled) { if (handled) rejected.delete(p); else rejected.set(p, reason); }
(async () => { log.push("before"); await null; log.push("after") })();
Promise.resolve().then(() => log.push("then"));
const p = Promise.reject(new Error("late")); p.catch(() => log.push("caught"));
Promise.reject(new Error("left"));`, "input"); err != nil {
		t.Fatal(err)
	}
	for {
		ran, err := js.RunJob()
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	if got := evalString(t, js, `log.join(",") + "|" + [...rejected.values()].map(e => e.message).join(",")`); got != "before,after,then,caught|left" {
		t.Fatalf("%q", got)
	}
	// A job that throws returns its exception.
	if err := js.Exec(`Promise.resolve().then(() => { throw new Error("in job") })`, "input"); err != nil {
		t.Fatal(err)
	}
	if _, err := js.RunJob(); err != nil {
		t.Fatalf("a throwing reaction rejects its promise: %v", err)
	}
	// A self-feeding job loop never drains: the caller checks the deadline between jobs (as settle does).
	if err := js.Exec("const f = () => { Promise.resolve().then(f) }; f()", "input"); err != nil {
		t.Fatal(err)
	}
	js.Arm(time.Now().Add(100 * time.Millisecond))
	started := time.Now()
	for !js.Expired() {
		if ran, err := js.RunJob(); err != nil || !ran {
			t.Fatalf("the queue drained: %v", err)
		}
	}
	if time.Since(started) > time.Second {
		t.Fatal("deadline missed")
	}
}

func TestMemoryAndStack(t *testing.T) {
	js := newJS(t)
	before := js.HeapSize()
	if err := js.Exec("var keep = new Array(200000).fill(1.5)", "input"); err != nil {
		t.Fatal(err)
	}
	if after := js.HeapSize(); after < before+1<<20 {
		t.Fatalf("heap %d -> %d", before, after)
	}
	ex := exception(t, js.Exec("const a = []\nfor (;;) a.push(new Array(100000).fill(1.5))", "input"))
	if ex.Name != "InternalError" || ex.Message != "out of memory" {
		t.Fatalf("%+v", ex)
	}
	ex = exception(t, newJS(t).Exec("function f(n) { return f(n + 1) + 1 }\nf(0)", "input"))
	if ex.Name != "RangeError" || ex.Message != "Maximum call stack size exceeded" || strings.Count(ex.Stack, "\n") != maxFrames {
		t.Fatalf("%+v", ex)
	}
}
