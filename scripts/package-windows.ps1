# Build Banyan for Windows and put a portable zip and an installer in dist\,
# both with MinGit beside the app. Needs Inno Setup 6 (iscc) on the PATH.
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

# MinGit, Git for Windows' own minimal Git, goes beside banyan.exe in
# git\, which Banyan runs when the computer has no Git. Its licenses come
# along in it, unchanged.
$minGitVersion = "2.56.0.windows.4"
$minGitZip = "MinGit-2.56.0.4-64-bit.zip"
$minGitSha256 = "e3bf78351fe777fba47ddee5950082f8a98bb0f02604fdc7ba3ee2cb68ffa877"
$ProgressPreference = "SilentlyContinue"
$download = "build\$minGitZip"
if (-not (Test-Path $download)) {
    # GitHub's downloads now and then time out (504); try a few times.
    $url = "https://github.com/git-for-windows/git/releases/download/v$minGitVersion/$minGitZip"
    for ($attempt = 1; ; $attempt++) {
        try {
            Invoke-WebRequest -Uri $url -OutFile $download
            break
        } catch {
            Remove-Item -ErrorAction SilentlyContinue $download
            if ($attempt -ge 5) { throw }
            Write-Host "Downloading $minGitZip failed ($($_.Exception.Message)); retrying in $($attempt * 15) s"
            Start-Sleep -Seconds ($attempt * 15)
        }
    }
}
$hash = (Get-FileHash -Algorithm SHA256 $download).Hash.ToLower()
if ($hash -ne $minGitSha256) {
    Remove-Item $download
    throw "$minGitZip has the checksum $hash, not $minGitSha256"
}
if (Test-Path build\git) { Remove-Item -Recurse -Force build\git }
Expand-Archive -Path $download -DestinationPath build\git

New-Item -ItemType Directory -Force dist | Out-Null
Compress-Archive -Force -Path build\banyan.exe, build\git, README.md -DestinationPath "dist\Banyan-$version-windows-x64.zip"

iscc /Q "/DVersion=$version" "/DSource=$PWD\build" packaging\windows\banyan.iss
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
