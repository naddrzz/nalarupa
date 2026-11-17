#!/bin/sh
# Builds the Go core as the app's shared library, out/libnalarupa_core.so (out/nalarupa_core.dll on Windows,
# out/libnalarupa_core.dylib on macOS), with quickjs-ng and wgpu-native linked in, so the one file ships. Needs a
# C compiler (CC; MinGW gcc on Windows) and wgpu-native, which `go generate ./shader/gpu` fetches once. The app
# finds the library in out/ on its own; NALARUPA_CORE=<full path of the library> points it elsewhere.
# `build.sh cli` builds the headless CLI instead, out/nalarupa-cli (nalarupa-cli.exe on Windows), with the same flags.
set -eu
cd "$(dirname "$0")"
platform="$(go env GOOS)_$(go env GOARCH)"
if [ ! -f "shader/gpu/wgpu-native/$platform/lib/libwgpu_native.a" ]; then
  echo "wgpu-native is missing: run 'go generate ./shader/gpu' in core-go first" >&2
  exit 1
fi
case "$(go env GOOS)" in
windows)
  out=out/nalarupa_core.dll exe=.exe
  # -static links MinGW's own runtime (libgcc, winpthread) into the dll as well.
  ldflags='-s -w -extldflags=-static'
  ;;
darwin)
  out=out/libnalarupa_core.dylib exe=
  ldflags='-s -w'
  # The release supports macOS 13 and later.
  export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-13.0}"
  ;;
*)
  out=out/libnalarupa_core.so exe=
  ldflags='-s -w'
  ;;
esac
# The MCP server reports the app's version (packaging/set-version.sh stamps it into app/package.json).
version=$(sed -n 's/^  "version": "\([^"]*\)".*/\1/p' ../app/package.json)
ldflags="$ldflags -X github.com/naddrzz/nalarupa/core-go/agent.version=${version:?no version in app/package.json}"
if [ "${1:-}" = cli ]; then
  out=out/nalarupa-cli$exe
  CGO_ENABLED=1 go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/cli
else
  CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags "$ldflags" -o "$out" ./cmd/core
fi
echo "built $(pwd)/$out"
