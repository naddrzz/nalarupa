# Builds the Nalarupa desktop application (MyGo Native UI + core-go linked in-process) into dist/.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
if (-not (Test-Path '..\core-go\shader\gpu\wgpu-native\windows_amd64\lib\libwgpu_native.a')) {
    throw 'wgpu-native is missing: run `go generate ./shader/gpu` in core-go first'
}
$version = (Get-Content -Raw mygo.json | ConvertFrom-Json).version
if (-not $version) { throw 'no version in mygo.json' }
if (Test-Path dist) { Remove-Item -Recurse -Force dist }
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = '1'
$env:MYGO_LDFLAGS = "-extldflags=-static -X main.appVersion=$version -X github.com/naddrzz/nalarupa/core-go/agent.version=$version"
go tool mygo build -skip-installer -o .build-tmp .
if ($LASTEXITCODE -ne 0) { throw 'mygo build failed' }
Copy-Item '.build-tmp\windows-amd64\Nalarupa.exe' 'dist\nalarupa.exe'
Remove-Item -Recurse -Force .build-tmp
Copy-Item 'assets\brand\nalarupa.ico' 'dist\nalarupa.ico'
Copy-Item '..\THIRD_PARTY_NOTICES.md' 'dist\THIRD_PARTY_NOTICES.md'
Write-Output "built $PSScriptRoot\dist\nalarupa.exe"
