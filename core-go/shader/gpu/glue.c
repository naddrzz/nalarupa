#include "glue.h"

#include <stdio.h>
#include <string.h>

static WGPUStringView sv(const char *s) {
	WGPUStringView v = {s, s ? strlen(s) : 0};
	return v;
}

static char *copy_view(WGPUStringView s) {
	size_t n = s.data == NULL ? 0 : s.length == WGPU_STRLEN ? strlen(s.data) : s.length;
	char *out = malloc(n + 1);
	if (out == NULL) {
		return NULL;
	}
	if (n > 0) {
		memcpy(out, s.data, n);
	}
	out[n] = 0;
	return out;
}

static char *copy_str(const char *s) {
	return copy_view(sv(s));
}

// Full-screen triangle (shader.rs `VS`).
static const char *VS =
	"@vertex fn vs(@builtin(vertex_index) i: u32) -> @builtin(position) vec4<f32> {\n"
	"    let p = vec2<f32>(f32((i << 1u) & 2u), f32(i & 2u));\n"
	"    return vec4<f32>(p * 2.0 - 1.0, 0.0, 1.0);\n"
	"}";

char *nr_instance(nr_gpu *g, WGPUInstanceBackend backends) {
	WGPUInstanceExtras extras;
	memset(&extras, 0, sizeof extras);
	extras.chain.sType = (WGPUSType)WGPUSType_InstanceExtras;
	extras.backends = backends;
	// wgpu's release-build default (InstanceFlags::from_build_config).
	extras.flags = WGPUInstanceFlag_ValidationIndirectCall;
	extras.dx12ShaderCompiler = WGPUDx12Compiler_Undefined;
	WGPUInstanceDescriptor desc = WGPU_INSTANCE_DESCRIPTOR_INIT;
	desc.nextInChain = &extras.chain;
	g->instance = wgpuCreateInstance(&desc);
	return g->instance ? NULL : copy_str("could not create a wgpu instance");
}

typedef struct {
	int status;
	WGPUAdapter adapter;
	WGPUDevice device;
	char *msg;
} request;

static void on_adapter(WGPURequestAdapterStatus status, WGPUAdapter adapter, WGPUStringView message, void *u1, void *u2) {
	(void)u2;
	request *r = u1;
	r->status = (int)status;
	r->adapter = adapter;
	r->msg = copy_view(message);
}

char *nr_adapter(nr_gpu *g, WGPUPowerPreference power, int fallback) {
	request r = {0};
	WGPURequestAdapterOptions opt = WGPU_REQUEST_ADAPTER_OPTIONS_INIT;
	opt.powerPreference = power;
	opt.forceFallbackAdapter = fallback ? 1 : 0;
	WGPURequestAdapterCallbackInfo cb = WGPU_REQUEST_ADAPTER_CALLBACK_INFO_INIT;
	cb.mode = WGPUCallbackMode_AllowSpontaneous;
	cb.callback = on_adapter;
	cb.userdata1 = &r;
	wgpuInstanceRequestAdapter(g->instance, &opt, cb);
	while (r.status == 0) {
		wgpuInstanceProcessEvents(g->instance);
	}
	if (r.status == WGPURequestAdapterStatus_Success && r.adapter) {
		free(r.msg);
		g->adapter = r.adapter;
		return NULL;
	}
	return r.msg ? r.msg : copy_str("request failed");
}

char *nr_adapter_name(nr_gpu *g, int *backend, int *type) {
	WGPUAdapterInfo info = WGPU_ADAPTER_INFO_INIT;
	wgpuAdapterGetInfo(g->adapter, &info);
	char *name = copy_view(info.device);
	*backend = (int)info.backendType;
	*type = (int)info.adapterType;
	wgpuAdapterInfoFreeMembers(info);
	return name;
}

static void on_device(WGPURequestDeviceStatus status, WGPUDevice device, WGPUStringView message, void *u1, void *u2) {
	(void)u2;
	request *r = u1;
	r->status = (int)status;
	r->device = device;
	r->msg = copy_view(message);
}

// Errors no scope caught (shader.rs `on_uncaptured_error`).
static void on_uncaptured(WGPUDevice const *device, WGPUErrorType type, WGPUStringView message, void *u1, void *u2) {
	(void)device, (void)type, (void)u1, (void)u2;
	size_t n = message.data == NULL ? 0 : message.length == WGPU_STRLEN ? strlen(message.data) : message.length;
	fprintf(stderr, "shader fill GPU error: %.*s\n", (int)n, message.data ? message.data : "");
}

