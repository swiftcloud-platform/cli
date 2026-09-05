<#
.SYNOPSIS
  Install the SwiftCloud CLI on Windows.

.DESCRIPTION
  irm https://cloud.co.zm/install.ps1 | iex

  Downloads the latest release for this architecture from GitHub, verifies its
  SHA-256 against the release's own checksums.txt, and installs cloud.exe to
  %LOCALAPPDATA%\Programs\cloud, adding that directory to your PATH.

  Installing per-user rather than into Program Files is deliberate: it needs no
  administrator, and `cloud update` can then replace the binary in place
  without one either.

.PARAMETER Version
  A specific tag to install, e.g. v1.2.3. Defaults to the latest release.
  Also read from the CLOUD_VERSION environment variable.

.PARAMETER InstallDir
  Where to put cloud.exe. Defaults to %LOCALAPPDATA%\Programs\cloud, or
  CLOUD_INSTALL_DIR if set.

.EXAMPLE
  irm https://cloud.co.zm/install.ps1 | iex

.EXAMPLE
  # A specific version, to a directory of your choosing
  $env:CLOUD_VERSION = 'v1.2.3'; irm https://cloud.co.zm/install.ps1 | iex
#>
[CmdletBinding()]
param(
    [string]$Version = $env:CLOUD_VERSION,
    [string]$InstallDir = $env:CLOUD_INSTALL_DIR
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo = 'swiftcloud-platform/cli'

function Say($msg) { Write-Host $msg }
function Die($msg) { Write-Error "install.ps1: $msg"; exit 1 }

# TLS 1.2 is not the default on Windows PowerShell 5.1, and GitHub requires it.
if ([Net.ServicePointManager]::SecurityProtocol -notmatch 'Tls12') {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}

# ── architecture ────────────────────────────────────────────────────────────
# PROCESSOR_ARCHITECTURE reports the *process* architecture, which is x86 for a
# 32-bit shell on 64-bit Windows; OSArchitecture is what we actually want.
$osArch = (Get-CimInstance Win32_OperatingSystem -ErrorAction SilentlyContinue).OSArchitecture
switch -Wildcard ($osArch) {
    '*ARM*64*' { $arch = 'arm64' }
    '*64*'     { $arch = 'amd64' }
    default {
        # Fall back to the environment when CIM is unavailable.
        $arch = switch ($env:PROCESSOR_ARCHITECTURE) {
            'AMD64' { 'amd64' }
            'ARM64' { 'arm64' }
            default { Die "unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
        }
    }
}

if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\cloud' }

# ── resolve the version ─────────────────────────────────────────────────────
if (-not $Version) {
    try {
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" `
            -Headers @{ 'User-Agent' = 'swiftcloud-cli-installer' }
        $Version = $rel.tag_name
    } catch {
        Die "could not determine the latest version: $($_.Exception.Message)"
    }
}
if (-not $Version) { Die 'could not determine the latest version' }
$ver = $Version.TrimStart('v')

$base    = "https://github.com/$Repo/releases/download/$Version"
$archive = "cloud_${ver}_windows_${arch}.zip"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("cloud-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
    Say "Downloading cloud $Version for windows/$arch…"
    $zip = Join-Path $tmp $archive
    $sums = Join-Path $tmp 'checksums.txt'
    try {
        Invoke-WebRequest -Uri "$base/$archive" -OutFile $zip -UseBasicParsing
        Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sums -UseBasicParsing
    } catch {
        Die "download failed: $($_.Exception.Message)"
    }

    # ── verify before anything is written outside the temp directory ────────
    $line = Select-String -Path $sums -Pattern ([regex]::Escape($archive) + '$') | Select-Object -First 1
    if (-not $line) { Die "no checksum for $archive in checksums.txt" }
    $expected = ($line.Line -split '\s+')[0].ToLower()
    $actual = (Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLower()
    if ($expected -ne $actual) {
        Die "checksum mismatch for $archive (expected $expected, got $actual) — nothing was installed"
    }

    Expand-Archive -Path $zip -DestinationPath (Join-Path $tmp 'x') -Force
    $exe = Join-Path $tmp 'x\cloud.exe'
    if (-not (Test-Path $exe)) { Die "the release archive contains no cloud.exe" }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $target = Join-Path $InstallDir 'cloud.exe'
    try {
        Copy-Item -Path $exe -Destination $target -Force
    } catch {
        Die "could not write $target — close any running cloud.exe and try again: $($_.Exception.Message)"
    }

    # ── PATH ────────────────────────────────────────────────────────────────
    # The user's own PATH, so no administrator is needed. A fresh terminal
    # picks it up; this session gets it immediately.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    $already = $userPath -split ';' | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }
    if (-not $already) {
        $newPath = if ($userPath.TrimEnd(';')) { $userPath.TrimEnd(';') + ';' + $InstallDir } else { $InstallDir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Say "Added $InstallDir to your PATH — open a new terminal for it to take effect elsewhere."
    }
    if (($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }).Count -eq 0) {
        $env:Path = "$env:Path;$InstallDir"
    }

    Say "Installed: $(& $target version)"
    Say 'Next: cloud login'
} finally {
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
