#!/bin/sh
# Builds the Go core as the app's shared library, out/libnalarupa_core.so (out/nalarupa_core.dll on Windows),
# with quickjs-ng and wgpu-native linked in, so the one file ships. Needs a C compiler (CC; MinGW gcc on Windows)
# and wgpu-native, which `go generate ./shader/gpu` fetches once. Run the app on it with
# NALARUPA_CORE=<full path of the library>.
set -eu
cd "$(dirname "$0")"
platform="$(go env GOOS)_$(go env GOARCH)"
if [ ! -f "shader/gpu/wgpu-native/$platform/lib/libwgpu_native.a" ]; then
  echo "wgpu-native is missing: run 'go generate ./shader/gpu' in core-go first" >&2
  exit 1
fi
if [ "$(go env GOOS)" = windows ]; then
  out=out/nalarupa_core.dll
  # -static links MinGW's own runtime (libgcc, winpthread) into the dll as well.
  ldflags='-s -w -extldflags=-static'
else
  out=out/libnalarupa_core.so
  ldflags='-s -w'
fi
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags "$ldflags" -o "$out" ./cmd/core
echo "built $(pwd)/$out"