char *nr_device(nr_gpu *g) {
	request r = {0};
	WGPUDeviceDescriptor desc = WGPU_DEVICE_DESCRIPTOR_INIT;
	desc.label = sv("nalarupa shader fills");
	desc.uncapturedErrorCallbackInfo.callback = on_uncaptured;
	WGPURequestDeviceCallbackInfo cb = WGPU_REQUEST_DEVICE_CALLBACK_INFO_INIT;
	cb.mode = WGPUCallbackMode_AllowSpontaneous;
	cb.callback = on_device;
	cb.userdata1 = &r;
	wgpuAdapterRequestDevice(g->adapter, &desc, cb);
	while (r.status == 0) {
		wgpuInstanceProcessEvents(g->instance);
	}
	if (r.status != WGPURequestDeviceStatus_Success || r.device == NULL) {
		return r.msg ? r.msg : copy_str("request failed");
	}
	free(r.msg);
	g->device = r.device;
	g->queue = wgpuDeviceGetQueue(g->device);
	WGPUShaderSourceWGSL wgsl = WGPU_SHADER_SOURCE_WGSL_INIT;
	wgsl.code = sv(VS);
	WGPUShaderModuleDescriptor md = WGPU_SHADER_MODULE_DESCRIPTOR_INIT;
	md.nextInChain = &wgsl.chain;
	md.label = sv("nr-vs");
	g->vs = wgpuDeviceCreateShaderModule(g->device, &md);
	return NULL;
}

void nr_release(nr_gpu *g) {
	if (g->vs) wgpuShaderModuleRelease(g->vs);
	if (g->queue) wgpuQueueRelease(g->queue);
	if (g->device) wgpuDeviceRelease(g->device);
	if (g->adapter) wgpuAdapterRelease(g->adapter);
	if (g->instance) wgpuInstanceRelease(g->instance);
	memset(g, 0, sizeof *g);
}

void nr_push_scope(WGPUDevice d) {
	wgpuDevicePushErrorScope(d, WGPUErrorFilter_Validation);
}

typedef struct {
	int status;
	int type;
	char *msg;
} popped;

static void on_pop(WGPUPopErrorScopeStatus status, WGPUErrorType type, WGPUStringView message, void *u1, void *u2) {
	(void)u2;
	popped *p = u1;
	p->type = (int)type;
	p->msg = copy_view(message);
	p->status = (int)status;
}

char *nr_pop_scope(WGPUDevice d) {
	popped p = {0};
	WGPUPopErrorScopeCallbackInfo cb = WGPU_POP_ERROR_SCOPE_CALLBACK_INFO_INIT;
	cb.mode = WGPUCallbackMode_AllowSpontaneous;
	cb.callback = on_pop;
	cb.userdata1 = &p;
	wgpuDevicePopErrorScope(d, cb);
	while (p.status == 0) {
		wgpuDevicePoll(d, 0, NULL);
	}
	if (p.status == WGPUPopErrorScopeStatus_Success && p.type == WGPUErrorType_NoError) {
		free(p.msg);
		return NULL;
	}
	return p.msg ? p.msg : copy_str("error scope failed");
}

WGPUShaderModule nr_glsl(WGPUDevice d, const char *code, size_t len) {
	WGPUShaderSourceGLSL glsl;
	memset(&glsl, 0, sizeof glsl);
	glsl.chain.sType = (WGPUSType)WGPUSType_ShaderSourceGLSL;
	glsl.stage = WGPUShaderStage_Fragment;
	glsl.code.data = code;
	glsl.code.length = len;
	WGPUShaderModuleDescriptor md = WGPU_SHADER_MODULE_DESCRIPTOR_INIT;
	md.nextInChain = &glsl.chain;
	md.label = sv("nr-fs");
	return wgpuDeviceCreateShaderModule(d, &md);
}

