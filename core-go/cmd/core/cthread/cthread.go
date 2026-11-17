//go:build !windows

// Package cthread calls the core's C entry points from a thread the Go runtime did not create, as the app's
// JavaScript threads do, and reports whether that thread's alternate signal stack stayed as it was (the core's
// tests use it; see bridge_unix.c in cmd/core).
package cthread

/*
#include <pthread.h>
#include <signal.h>
#include <stdint.h>
#include <stdlib.h>

extern void *nr_call(const void *ptr, uint32_t n);
extern void nr_free(void *p);

typedef struct {
	const char *req;
	uint32_t n;
	char *out;
	int before, after; // ss_flags of the thread's alternate signal stack before and after the call
} job;

static void *body(void *arg) {
	job *j = arg;
	stack_t st;
	sigaltstack(NULL, &st);
	j->before = st.ss_flags;
	char *p = nr_call(j->req, j->n);
	sigaltstack(NULL, &st);
	j->after = st.ss_flags;
	if (p) {
		size_t len = 0;
		while (p[len]) len++;
		j->out = malloc(len + 1);
		for (size_t i = 0; i <= len; i++) j->out[i] = p[i];
		nr_free(p);
	}
	return NULL;
}

static job *run(const char *req, uint32_t n) {
	job *j = calloc(1, sizeof(job));
	j->req = req;
	j->n = n;
	pthread_t t;
	pthread_create(&t, NULL, body, j);
	pthread_join(t, NULL);
	return j;
}
*/
import "C"

import "unsafe"

// Call runs nr_call with req on a new C thread: the answer, and whether that thread's alternate signal stack
// was the same after the call as before.
func Call(req string) (string, bool) {
	r := C.CString(req)
	defer C.free(unsafe.Pointer(r))
	j := C.run(r, C.uint32_t(len(req)))
	defer C.free(unsafe.Pointer(j))
	out := ""
	if j.out != nil {
		out = C.GoString(j.out)
		C.free(unsafe.Pointer(j.out))
	}
	return out, j.before == j.after
}
