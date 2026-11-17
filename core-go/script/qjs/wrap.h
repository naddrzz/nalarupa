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

void qjs_set_go(qjs_guard *g, uintptr_t h);
int qjs_define_native(JSContext *ctx, const char *name, int index);
JSValue qjs_throw_message(JSContext *ctx, const char *msg, size_t len);
JSValue qjs_throw_panic(JSContext *ctx);
JSValue qjs_new_string(JSContext *ctx, const char *s, size_t len);
JSValue qjs_global(JSContext *ctx, const char *name);
JSValue qjs_exception(void);

#endif
