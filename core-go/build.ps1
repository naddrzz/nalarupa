# Builds the headless CLI, out/nalarupa-cli.exe, with quickjs-ng and wgpu-native linked in.
# Needs MinGW gcc (CC, else gcc on PATH) and wgpu-native, which `go generate ./shader/gpu` fetches once.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
if (-not (Test-Path 'shader/gpu/wgpu-native/windows_amd64/lib/libwgpu_native.a')) {
    throw 'wgpu-native is missing: run `go generate ./shader/gpu` in core-go first'
}
# The MCP server reports the app's version (packaging/set-version.sh stamps it into app/mygo.json).
$version = (Get-Content -Raw ../app/mygo.json | ConvertFrom-Json).version
if (-not $version) { throw 'no version in app/mygo.json' }
$env:CGO_ENABLED = '1'
New-Item -ItemType Directory -Force out | Out-Null
# -static links MinGW's own runtime (libgcc, winpthread) into the binary as well.
go build -trimpath -ldflags "-s -w -extldflags=-static -X github.com/naddrzz/nalarupa/core-go/agent.version=$version" -o out/nalarupa-cli.exe ./cmd/cli
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
Write-Output "built $PSScriptRoot\out\nalarupa-cli.exe"
