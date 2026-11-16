// C side of the shader fill GPU (wgpu-native): object creation with the descriptors shader.rs uses, and the
// draw split into steps so Go can poll between them. Messages returned as char* are malloc'd (free them);
// NULL means success.
#ifndef NR_GLUE_H
#define NR_GLUE_H

#include <stdint.h>
#include <stdlib.h>
#include <webgpu/webgpu.h>
#include <webgpu/wgpu.h>

typedef struct {
	WGPUInstance instance;
	WGPUAdapter adapter;
	WGPUDevice device;
	WGPUQueue queue;
	WGPUShaderModule vs;
} nr_gpu;

char *nr_instance(nr_gpu *g, WGPUInstanceBackend backends);
char *nr_adapter(nr_gpu *g, WGPUPowerPreference power, int fallback);
// The adapter's device name; *backend is its WGPUBackendType, *type its WGPUAdapterType.
char *nr_adapter_name(nr_gpu *g, int *backend, int *type);
char *nr_device(nr_gpu *g);
void nr_release(nr_gpu *g);

void nr_push_scope(WGPUDevice d);
// The message of the error the scope caught, or NULL.
char *nr_pop_scope(WGPUDevice d);

WGPUShaderModule nr_glsl(WGPUDevice d, const char *code, size_t len);

typedef struct {
	WGPURenderPipeline pipe;
	WGPUBindGroupLayout bgl;
} nr_pipe;

void nr_pipeline(nr_gpu *g, WGPUShaderModule fs, int samplers, nr_pipe *out);
void nr_pipe_release(nr_pipe *p);

// A texture handed to the shader: RGBA8 (4 bytes per texel) or RGBA16F (half != 0, 8 bytes).
typedef struct {
	uint32_t w, h;
	int half;
	const void *data;
	WGPUAddressMode mode;
} nr_tex;

typedef struct {
	WGPUBuffer ubuf;
	int n;
	WGPUTexture *texs;
	WGPUTextureView *views;
	WGPUSampler *samplers;
	WGPUBindGroup group;
	WGPUTexture target;
	WGPUTextureView view;
	WGPUBuffer read;
	uint32_t pw, ph, row;
	// 0 while mapping, then the WGPUMapAsyncStatus.
	volatile int mapped;
} nr_draw;

void nr_draw_begin(nr_gpu *g, nr_pipe *p, nr_draw *d, const void *ubo, size_t len, const nr_tex *texs, int n, uint32_t pw, uint32_t ph);
void nr_draw_tile(nr_gpu *g, nr_pipe *p, nr_draw *d, uint32_t y0, uint32_t rows);
// Starts copying the target to the readback buffer and mapping it.
void nr_draw_copy(nr_gpu *g, nr_draw *d);
const uint8_t *nr_draw_mapped(nr_draw *d);
void nr_draw_end(nr_draw *d);

// One non-blocking poll; 1 when the queue is empty.
int nr_poll(WGPUDevice d);

#endif
