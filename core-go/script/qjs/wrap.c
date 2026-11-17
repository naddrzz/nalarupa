// The C side of package qjs (see wrap.h). Every entry from Go into JavaScript first moves the stack top to the
// current stack pointer, as rquickjs does before each eval, call and job with its `parallel` feature: the stack
// limit is then measured from where this call entered C.
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include "cutils.h"
#include "wrap.h"
#include "_cgo_export.h"

struct qjs_guard {
    _Atomic int cancel;
    // deadline is the qjs_now time past which running JavaScript is interrupted; 0: none.
    _Atomic int64_t deadline;
    // tracker is the global the rejection tracker calls, or NULL.
    char *tracker;
    // go is the cgo.Handle of the Go Runtime natives call back into; fn_class is the class of native functions.
    uintptr_t go;
    JSClassID fn_class;
};

void qjs_set_go(qjs_guard *g, uintptr_t h) { g->go = h; }

qjs_guard *qjs_guard_new(void) { return calloc(1, sizeof(qjs_guard)); }

void qjs_guard_free(qjs_guard *g) {
    if (g) {
        free(g->tracker);
        free(g);
    }
}

// qjs_now is quickjs-ng's monotonic clock (performance.now's), in nanoseconds.
int64_t qjs_now(void) { return (int64_t)js__hrtime_ns(); }

void qjs_arm(qjs_guard *g, int64_t deadline) { atomic_store(&g->deadline, deadline); }

int qjs_expired(qjs_guard *g) {
    int64_t d = atomic_load(&g->deadline);
    return d != 0 && qjs_now() > d;
}

void qjs_cancel(qjs_guard *g) { atomic_store(&g->cancel, 1); }

int qjs_canceled(qjs_guard *g) { return atomic_load(&g->cancel); }

static int interrupt(JSRuntime *rt, void *opaque) {
    qjs_guard *g = opaque;
    return qjs_canceled(g) || qjs_expired(g);
}

// native_call is the call hook of the native function class, as rquickjs's `RustFunction` class has one: a call
// pushes no stack frame of its own (so stacks match rquickjs's) and goes to the Go native at the object's index.
static JSValue native_call(JSContext *ctx, JSValueConst func, JSValueConst this_val, int argc, JSValueConst *argv,
                           int flags) {
    qjs_guard *g = JS_GetRuntimeOpaque(JS_GetRuntime(ctx));
    int index = (int)(uintptr_t)JS_GetOpaque(func, g->fn_class) - 1;
    return qjsGoNative(ctx, g->go, index, argc, (JSValue *)argv);
}

// qjs_new is rquickjs's `Runtime::new` (which registers three classes of its own; natives are instances of the
// second, `RustFunction`) followed by what script.rs sets on it: the memory and stack limits, the interrupt handler,
// then `Context::full` (JS_NewContext). The guard is the runtime's opaque.
JSContext *qjs_new(size_t memory, size_t stack, qjs_guard *g) {
    JSRuntime *rt = JS_NewRuntime();
    if (!rt)
        return NULL;
    JS_SetDumpFlags(rt, 0);
    static const char *const classes[] = {"RustClass", "RustFunction", "RustExotic"};
    JSClassID ids[3] = {0};
    for (int i = 0; i < 3; i++) {
        JS_NewClassID(rt, &ids[i]);
    }
    for (int i = 0; i < 3; i++) {
        JSClassDef def = {.class_name = classes[i], .call = i == 1 ? native_call : NULL};
        if (JS_NewClass(rt, ids[i], &def)) {
            JS_FreeRuntime(rt);
            return NULL;
        }
    }
    g->fn_class = ids[1];
    JS_SetRuntimeOpaque(rt, g);
    JS_SetMemoryLimit(rt, memory);
    JS_SetMaxStackSize(rt, stack);
    JS_SetInterruptHandler(rt, interrupt, g);
    JSContext *ctx = JS_NewContext(rt);
    if (!ctx)
        JS_FreeRuntime(rt);
    return ctx;
}

void qjs_free(JSContext *ctx) {
    JSRuntime *rt = JS_GetRuntime(ctx);
    JS_FreeContext(ctx);
    JS_FreeRuntime(rt);
}

JSValue qjs_eval(JSContext *ctx, const char *src, size_t len, const char *file, int flags) {
    JS_UpdateStackTop(JS_GetRuntime(ctx));
    return JS_Eval(ctx, src, len, file, flags);
}

JSValue qjs_call(JSContext *ctx, JSValue f, JSValue this_obj, int argc, JSValue *argv) {
    JS_UpdateStackTop(JS_GetRuntime(ctx));
    return JS_Call(ctx, f, this_obj, argc, argv);
}

