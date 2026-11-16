// The C side of package qjs: what cgo cannot call directly (macros, callbacks) and the runtime setup rquickjs
// does. Not part of quickjs-ng.
#ifndef QJS_WRAP_H
#define QJS_WRAP_H

#include <stdint.h>
#include "quickjs.h"

// qjs_guard is what the interrupt handler and the rejection tracker read: the deadline, the cancel flag and the
// tracker's global. The deadline and the flag may be written from another thread.
typedef struct qjs_guard qjs_guard;

qjs_guard *qjs_guard_new(void);
void qjs_guard_free(qjs_guard *g);
int64_t qjs_now(void);
void qjs_arm(qjs_guard *g, int64_t deadline);
int qjs_expired(qjs_guard *g);
void qjs_cancel(qjs_guard *g);
int qjs_canceled(qjs_guard *g);

JSContext *qjs_new(size_t memory, size_t stack, qjs_guard *g);
void qjs_free(JSContext *ctx);

JSValue qjs_eval(JSContext *ctx, const char *src, size_t len, const char *file, int flags);
JSValue qjs_call(JSContext *ctx, JSValue f, JSValue this_obj, int argc, JSValue *argv);
int qjs_run_job(JSContext *ctx);
int64_t qjs_heap(JSContext *ctx);
int qjs_track_rejections(JSContext *ctx, qjs_guard *g, const char *name);

int qjs_tag(JSValue v);
JSValue qjs_undefined(void);

#endif
