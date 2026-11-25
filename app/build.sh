#!/bin/sh
# Builds the Nalarupa desktop application (MyGo Native UI + core-go linked in-process) into dist/.
set -eu
cd "$(dirname "$0")"
platform="$(go env GOOS)_$(go env GOARCH)"
if [ ! -f "../core-go/shader/gpu/wgpu-native/$platform/lib/libwgpu_native.a" ]; then
  echo "wgpu-native is missing: run 'go generate ./shader/gpu' in core-go first" >&2
  exit 1
fi
version=$(sed -n 's/^  "version": "\([^"]*\)".*/\1/p' mygo.json)
version="${version:?no version in mygo.json}"
ldflags="-s -w -X main.appVersion=$version -X github.com/egoist/mygo.packageName=Nalarupa -X github.com/egoist/mygo.packageVersion=$version -X github.com/egoist/mygo.packageIdentifier=io.github.naddrzz.nalarupa -X github.com/naddrzz/nalarupa/core-go/agent.version=$version"
tags=""
if [ "${NALARUPA_RELEASE:-}" = "1" ]; then
  ldflags="$ldflags -X github.com/egoist/mygo.production=1"
  tags="-tags mygo_noinspector"
fi
rm -rf dist
mkdir -p dist
case "$(go env GOOS)" in
windows)
  exe=dist/nalarupa.exe
  ldflags="$ldflags -H=windowsgui -extldflags=-static"
  cp assets/brand/nalarupa.ico dist/nalarupa.ico
  ;;
darwin)
  exe=dist/nalarupa
  export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-13.0}"
  ;;
*)
  exe=dist/nalarupa
  ;;
esac
# shellcheck disable=SC2086
CGO_ENABLED=1 go build -trimpath $tags -ldflags "$ldflags" -o "$exe" .
cp ../THIRD_PARTY_NOTICES.md dist/THIRD_PARTY_NOTICES.md
echo "built $(pwd)/$exe"
