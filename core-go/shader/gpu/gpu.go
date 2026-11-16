// Package gpu runs shader fills on the GPU through wgpu-native (the GPU half of shader.rs): an offscreen
// device made on first use, GLSL 450 fragment shaders compiled by naga inside wgpu-native, and a tiled
// full-screen draw read back as premultiplied pixels. One process-wide mutex serialises all GPU work.
//
// The pinned wgpu-native release is linked statically; `go generate ./shader/gpu` fetches it into
// wgpu-native/<goos>_<goarch>/. On Windows it is the MinGW (gnu) build: the system DLLs it calls through the
// windows crates (D3D12, DXGI, the shader compilers) come with import libraries inside the archive or are
// loaded at run time, so only the Rust standard library's imports are named here.
package gpu

//go:generate go run fetch_wgpu.go

/*
#cgo windows,amd64 CFLAGS: -I${SRCDIR}/wgpu-native/windows_amd64/include
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/wgpu-native/windows_amd64/lib -lwgpu_native -lws2_32 -luserenv -lntdll
#cgo linux,amd64 CFLAGS: -I${SRCDIR}/wgpu-native/linux_amd64/include
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/wgpu-native/linux_amd64/lib -lwgpu_native -lm -ldl -lpthread
#include "glue.h"
*/
import "C"
