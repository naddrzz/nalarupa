package script

// The JavaScript engine: quickjs-ng (package qjs, the engine rquickjs bundles) behind the few calls script nodes and
// the execute sandbox need. One JS is one runtime with one context, confined to the goroutine that made it; only
// Cancel may be called from another goroutine.

import (
	"strconv"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/script/qjs"
)

// Limits are the caps of one runtime.
type Limits struct {
	// Memory caps the JS heap, in bytes.
	Memory int
	// Stack caps the C stack JavaScript may use, in bytes.
	Stack int
}

// JS is one quickjs-ng runtime and its context.
type JS struct{ rt *qjs.Runtime }

// Value is a JS value owned by the caller; Free releases it.
type Value struct{ v qjs.Value }

// NewJS is a fresh runtime with limits l, no deadline and the standard globals of rquickjs's `Context::full`.
func NewJS(l Limits) (*JS, error) {
	rt, err := qjs.New(l.Memory, l.Stack)
	if err != nil {
		return nil, err
	}
	return &JS{rt: rt}, nil
}

// Close frees the runtime. Every Value taken from it must be freed first.
func (js *JS) Close() {
	if js.rt != nil {
		js.rt.Close()
		js.rt = nil
	}
}

// Arm sets the deadline after which running JavaScript is interrupted (compared on the monotonic clock, so pass a
// time from time.Now); the zero time disarms it.
func (js *JS) Arm(deadline time.Time) {
	d := int64(0)
	if !deadline.IsZero() {
		d = max(qjs.Now()+int64(time.Until(deadline)), 1)
	}
	js.rt.Arm(d)
}

// Expired reports whether the armed deadline has passed.
func (js *JS) Expired() bool { return js.rt.Expired() }

// Cancel interrupts running JavaScript at its next poll and every later call. It may be called from any goroutine
// while the runtime is open.
func (js *JS) Cancel() { js.rt.Cancel() }

// Canceled reports whether Cancel was called.
func (js *JS) Canceled() bool { return js.rt.Canceled() }

// Eval runs src as sloppy-mode global code named file and returns its completion value. A thrown value is returned
// as an *Exception.
func (js *JS) Eval(src, file string) (Value, error) {
	return js.result(js.rt.Eval(src, file, false))
}

// ExecStrict runs src as rquickjs's `Ctx::eval` does: strict-mode global code named eval_script, the completion
// value dropped.
func (js *JS) ExecStrict(src string) error {
	v, err := js.result(js.rt.Eval(src, "eval_script", true))
	js.Free(v)
	return err
}

// Exec is Eval with the completion value dropped.
func (js *JS) Exec(src, file string) error {
	v, err := js.Eval(src, file)
	js.Free(v)
	return err
}

// result is v, or the pending exception when v is the exception marker.
func (js *JS) result(v qjs.Value) (Value, error) {
	if v.Tag() == qjs.TagException {
		return Value{}, js.exception()
	}
	return Value{v}, nil
}

// Free releases v; freeing the zero Value does nothing.
func (js *JS) Free(v Value) { js.rt.Free(v.v) }

// IsFunction reports whether v can be called.
func (js *JS) IsFunction(v Value) bool { return js.rt.IsFunction(v.v) }

// TypeName is rquickjs's name for v's type, as its conversion errors print it.
func (js *JS) TypeName(v Value) string { return js.typeName(v.v) }

// Call calls f with this undefined and string arguments; a thrown value is returned as an *Exception.
func (js *JS) Call(f Value, args ...string) (Value, error) {
	if !js.rt.IsFunction(f.v) {
		return Value{}, &ConvError{From: js.typeName(f.v), To: "function"}
	}
	vals := make([]qjs.Value, len(args))
	for i, a := range args {
		vals[i] = js.rt.NewString(a)
	}
	// Freed even when a native's panic comes back through Call: a leaked value fails the runtime's free.
	defer func() {
		for _, v := range vals {
			js.rt.Free(v)
		}
	}()
	return js.result(js.rt.Call(f.v, qjs.Undefined(), vals...))
}

// String is v as a Go string when v is a JS string, otherwise a *ConvError (rquickjs's `FromJs<String>`).
func (js *JS) String(v Value) (string, error) {
	if !isString(v.v) {
		return "", &ConvError{From: js.typeName(v.v), To: "string"}
	}
	s, _ := js.toString(v.v)
	return s, nil
}

func isString(v qjs.Value) bool {
	t := v.Tag()
	return t == qjs.TagString || t == qjs.TagStringRope
}

// toString is JS ToString(v); false when it throws (the exception is dropped).
func (js *JS) toString(v qjs.Value) (string, bool) {
	s, ok := js.rt.ToString(v)
	if !ok {
		js.rt.Free(js.rt.Exception())
	}
	return s, ok
}

