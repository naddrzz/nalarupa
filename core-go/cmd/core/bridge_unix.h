// The C side of the entry points on Linux and macOS (bridge_unix.c).
#include <stdint.h>

typedef struct nr_job {
	int kind; // 0: nr_call, 1: nr_render
	const void *ptr;
	uint32_t n;
	uint32_t w, h;
	double cam_x, cam_y, zoom;
	uint32_t bg;
	const void *solo;
	uint32_t solo_len;
	uint32_t flags;
	void *out;
	int done;
	struct nr_job *next;
} nr_job;

nr_job *nr_next_job(void);
void nr_finish(nr_job *job, void *out);
void *nr_call(const void *ptr, uint32_t n);
void nr_free(void *p);
void *nr_render(uint32_t w, uint32_t h, double cam_x, double cam_y, double zoom, uint32_t bg, const void *solo,
	uint32_t solo_len, uint32_t flags);
