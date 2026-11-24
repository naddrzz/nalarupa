#!/bin/sh
# Builds the headless CLI, out/nalarupa-cli (nalarupa-cli.exe on Windows), with quickjs-ng and wgpu-native
# linked in. Needs a C compiler (CC; MinGW gcc on Windows) and wgpu-native, which `go generate ./shader/gpu`
# fetches once.
set -eu
cd "$(dirname "$0")"
platform="$(go env GOOS)_$(go env GOARCH)"
if [ ! -f "shader/gpu/wgpu-native/$platform/lib/libwgpu_native.a" ]; then
  echo "wgpu-native is missing: run 'go generate ./shader/gpu' in core-go first" >&2
  exit 1
fi
case "$(go env GOOS)" in
windows)
  exe=.exe
  # -static links MinGW's own runtime (libgcc, winpthread) into the binary as well.
  ldflags='-s -w -extldflags=-static'
  ;;
darwin)
  exe=
  ldflags='-s -w'
  # The release supports macOS 13 and later.
  export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-13.0}"
  ;;
*)
  exe=
  ldflags='-s -w'
  ;;
esac
# The MCP server reports the app's version (packaging/set-version.sh stamps it into app/mygo.json).
version=$(sed -n 's/^  "version": "\([^"]*\)".*/\1/p' ../app/mygo.json)
ldflags="$ldflags -X github.com/naddrzz/nalarupa/core-go/agent.version=${version:?no version in app/mygo.json}"
mkdir -p out
out=out/nalarupa-cli$exe
CGO_ENABLED=1 go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/cli
echo "built $(pwd)/$out"