// typeName is rquickjs's name for v's type, as its conversion errors print it.
func (js *JS) typeName(v qjs.Value) string {
	switch v.Tag() {
	case qjs.TagUndefined:
		return "undefined"
	case qjs.TagNull:
		return "null"
	case qjs.TagBool:
		return "bool"
	case qjs.TagInt:
		return "int"
	case qjs.TagFloat64:
		return "float"
	case qjs.TagString, qjs.TagStringRope:
		return "string"
	case qjs.TagSymbol:
		return "symbol"
	case qjs.TagBigInt, qjs.TagShortBigInt:
		return "big_int"
	case qjs.TagObject:
		switch {
		case js.rt.IsFunction(v):
			return "function"
		case qjs.IsArray(v):
			return "array"
		}
		return "object"
	}
	return "unknown"
}

// prop is v's property k; ok is false when reading it throws (the exception is dropped).
func (js *JS) prop(v qjs.Value, k string) (p qjs.Value, ok bool) {
	p = js.rt.Get(v, k)
	if p.Tag() == qjs.TagException {
		js.rt.Free(js.rt.Exception())
		return p, false
	}
	return p, true
}

// strProp is v's property k when it is a string.
func (js *JS) strProp(v qjs.Value, k string) (string, bool) {
	p, ok := js.prop(v, k)
	if !ok {
		return "", false
	}
	defer js.rt.Free(p)
	if !isString(p) {
		return "", false
	}
	return js.toString(p)
}

// coercedProp is v's property k as rquickjs's `Option<Coerced<String>>` reads it: JS ToString of the value, ""
// when it is undefined or null or ToString throws.
func (js *JS) coercedProp(v qjs.Value, k string) string {
	p, ok := js.prop(v, k)
	if !ok {
		return ""
	}
	defer js.rt.Free(p)
	if t := p.Tag(); t == qjs.TagUndefined || t == qjs.TagNull {
		return ""
	}
	s, _ := js.toString(p)
	return s
}

// RunJob runs one pending job (a promise reaction or an async continuation): false when there was none. A job that
// throws returns its exception.
func (js *JS) RunJob() (bool, error) {
	switch js.rt.RunJob() {
	case 0:
		return false, nil
	case -1:
		return true, js.exception()
	}
	return true, nil
}

// HeapSize is the bytes the runtime has allocated (quickjs-ng's malloc_size).
func (js *JS) HeapSize() int64 { return js.rt.Heap() }

// TrackRejections makes every promise rejection, and every later handling of one, call the global function
// name(promise, reason, handled) when it is defined.
func (js *JS) TrackRejections(name string) {
	if err := js.rt.TrackRejections(name); err != nil {
		panic("script: " + err.Error())
	}
}

// Exception is a thrown value.
type Exception struct {
	// IsError: the value is an Error object, and Name, Message and Stack are its properties ("" when not strings;
	// Name is then "Error").
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

// exception takes the pending exception and reads it as rquickjs does.
func (js *JS) exception() *Exception {
	v := js.rt.Exception()
	defer js.rt.Free(v)
	return js.read(v)
}

// read is a thrown value as rquickjs reads it: an Error's name, message and stack, or another value's text.
func (js *JS) read(v qjs.Value) *Exception {
	if !qjs.IsError(v) {
		s, _ := js.toString(v)
		return &Exception{Text: s}
	}
	e := &Exception{IsError: true, Name: "Error"}
	if s, ok := js.strProp(v, "name"); ok {
		e.Name = s
	}
	e.Message = js.coercedProp(v, "message")
	e.Stack = js.coercedProp(v, "stack")
	return e
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

// Native is a Go function JavaScript calls by a global name: its arguments as JS ToString makes them (rquickjs's
// `Rest<Coerced<String>>`), its result a string. An error is thrown as a new Error with its text as the message.
type Native = qjs.Native

// Define makes the global name call f, as rquickjs's `Function::new` set on the globals.
func (js *JS) Define(name string, f Native) error { return js.rt.Define(name, f) }

// Global is the global property name.
func (js *JS) Global(name string) (Value, error) { return js.result(js.rt.Global(name)) }

// Nullish reports whether v is undefined or null.
func (js *JS) Nullish(v Value) bool {
	t := v.v.Tag()
	return t == qjs.TagUndefined || t == qjs.TagNull
}

// Index is v[i]; a throwing getter returns its exception.
func (js *JS) Index(v Value, i int) (Value, error) {
	return js.result(js.rt.Get(v.v, strconv.Itoa(i)))
}

// IsArray reports whether v is an Array.
func (js *JS) IsArray(v Value) bool { return qjs.IsArray(v.v) }

// Thrown is v read as a thrown value, as rquickjs reads the exception after `ctx.throw(v)`.
func (js *JS) Thrown(v Value) *Exception { return js.read(v.v) }