void nr_pipeline(nr_gpu *g, WGPUShaderModule fs, int samplers, nr_pipe *out) {
	int n = 1 + 2 * samplers;
	WGPUBindGroupLayoutEntry *entries = calloc((size_t)n, sizeof *entries);
	entries[0].binding = 0;
	entries[0].visibility = WGPUShaderStage_Fragment;
	entries[0].buffer.type = WGPUBufferBindingType_Uniform;
	for (int i = 0; i < samplers; i++) {
		WGPUBindGroupLayoutEntry *t = &entries[1 + 2 * i], *s = &entries[2 + 2 * i];
		t->binding = (uint32_t)(1 + 2 * i);
		t->visibility = WGPUShaderStage_Fragment;
		t->texture.sampleType = WGPUTextureSampleType_Float;
		t->texture.viewDimension = WGPUTextureViewDimension_2D;
		s->binding = (uint32_t)(2 + 2 * i);
		s->visibility = WGPUShaderStage_Fragment;
		s->sampler.type = WGPUSamplerBindingType_Filtering;
	}
	WGPUBindGroupLayoutDescriptor bd = WGPU_BIND_GROUP_LAYOUT_DESCRIPTOR_INIT;
	bd.entryCount = (size_t)n;
	bd.entries = entries;
	out->bgl = wgpuDeviceCreateBindGroupLayout(g->device, &bd);
	free(entries);

	WGPUPipelineLayoutDescriptor ld = WGPU_PIPELINE_LAYOUT_DESCRIPTOR_INIT;
	ld.bindGroupLayoutCount = 1;
	ld.bindGroupLayouts = &out->bgl;
	WGPUPipelineLayout layout = wgpuDeviceCreatePipelineLayout(g->device, &ld);

	WGPUColorTargetState target = WGPU_COLOR_TARGET_STATE_INIT;
	target.format = WGPUTextureFormat_RGBA8Unorm;
	target.writeMask = WGPUColorWriteMask_All;
	WGPUFragmentState frag = WGPU_FRAGMENT_STATE_INIT;
	frag.module = fs;
	frag.entryPoint = sv("main");
	frag.targetCount = 1;
	frag.targets = &target;
	WGPURenderPipelineDescriptor pd = WGPU_RENDER_PIPELINE_DESCRIPTOR_INIT;
	pd.label = sv("nr-shader-fill");
	pd.layout = layout;
	pd.vertex.module = g->vs;
	pd.vertex.entryPoint = sv("vs");
	pd.primitive.topology = WGPUPrimitiveTopology_TriangleList;
	pd.primitive.frontFace = WGPUFrontFace_CCW;
	pd.primitive.cullMode = WGPUCullMode_None;
	pd.multisample.count = 1;
	pd.multisample.mask = 0xFFFFFFFF;
	pd.fragment = &frag;
	out->pipe = wgpuDeviceCreateRenderPipeline(g->device, &pd);
	wgpuPipelineLayoutRelease(layout);
}

void nr_pipe_release(nr_pipe *p) {
	if (p->pipe) wgpuRenderPipelineRelease(p->pipe);
	if (p->bgl) wgpuBindGroupLayoutRelease(p->bgl);
	p->pipe = NULL;
	p->bgl = NULL;
}

