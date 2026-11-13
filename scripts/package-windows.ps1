# Build Banyan for Windows and put a portable zip and an installer in dist\.
# Banyan uses the Git the computer has; it does not bring one. Needs Inno
# Setup 6 (iscc) on the PATH.
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot)

$version = (Select-String -Path main.go -Pattern '^var version = "(.*)"').Matches[0].Groups[1].Value
New-Item -ItemType Directory -Force build | Out-Null
$env:CGO_ENABLED = "0"
# The executable's resources: its icon, which MyGo gives the windows and
# the taskbar (resource 1), a manifest and version information. Go links
# the .syso file of the main package in; it goes once built.
go run github.com/tc-hib/go-winres@v0.3.3 make --in packaging\windows\winres.json --out rsrc --arch amd64 `
    --product-version "$version.0" --file-version "$version.0"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
try {
    go build -trimpath -tags mygo_noinspector -ldflags "-s -w -H windowsgui -X main.version=$version -X github.com/egoist/mygo.production=1" -o build\banyan.exe .
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
} finally {
    Remove-Item -ErrorAction SilentlyContinue rsrc_windows_amd64.syso
}

New-Item -ItemType Directory -Force dist | Out-Null
Compress-Archive -Force -Path build\banyan.exe, README.md -DestinationPath "dist\Banyan-$version-windows-x64.zip"

iscc /Q "/DVersion=$version" "/DSource=$PWD\build" packaging\windows\banyan.iss
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
