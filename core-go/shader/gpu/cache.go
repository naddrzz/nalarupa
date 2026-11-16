package gpu

/*
#include "glue.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// Compile checks a transpiled shader with naga inside wgpu-native (the GPU half of shader.rs `compile`),
// starting the device if needed, and keeps the module for its draws. key identifies the source (its
// FxHash, as shader.rs keys `pipes`). checked is false when the device could not start, so naga never saw
// the shader; err is then the device's error.
func Compile(key uint64, glsl string) (checked bool, err error) {
	mu.Lock()
	defer mu.Unlock()
	d, err := current()
	if err != nil {
		return false, err
	}
	_, err = d.module(key, glsl)
	return true, err
}

// module is the fragment module of glsl, compiled once per device. Failures are not kept: the caller
// caches them with the program.
func (d *device) module(key uint64, glsl string) (C.WGPUShaderModule, error) {
	if m, ok := d.modules[key]; ok {
		return m, nil
	}
	dev := d.g.device
	C.nr_push_scope(dev)
	var m C.WGPUShaderModule
	if len(glsl) > 0 {
		m = C.nr_glsl(dev, (*C.char)(unsafe.Pointer(unsafe.StringData(glsl))), C.size_t(len(glsl)))
	} else {
		m = C.nr_glsl(dev, nil, 0)
	}
	if msg := C.nr_pop_scope(dev); msg != nil {
		C.wgpuShaderModuleRelease(m)
		return nil, moduleError(cstr(msg), glsl)
	}
	d.modules[key] = m
	return m, nil
}

// pipeline is shader.rs `pipeline`: the render pipeline and bind group layout of a shader with samplers
// texture/sampler pairs, made once per device.
func (d *device) pipeline(key uint64, glsl string, samplers int) (*C.nr_pipe, error) {
	if p, ok := d.pipes[key]; ok {
		return p, nil
	}
	fs, err := d.module(key, glsl)
	if err != nil {
		return nil, err
	}
	p := (*C.nr_pipe)(C.calloc(1, C.size_t(unsafe.Sizeof(C.nr_pipe{}))))
	if p == nil {
		return nil, errors.New("out of memory")
	}
	C.nr_push_scope(d.g.device)
	C.nr_pipeline(&d.g, fs, C.int(samplers), p)
	if msg := C.nr_pop_scope(d.g.device); msg != nil {
		C.nr_pipe_release(p)
		C.free(unsafe.Pointer(p))
		return nil, compileError(cstr(msg))
	}
	d.pipes[key] = p
	return p, nil
}
