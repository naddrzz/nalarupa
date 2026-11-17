//go:build !windows

// The entry points on Linux and macOS are C: the caller's thread (the app's JavaScript thread, or a worker's) only
// queues the job and waits, while a goroutine on one of the Go runtime's own threads does the work. Go code run
// on the caller's thread would leave the Go runtime bound to it with its own alternate signal stack, and the
// JavaScript engine's collector, which suspends a thread with a signal and then scans its stack, would find the
// handler on that stack instead of the thread's, back off and retry forever (chat-check hung the Linux CI).
#include <pthread.h>
#include <signal.h>
#include <stdlib.h>
#include "bridge_unix.h"

#ifdef SIGPWR
// The same engine suspends a thread with SIGPWR (Linux), and its handler backs off and retries when it runs on an
// alternate signal stack. Loading the Go runtime adds SA_ONSTACK to every handler already installed, and Bun gives
// its main thread an alternate stack (for its crash handler): from then on a collection that scanned the main thread
// from a helper thread retried forever (e2e hung on Linux). saved is the handler before the runtime started; restore
// takes the flag off again while the handler is still that one. The engine sends SIGPWR only to its own threads.
static struct sigaction saved;
static int have_saved;

// Priority 101 runs before the Go runtime's own initializer, which sets the flag.
__attribute__((constructor(101))) static void save_suspend_signal(void) {
	have_saved = sigaction(SIGPWR, NULL, &saved) == 0;
}

// POSIX has no compare-and-swap for a disposition, so this check-then-set is only safe while nothing else installs a
// SIGPWR handler: it runs once (pthread_once), from the Go init while the host loads this library, before any call
// returns to the host; the engine installs its handler when its VM starts, before the library is loaded.
static void restore_suspend_signal(void) {
	struct sigaction cur;
	if (!have_saved || (saved.sa_flags & SA_ONSTACK) || sigaction(SIGPWR, NULL, &cur) != 0) {
		return;
	}
	if ((cur.sa_flags & SA_ONSTACK) && cur.sa_handler == saved.sa_handler) {
		cur.sa_flags &= ~SA_ONSTACK;
		sigaction(SIGPWR, &cur, NULL);
	}
}
#else
static void restore_suspend_signal(void) {}
#endif

static pthread_once_t restored = PTHREAD_ONCE_INIT;

void nr_restore_signals(void) { pthread_once(&restored, restore_suspend_signal); }

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t queued = PTHREAD_COND_INITIALIZER; // a job was queued
static pthread_cond_t finished = PTHREAD_COND_INITIALIZER; // a job is done
static nr_job *head, *tail;

static void *submit(nr_job *job) {
	nr_restore_signals();
	pthread_mutex_lock(&mu);
	job->done = 0;
	job->next = NULL;
	if (tail) {
		tail->next = job;
	} else {
		head = job;
	}
	tail = job;
	pthread_cond_signal(&queued);
	while (!job->done) {
		pthread_cond_wait(&finished, &mu);
	}
	pthread_mutex_unlock(&mu);
	return job->out;
}

nr_job *nr_next_job(void) {
	pthread_mutex_lock(&mu);
	while (!head) {
		pthread_cond_wait(&queued, &mu);
	}
	nr_job *job = head;
	head = job->next;
	if (!head) {
		tail = NULL;
	}
	pthread_mutex_unlock(&mu);
	return job;
}

void nr_finish(nr_job *job, void *out) {
	pthread_mutex_lock(&mu);
	job->out = out;
	job->done = 1;
	pthread_cond_broadcast(&finished);
	pthread_mutex_unlock(&mu);
}

void *nr_call(const void *ptr, uint32_t n) {
	nr_job job = {0};
	job.kind = 0;
	job.ptr = ptr;
	job.n = n;
	return submit(&job);
}

void nr_free(void *p) { free(p); }

void *nr_render(uint32_t w, uint32_t h, double cam_x, double cam_y, double zoom, uint32_t bg, const void *solo,
	uint32_t solo_len, uint32_t flags) {
	nr_job job = {0};
	job.kind = 1;
	job.w = w;
	job.h = h;
	job.cam_x = cam_x;
	job.cam_y = cam_y;
	job.zoom = zoom;
	job.bg = bg;
	job.solo = solo;
	job.solo_len = solo_len;
	job.flags = flags;
	return submit(&job);
}
