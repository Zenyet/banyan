# Build Banyan for Windows and put a portable zip and an installer in dist\.
# Needs Inno Setup 6 (iscc) on the PATH.
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot)

$version = (Select-String -Path Cargo.toml -Pattern '^version = "(.*)"').Matches[0].Groups[1].Value
cargo build --release
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

New-Item -ItemType Directory -Force dist | Out-Null
Compress-Archive -Force -Path target\release\banyan.exe, README.md -DestinationPath "dist\Banyan-$version-windows-x64.zip"

iscc /Q "/DVersion=$version" "/DSource=$PWD\target\release" packaging\windows\banyan.iss
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
