//go:build !windows

// The entry points on Linux and macOS are C: the caller's thread (the app's JavaScript thread, or a worker's) only
// queues the job and waits, while a goroutine on one of the Go runtime's own threads does the work. Go code run
// on the caller's thread would leave the Go runtime bound to it with its own alternate signal stack, and the
// JavaScript engine's collector, which suspends a thread with a signal and then scans its stack, would find the
// handler on that stack instead of the thread's, back off and retry forever (chat-check hung the Linux CI).
#include <pthread.h>
#include <stdlib.h>
#include "bridge_unix.h"

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t queued = PTHREAD_COND_INITIALIZER; // a job was queued
static pthread_cond_t finished = PTHREAD_COND_INITIALIZER; // a job is done
static nr_job *head, *tail;

static void *submit(nr_job *job) {
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
