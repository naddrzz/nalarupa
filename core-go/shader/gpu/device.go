package gpu

/*
#include "glue.h"
*/
import "C"

import (
	"errors"
	"os"
	"strings"
	"sync"
	"unsafe"
)

// device is the offscreen GPU (shader.rs `Gpu`) with its compiled modules and pipelines, by source key.
type device struct {
	g       C.nr_gpu
	modules map[uint64]C.WGPUShaderModule
	pipes   map[uint64]*C.nr_pipe
	adapter string
	// backend is wgpu's name for the adapter's backend; cpu marks a software rasterizer (WARP, llvmpipe).
	backend string
	cpu     bool
}

// state is shader.rs `GPU`: nil until first use, then the device or the error that kept it from starting
// (cached for good). mu is held for all GPU work.
var (
	mu    sync.Mutex
	state *struct {
		dev *device
		err error
	}
)

// current is the device, started if needed. mu must be held.
func current() (*device, error) {
	if state == nil {
		d, err := start()
		state = &struct {
			dev *device
			err error
		}{d, err}
	}
	return state.dev, state.err
}

// reset drops the device (lost or timed out), so the next use starts a fresh one. mu must be held.
func reset() {
	if state != nil && state.dev != nil {
		state.dev.release()
	}
	state = nil
}

func cstr(p *C.char) string {
	s := C.GoString(p)
	C.free(unsafe.Pointer(p))
	return s
}

// start is shader.rs `gpu_init`: the backends and power preference from WGPU_BACKEND and WGPU_POWER_PREF
// (else the primary backends at low power), and the backends' software rasterizers (WARP, lavapipe) when
// no hardware adapter is found.
func start() (*device, error) {
	d := &device{modules: map[uint64]C.WGPUShaderModule{}, pipes: map[uint64]*C.nr_pipe{}}
	backends, uncompiled := backendsFromEnv()
	if backends == 0 {
		// No backend wgpu-native would start: the Rust core's instance has none either.
		return nil, errors.New("no GPU adapter for shader fills: " + noBackends(uncompiled))
	}
	if msg := C.nr_instance(&d.g, backends); msg != nil {
		return nil, errors.New("no GPU adapter for shader fills: " + cstr(msg))
	}
	power := C.WGPUPowerPreference(C.WGPUPowerPreference_LowPower)
	switch strings.ToLower(os.Getenv("WGPU_POWER_PREF")) {
	case "high":
		power = C.WGPUPowerPreference_HighPerformance
	case "none":
		power = C.WGPUPowerPreference_Undefined
	}
	if msg := C.nr_adapter(&d.g, power, 0); msg != nil {
		C.free(unsafe.Pointer(msg))
		if msg := C.nr_adapter(&d.g, power, 1); msg != nil {
			d.release()
			return nil, errors.New("no GPU adapter for shader fills: " + adapterMessage(cstr(msg), uncompiled))
		}
	}
	var backend, kind C.int
	name := cstr(C.nr_adapter_name(&d.g, &backend, &kind))
	d.cpu = kind == C.WGPUAdapterType_CPU
	if msg := C.nr_device(&d.g); msg != nil {
		d.release()
		return nil, errors.New("GPU device for shader fills: " + unwrap(cstr(msg)))
	}
	d.backend = backendName(backend)
	d.adapter = name + " (" + d.backend + ")"
	return d, nil
}

func (d *device) release() {
	for _, p := range d.pipes {
		C.nr_pipe_release(p)
		C.free(unsafe.Pointer(p))
	}
	for _, m := range d.modules {
		C.wgpuShaderModuleRelease(m)
	}
	d.pipes, d.modules = nil, nil
	C.nr_release(&d.g)
}

// wgpu-native's `WGPUInstanceBackend` bits (static consts in its header, which cgo cannot read).
const (
	backendVulkan  = 1 << 0
	backendMetal   = 1 << 2
	backendDX12    = 1 << 3
	backendWebGPU  = 1 << 5
	backendPrimary = backendVulkan | backendMetal | backendDX12 | backendWebGPU
)

// backendsFromEnv is wgpu's `Backends::from_env().unwrap_or(Backends::PRIMARY)` (`from_comma_list`), with
// the requested backends the Rust core builds wgpu without (noop, gl) left out and listed in uncompiled:
// wgpu-native has GL and no noop bit, so neither is handed to it.
func backendsFromEnv() (b C.WGPUInstanceBackend, uncompiled []string) {
	env, ok := os.LookupEnv("WGPU_BACKEND")
	if !ok {
		return backendPrimary, nil
	}
	for _, name := range strings.Split(strings.ToLower(env), ",") {
		switch strings.TrimSpace(name) {
		case "vulkan", "vk":
			b |= backendVulkan
		case "dx12", "d3d12":
			b |= backendDX12
		case "metal", "mtl":
			b |= backendMetal
		case "noop":
			uncompiled = append(uncompiled, "noop")
		case "opengl", "gles", "gl":
			uncompiled = append(uncompiled, "gl")
		case "webgpu":
			b |= backendWebGPU
		}
	}
	return b, uncompiled
}

// backendName is wgpu's `Backend` Debug name.
func backendName(b C.int) string {
	switch b {
	case C.WGPUBackendType_Vulkan:
		return "Vulkan"
	case C.WGPUBackendType_D3D12:
		return "Dx12"
	case C.WGPUBackendType_Metal:
		return "Metal"
	case C.WGPUBackendType_OpenGL, C.WGPUBackendType_OpenGLES:
		return "Gl"
	case C.WGPUBackendType_WebGPU:
		return "BrowserWebGpu"
	}
	return "Noop"
}

// Adapter is the name of the adapter shader fills run on, starting the device if needed (shader.rs
// `adapter`).
func Adapter() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	d, err := current()
	if err != nil {
		return "", err
	}
	return d.adapter, nil
}