void nr_draw_begin(nr_gpu *g, nr_pipe *p, nr_draw *d, const void *ubo, size_t len, const nr_tex *texs, int n, uint32_t pw, uint32_t ph) {
	WGPUBufferDescriptor ud = WGPU_BUFFER_DESCRIPTOR_INIT;
	ud.size = len < 16 ? 16 : len;
	ud.usage = WGPUBufferUsage_Uniform | WGPUBufferUsage_CopyDst;
	d->ubuf = wgpuDeviceCreateBuffer(g->device, &ud);
	wgpuQueueWriteBuffer(g->queue, d->ubuf, 0, ubo, len);

	d->n = n;
	d->texs = calloc((size_t)(n > 0 ? n : 1), sizeof *d->texs);
	d->views = calloc((size_t)(n > 0 ? n : 1), sizeof *d->views);
	d->samplers = calloc((size_t)(n > 0 ? n : 1), sizeof *d->samplers);
	for (int i = 0; i < n; i++) {
		const nr_tex *t = &texs[i];
		uint32_t bpp = t->half ? 8 : 4;
		WGPUTextureDescriptor td = WGPU_TEXTURE_DESCRIPTOR_INIT;
		td.usage = WGPUTextureUsage_TextureBinding | WGPUTextureUsage_CopyDst;
		td.dimension = WGPUTextureDimension_2D;
		td.size.width = t->w;
		td.size.height = t->h;
		td.size.depthOrArrayLayers = 1;
		td.format = t->half ? WGPUTextureFormat_RGBA16Float : WGPUTextureFormat_RGBA8Unorm;
		td.mipLevelCount = 1;
		td.sampleCount = 1;
		d->texs[i] = wgpuDeviceCreateTexture(g->device, &td);
		WGPUTexelCopyTextureInfo dst = WGPU_TEXEL_COPY_TEXTURE_INFO_INIT;
		dst.texture = d->texs[i];
		dst.aspect = WGPUTextureAspect_All;
		WGPUTexelCopyBufferLayout lay = WGPU_TEXEL_COPY_BUFFER_LAYOUT_INIT;
		lay.offset = 0;
		lay.bytesPerRow = t->w * bpp;
		lay.rowsPerImage = t->h;
		wgpuQueueWriteTexture(g->queue, &dst, t->data, (size_t)t->w * t->h * bpp, &lay, &td.size);
		WGPUSamplerDescriptor sd = WGPU_SAMPLER_DESCRIPTOR_INIT;
		sd.addressModeU = t->mode;
		sd.addressModeV = t->mode;
		sd.addressModeW = WGPUAddressMode_ClampToEdge;
		sd.magFilter = WGPUFilterMode_Linear;
		sd.minFilter = WGPUFilterMode_Linear;
		sd.mipmapFilter = WGPUMipmapFilterMode_Nearest;
		sd.lodMinClamp = 0;
		sd.lodMaxClamp = 32;
		sd.maxAnisotropy = 1;
		d->samplers[i] = wgpuDeviceCreateSampler(g->device, &sd);
		d->views[i] = wgpuTextureCreateView(d->texs[i], NULL);
	}

	int ne = 1 + 2 * n;
	WGPUBindGroupEntry *entries = calloc((size_t)ne, sizeof *entries);
	entries[0].binding = 0;
	entries[0].buffer = d->ubuf;
	entries[0].offset = 0;
	entries[0].size = WGPU_WHOLE_SIZE;
	for (int i = 0; i < n; i++) {
		entries[1 + 2 * i].binding = (uint32_t)(1 + 2 * i);
		entries[1 + 2 * i].textureView = d->views[i];
		entries[2 + 2 * i].binding = (uint32_t)(2 + 2 * i);
		entries[2 + 2 * i].sampler = d->samplers[i];
	}
	WGPUBindGroupDescriptor gd = WGPU_BIND_GROUP_DESCRIPTOR_INIT;
	gd.layout = p->bgl;
	gd.entryCount = (size_t)ne;
	gd.entries = entries;
	d->group = wgpuDeviceCreateBindGroup(g->device, &gd);
	free(entries);

	WGPUTextureDescriptor rd = WGPU_TEXTURE_DESCRIPTOR_INIT;
	rd.usage = WGPUTextureUsage_RenderAttachment | WGPUTextureUsage_CopySrc;
	rd.dimension = WGPUTextureDimension_2D;
	rd.size.width = pw;
	rd.size.height = ph;
	rd.size.depthOrArrayLayers = 1;
	rd.format = WGPUTextureFormat_RGBA8Unorm;
	rd.mipLevelCount = 1;
	rd.sampleCount = 1;
	d->target = wgpuDeviceCreateTexture(g->device, &rd);
	d->view = wgpuTextureCreateView(d->target, NULL);
	d->pw = pw;
	d->ph = ph;
	d->row = (pw * 4 + 255) / 256 * 256;
}

