# Builds the Go core as the app's shared library, out/nalarupa_core.dll, with quickjs-ng and wgpu-native linked
# in, so the one file ships. Needs MinGW gcc (CC, else gcc on PATH) and wgpu-native, which
# `go generate ./shader/gpu` fetches once. Run the app on it with NALARUPA_CORE=<full path of the dll>.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
if (-not (Test-Path 'shader/gpu/wgpu-native/windows_amd64/lib/libwgpu_native.a')) {
    throw 'wgpu-native is missing: run `go generate ./shader/gpu` in core-go first'
}
$env:CGO_ENABLED = '1'
# -static links MinGW's own runtime (libgcc, winpthread) into the dll as well.
go build -buildmode=c-shared -trimpath -ldflags '-s -w -extldflags=-static' -o out/nalarupa_core.dll ./cmd/core
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
Write-Output "built $PSScriptRoot\out\nalarupa_core.dll"