int qjs_run_job(JSContext *ctx) {
    JSRuntime *rt = JS_GetRuntime(ctx);
    JSContext *job;
    JS_UpdateStackTop(rt);
    return JS_ExecutePendingJob(rt, &job);
}

int64_t qjs_heap(JSContext *ctx) {
    JSMemoryUsage u;
    JS_ComputeMemoryUsage(JS_GetRuntime(ctx), &u);
    return u.malloc_size;
}

// track calls the global named by the guard with (promise, reason, handled) when it is a function, as script.rs's
// tracker does; what it throws is dropped.
static void track(JSContext *ctx, JSValueConst promise, JSValueConst reason, bool handled, void *opaque) {
    qjs_guard *g = opaque;
    JSValue global = JS_GetGlobalObject(ctx);
    JSValue f = JS_GetPropertyStr(ctx, global, g->tracker);
    JS_FreeValue(ctx, global);
    if (JS_IsException(f)) {
        JS_FreeValue(ctx, JS_GetException(ctx));
        return;
    }
    if (JS_IsFunction(ctx, f)) {
        JSValue argv[3] = {promise, reason, JS_NewBool(ctx, handled)};
        JSValue r = qjs_call(ctx, f, JS_UNDEFINED, 3, argv);
        if (JS_IsException(r))
            JS_FreeValue(ctx, JS_GetException(ctx));
        JS_FreeValue(ctx, r);
    }
    JS_FreeValue(ctx, f);
}

int qjs_track_rejections(JSContext *ctx, qjs_guard *g, const char *name) {
    char *copy = strdup(name);
    if (!copy)
        return -1;
    free(g->tracker);
    g->tracker = copy;
    JS_SetHostPromiseRejectionTracker(JS_GetRuntime(ctx), track, g);
    return 0;
}

int qjs_tag(JSValue v) { return JS_VALUE_GET_TAG(v); }

JSValue qjs_undefined(void) { return JS_UNDEFINED; }

// qjs_define_native makes the global name a native function that calls the Go native at index, as rquickjs's
// `Function::new` does: an instance of the native class with Function.prototype and a configurable length of 0.
int qjs_define_native(JSContext *ctx, const char *name, int index) {
    qjs_guard *g = JS_GetRuntimeOpaque(JS_GetRuntime(ctx));
    JSValue proto = JS_GetFunctionProto(ctx);
    JSValue f = JS_NewObjectProtoClass(ctx, proto, g->fn_class);
    JS_FreeValue(ctx, proto);
    if (JS_IsException(f))
        return -1;
    JS_SetOpaque(f, (void *)(uintptr_t)(index + 1));
    if (JS_DefinePropertyValueStr(ctx, f, "length", JS_NewInt32(ctx, 0), JS_PROP_CONFIGURABLE | JS_PROP_THROW) < 0) {
        JS_FreeValue(ctx, f);
        return -1;
    }
    JSValue global = JS_GetGlobalObject(ctx);
    int r = JS_SetPropertyStr(ctx, global, name, f);
    JS_FreeValue(ctx, global);
    return r < 0 ? -1 : 0;
}

// qjs_throw_message throws a new Error with an own `message` property, as rquickjs's `Exception::throw_message`.
JSValue qjs_throw_message(JSContext *ctx, const char *msg, size_t len) {
    JSValue e = JS_NewError(ctx);
    if (JS_IsException(e))
        return e;
    if (JS_SetPropertyStr(ctx, e, "message", JS_NewStringLen(ctx, msg, len)) < 0) {
        JS_FreeValue(ctx, e);
        return JS_EXCEPTION;
    }
    return JS_Throw(ctx, e);
}

// qjs_throw_panic throws an uncatchable error: a Go native panicked, and no JavaScript may run on past it.
JSValue qjs_throw_panic(JSContext *ctx) {
    JSValue e = JS_NewError(ctx);
    if (JS_IsException(e))
        return e;
    JS_SetUncatchableError(ctx, e);
    return JS_Throw(ctx, e);
}

JSValue qjs_new_string(JSContext *ctx, const char *s, size_t len) { return JS_NewStringLen(ctx, len ? s : "", len); }

JSValue qjs_global(JSContext *ctx, const char *name) {
    JSValue global = JS_GetGlobalObject(ctx);
    JSValue v = JS_GetPropertyStr(ctx, global, name);
    JS_FreeValue(ctx, global);
    return v;
}

JSValue qjs_exception(void) { return JS_EXCEPTION; }