void nr_draw_tile(nr_gpu *g, nr_pipe *p, nr_draw *d, uint32_t y0, uint32_t rows) {
	WGPUCommandEncoder enc = wgpuDeviceCreateCommandEncoder(g->device, NULL);
	WGPURenderPassColorAttachment att = WGPU_RENDER_PASS_COLOR_ATTACHMENT_INIT;
	att.view = d->view;
	att.loadOp = y0 == 0 ? WGPULoadOp_Clear : WGPULoadOp_Load;
	att.storeOp = WGPUStoreOp_Store;
	WGPUColor transparent = {0, 0, 0, 0};
	att.clearValue = transparent;
	WGPURenderPassDescriptor rp = WGPU_RENDER_PASS_DESCRIPTOR_INIT;
	rp.colorAttachmentCount = 1;
	rp.colorAttachments = &att;
	WGPURenderPassEncoder pass = wgpuCommandEncoderBeginRenderPass(enc, &rp);
	wgpuRenderPassEncoderSetPipeline(pass, p->pipe);
	wgpuRenderPassEncoderSetBindGroup(pass, 0, d->group, 0, NULL);
	wgpuRenderPassEncoderSetScissorRect(pass, 0, y0, d->pw, rows);
	wgpuRenderPassEncoderDraw(pass, 3, 1, 0, 0);
	wgpuRenderPassEncoderEnd(pass);
	wgpuRenderPassEncoderRelease(pass);
	WGPUCommandBuffer cmd = wgpuCommandEncoderFinish(enc, NULL);
	wgpuQueueSubmit(g->queue, 1, &cmd);
	wgpuCommandBufferRelease(cmd);
	wgpuCommandEncoderRelease(enc);
}

static void on_map(WGPUMapAsyncStatus status, WGPUStringView message, void *u1, void *u2) {
	(void)message, (void)u2;
	((nr_draw *)u1)->mapped = (int)status;
}

void nr_draw_copy(nr_gpu *g, nr_draw *d) {
	WGPUBufferDescriptor bd = WGPU_BUFFER_DESCRIPTOR_INIT;
	bd.size = (uint64_t)d->row * d->ph;
	bd.usage = WGPUBufferUsage_MapRead | WGPUBufferUsage_CopyDst;
	d->read = wgpuDeviceCreateBuffer(g->device, &bd);
	WGPUCommandEncoder enc = wgpuDeviceCreateCommandEncoder(g->device, NULL);
	WGPUTexelCopyTextureInfo src = WGPU_TEXEL_COPY_TEXTURE_INFO_INIT;
	src.texture = d->target;
	src.aspect = WGPUTextureAspect_All;
	WGPUTexelCopyBufferInfo dst = WGPU_TEXEL_COPY_BUFFER_INFO_INIT;
	dst.buffer = d->read;
	dst.layout.offset = 0;
	dst.layout.bytesPerRow = d->row;
	dst.layout.rowsPerImage = d->ph;
	WGPUExtent3D size = {d->pw, d->ph, 1};
	wgpuCommandEncoderCopyTextureToBuffer(enc, &src, &dst, &size);
	WGPUCommandBuffer cmd = wgpuCommandEncoderFinish(enc, NULL);
	wgpuQueueSubmit(g->queue, 1, &cmd);
	wgpuCommandBufferRelease(cmd);
	wgpuCommandEncoderRelease(enc);
	d->mapped = 0;
	WGPUBufferMapCallbackInfo cb = WGPU_BUFFER_MAP_CALLBACK_INFO_INIT;
	cb.mode = WGPUCallbackMode_AllowSpontaneous;
	cb.callback = on_map;
	cb.userdata1 = d;
	wgpuBufferMapAsync(d->read, WGPUMapMode_Read, 0, (size_t)bd.size, cb);
}

const uint8_t *nr_draw_mapped(nr_draw *d) {
	return wgpuBufferGetConstMappedRange(d->read, 0, (size_t)d->row * d->ph);
}

void nr_draw_end(nr_draw *d) {
	if (d->read) {
		if (d->mapped == WGPUMapAsyncStatus_Success) wgpuBufferUnmap(d->read);
		wgpuBufferRelease(d->read);
	}
	if (d->view) wgpuTextureViewRelease(d->view);
	if (d->target) wgpuTextureRelease(d->target);
	if (d->group) wgpuBindGroupRelease(d->group);
	for (int i = 0; i < d->n; i++) {
		if (d->views[i]) wgpuTextureViewRelease(d->views[i]);
		if (d->samplers[i]) wgpuSamplerRelease(d->samplers[i]);
		if (d->texs[i]) wgpuTextureRelease(d->texs[i]);
	}
	free(d->views);
	free(d->samplers);
	free(d->texs);
	if (d->ubuf) wgpuBufferRelease(d->ubuf);
	memset(d, 0, sizeof *d);
}

int nr_poll(WGPUDevice d) {
	return wgpuDevicePoll(d, 0, NULL) ? 1 : 0;
}
