# sluice installer for Windows (PowerShell 5.1+).
#
#   irm https://raw.githubusercontent.com/jiawei42/sluice/main/install.ps1 | iex
#
# What it does: downloads sluice_windows_<arch>.zip and checksums.txt from the
# GitHub release, verifies the SHA-256, copies sluice.exe to
# %LOCALAPPDATA%\Programs\sluice and adds that folder to your *user* PATH.
# No admin rights, no telemetry.
#
# Options (environment variables):
#   SLUICE_VERSION        release tag, e.g. v1.0.0 (default: latest)
#   SLUICE_INSTALL_DIR    target directory
#   SLUICE_NO_PATH=1      do not modify the user PATH
#   SLUICE_DOWNLOAD_BASE  alternative download URL (mirrors, testing)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'jiawei42/sluice'
$version = if ($env:SLUICE_VERSION) { $env:SLUICE_VERSION } else { 'latest' }
$installDir = if ($env:SLUICE_INSTALL_DIR) { $env:SLUICE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\sluice' }
$base = $env:SLUICE_DOWNLOAD_BASE
if (-not $base) {
    if ($version -eq 'latest') { $base = "https://github.com/$repo/releases/latest/download" }
    else { $base = "https://github.com/$repo/releases/download/$version" }
}

$arch = 'amd64'
try {
    if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { $arch = 'arm64' }
} catch { if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { $arch = 'arm64' } }

$file = "sluice_windows_$arch.zip"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("sluice-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Write-Host "sluice-install: downloading $file ($version)"
    $zip = Join-Path $tmp $file
    $sums = Join-Path $tmp 'checksums.txt'
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$file" -OutFile $zip
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $sums

    $expected = $null
    foreach ($line in Get-Content $sums) {
        $parts = $line -split '\s+'
        if ($parts.Count -ge 2 -and $parts[1] -eq $file) { $expected = $parts[0].ToLower() }
    }
    if (-not $expected) { throw "$file is not listed in checksums.txt" }
    $actual = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLower()
    if ($expected -ne $actual) { throw "checksum mismatch for $file (expected $expected, got $actual)" }
    Write-Host 'sluice-install: checksum OK'

    $extract = Join-Path $tmp 'x'
    Expand-Archive -Path $zip -DestinationPath $extract -Force
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Copy-Item -Path (Join-Path $extract 'sluice.exe') -Destination (Join-Path $installDir 'sluice.exe') -Force
    Write-Host "sluice-install: installed $installDir\sluice.exe"

    if ($env:SLUICE_NO_PATH -ne '1') {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $entries = @()
        if ($userPath) { $entries = $userPath -split ';' | Where-Object { $_ } }
        if ($entries -notcontains $installDir) {
            [Environment]::SetEnvironmentVariable('Path', (($entries + $installDir) -join ';'), 'User')
            Write-Host "sluice-install: added $installDir to your user PATH (open a new terminal to use it)"
        }
    }
    & (Join-Path $installDir 'sluice.exe') version
    Write-Host 'sluice-install: next: sluice init github   (see: sluice presets)'
}
finally {
    Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
}
