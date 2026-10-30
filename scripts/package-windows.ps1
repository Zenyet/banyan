# Build Banyan for Windows and put a portable zip and an installer in dist\.
# Needs Inno Setup 6 (iscc) on the PATH.
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot)

$version = (Select-String -Path main.go -Pattern '^var version = "(.*)"').Matches[0].Groups[1].Value
New-Item -ItemType Directory -Force build | Out-Null
$env:CGO_ENABLED = "0"
go build -trimpath -tags mygo_noinspector -ldflags "-s -w -H windowsgui -X main.version=$version -X github.com/egoist/mygo.production=1" -o build\banyan.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

New-Item -ItemType Directory -Force dist | Out-Null
Compress-Archive -Force -Path build\banyan.exe, README.md -DestinationPath "dist\Banyan-$version-windows-x64.zip"

iscc /Q "/DVersion=$version" "/DSource=$PWD\build" packaging\windows\banyan.iss
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
