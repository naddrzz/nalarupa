package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// suspender plays the app: it installs a SIGPWR handler that, as the JavaScript engine's does, tells a thread
// suspended for a stack scan to back off when the handler runs on an alternate signal stack, and gives the main
// thread an alternate stack, as Bun does. It then loads the core, calls it once and suspends the main thread from a
// helper the way the collector does, retrying until the handler runs on the main thread's own stack.
const suspender = `#define _GNU_SOURCE
#include <dlfcn.h>
#include <pthread.h>
#include <sched.h>
#include <semaphore.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

static pthread_t target;
static char *lo, *hi;
static sem_t ack;
static volatile sig_atomic_t own;

static void handler(int sig, siginfo_t *info, void *ctx) {
	char here;
	own = &here >= lo && &here < hi;
	sem_post(&ack);
}

static void *collector(void *arg) {
	for (long tries = 1;; tries++) {
		pthread_kill(target, SIGPWR);
		while (sem_wait(&ack) != 0) {
		}
		if (own) {
			printf("suspended after %ld tries\n", tries);
			return NULL;
		}
		sched_yield();
	}
}

int main(int argc, char **argv) {
	target = pthread_self();
	pthread_attr_t attr;
	void *addr;
	size_t size;
	if (pthread_getattr_np(target, &attr) != 0 || pthread_attr_getstack(&attr, &addr, &size) != 0) return 2;
	lo = addr;
	hi = lo + size;
	if (sem_init(&ack, 0, 0) != 0) return 2;
	struct sigaction sa = {0};
	sa.sa_sigaction = handler;
	sa.sa_flags = SA_SIGINFO | SA_RESTART;
	sigfillset(&sa.sa_mask);
	if (sigaction(SIGPWR, &sa, NULL) != 0) return 2;
	stack_t st = {0};
	st.ss_size = 1 << 19;
	st.ss_sp = malloc(st.ss_size);
	if (!st.ss_sp || sigaltstack(&st, NULL) != 0) return 2;

	void *core = dlopen(argv[1], RTLD_NOW);
	if (!core) {
		fprintf(stderr, "%s\n", dlerror());
		return 2;
	}
	void *(*call)(const void *, uint32_t) = (void *(*)(const void *, uint32_t))dlsym(core, "nr_call");
	void (*release)(void *) = (void (*)(void *))dlsym(core, "nr_free");
	if (!call || !release) return 2;
	static const char req[] = "{\"cmd\":\"state\"}";
	void *out = call(req, sizeof req - 1);
	if (!out) return 3;
	release(out);

	pthread_t t;
	if (pthread_create(&t, NULL, collector, NULL) != 0 || pthread_join(t, NULL) != 0) return 2;
	return 0;
}
`

// Loading the core must leave the engine's suspend signal as it found it: the Go runtime set SA_ONSTACK on it, so
// in Bun, whose main thread has an alternate signal stack, the collector's suspend of that thread retried forever
// and the app froze (e2e hung on Linux). Without the fix the suspender never returns.
func TestTheEngineCanStillSuspendTheMainThread(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the core as a shared library")
	}
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
	dir := t.TempDir()
	lib := filepath.Join(dir, "libnalarupa_core.so")
	build := exec.Command(gobin, "build", "-buildmode=c-shared", "-o", lib, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the core: %v\n%s", err, out)
	}
	cc, err := exec.Command(gobin, "env", "CC").Output()
	if err != nil {
		t.Fatalf("go env CC: %v", err)
	}
	ccArgs := strings.Fields(string(cc))
	if len(ccArgs) == 0 {
		t.Fatal("go env CC is empty")
	}
	src := filepath.Join(dir, "suspender.c")
	if err := os.WriteFile(src, []byte(suspender), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "suspender")
	compile := exec.Command(ccArgs[0], append(ccArgs[1:], "-o", exe, src, "-ldl", "-lpthread")...)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compiling the suspender: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, exe, lib)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("the main thread could not be suspended in 30s: its handler ran on the alternate signal stack (SA_ONSTACK)\n%s", out)
	}
	if err != nil {
		t.Fatalf("suspender: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
