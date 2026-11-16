// The C side of package qjs (see wrap.h). Every entry from Go into JavaScript first moves the stack top to the
// current stack pointer, as rquickjs does before each eval, call and job with its `parallel` feature: the stack
// limit is then measured from where this call entered C.
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include "cutils.h"
#include "wrap.h"

struct qjs_guard {
    _Atomic int cancel;
    // deadline is the qjs_now time past which running JavaScript is interrupted; 0: none.
    _Atomic int64_t deadline;
    // tracker is the global the rejection tracker calls, or NULL.
    char *tracker;
};

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

// qjs_new is rquickjs's `Runtime::new` (which registers three classes of its own) followed by what script.rs sets
// on it: the memory and stack limits, the interrupt handler, then `Context::full` (JS_NewContext). The guard is the
// runtime's opaque.
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
        JSClassDef def = {.class_name = classes[i]};
        if (JS_NewClass(rt, ids[i], &def)) {
            JS_FreeRuntime(rt);
            return NULL;
        }
    }
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
