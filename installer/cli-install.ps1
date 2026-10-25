# RunOS CLI installer for Windows.
#
# This file is STATIC. The RunOS API serves it unchanged at /cli.ps1, the same file for everyone.
#
#   irm https://api.runos.com/cli.ps1 | iex
#   $RunOSRelease = 'v1.2.3'; irm https://api.runos.com/cli.ps1 | iex
#
# $env:RUNOS_VERSION does the same as $RunOSRelease. The release is checked against the release
# tag shape before anything runs. The installer downloads the exact release archive from GitHub
# Releases and installs it only when its sha256 matches the digest in the RunOS validated
# registry, which is written only after the release's build-provenance attestation is verified.
#
# The source of truth for this file is the CLI repository (installer/cli-install.ps1). The RunOS
# API ships a copy pinned at its build, and a test fails when the copy and this file differ.

$ErrorActionPreference = "Stop"

# --- The release pin: checked before anything runs ---------------------------------------------

$tagPattern = '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
$pin = ""
if (Get-Variable -Name RunOSRelease -ErrorAction SilentlyContinue) {
    $pin = [string]$RunOSRelease
} elseif (-not [string]::IsNullOrEmpty($env:RUNOS_VERSION)) {
    $pin = [string]$env:RUNOS_VERSION
}
if (-not [string]::IsNullOrEmpty($pin)) {
    if (-not $pin.StartsWith("v")) { $pin = "v$pin" }
    if ($pin -cnotmatch $tagPattern) {
        # Never echo the refused value.
        Write-Host "RunOS CLI installer: the release is malformed (expected a release tag such as v1.2.3)." -ForegroundColor Red
        Write-Host "Nothing was installed." -ForegroundColor Red
        exit 2
    }
}

Write-Host "RunOS CLI Installer" -ForegroundColor Green
Write-Host ""

# The validated digest registry (public read). Only the release validator writes it.
$registryBase = "https://runoscdn.com/cli/validated"

# Detect architecture
$arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else { "amd64" }
$platform = "windows"

# Abort fails closed: prints a clear reason and exits without installing anything.
function Abort-Install([string]$message) {
    Write-Host ""
    Write-Host "Installation aborted: $message" -ForegroundColor Red
    exit 1
}

# Resolve the version (tag) to install: the pin, else the validated current.json pointer. A pin
# with no validated digest yet fails closed with "version pending verification".
$pinned = $false
$tag = ""
if (-not [string]::IsNullOrEmpty($pin)) {
    $tag = $pin
    $pinned = $true
} else {
    try {
        $current = Invoke-RestMethod -Uri "$registryBase/current.json" -TimeoutSec 30
    } catch {
        Abort-Install "could not resolve the current validated version"
    }
    $tag = [string]$current.version
    if ([string]::IsNullOrEmpty($tag)) {
        Abort-Install "could not parse the current validated version"
    }
    if ($tag -cnotmatch $tagPattern) {
        Abort-Install "the registry named a malformed version"
    }
}

# Fetch the validated digest for this exact tag + platform.
$digestUrl = "$registryBase/$tag/runos-$platform-$arch.sha256"
$expected = ""
try {
    $resp = Invoke-WebRequest -Uri $digestUrl -UseBasicParsing -TimeoutSec 30
    $expected = ([string]$resp.Content).Trim().ToLower()
} catch {
    $expected = ""
}
if ([string]::IsNullOrEmpty($expected)) {
    if ($pinned) {
        Abort-Install "version $tag is pending verification (no validated digest yet)"
    }
    Abort-Install "verification failed: no validated digest for $tag ($platform-$arch)"
}
if ($expected -cnotmatch '^[0-9a-f]{64}$') {
    Abort-Install "verification failed: the registry digest for $tag is malformed"
}

# Install directory
$installDir = "$env:LOCALAPPDATA\RunOS\bin"
if (!(Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}

# Download the archive by EXACT tag into a temp dir (never Expand-Archive'd
# before verification).
$archiveUrl = "https://github.com/runos-official/cli/releases/download/$tag/runos-$platform-$arch.zip"
$tmpDir = Join-Path $env:TEMP "runos-install"
if (Test-Path $tmpDir) { Remove-Item -Recurse -Force $tmpDir }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
$archivePath = "$tmpDir\runos.zip"

Write-Host "Downloading RunOS CLI $tag from GitHub..." -ForegroundColor Yellow
Write-Host "  Source: $archiveUrl"
Write-Host "  View source code: https://github.com/runos-official/cli"
Write-Host ""
try {
    Invoke-WebRequest -Uri $archiveUrl -OutFile $archivePath -UseBasicParsing
} catch {
    Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
    Abort-Install "could not download the release archive for $tag"
}

# Verify the download against the validated digest. Fail closed.
$computed = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLower()
if ($computed -ne $expected) {
    Write-Host "  expected: $expected" -ForegroundColor Yellow
    Write-Host "  computed: $computed" -ForegroundColor Yellow
    Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
    Abort-Install "verification failed: archive digest does not match the validated digest"
}
Write-Host "Verified build provenance (sha256 matches the validated registry)." -ForegroundColor Green

# Verified: extract + install.
Expand-Archive -Path $archivePath -DestinationPath $tmpDir -Force
Copy-Item "$tmpDir\runos.exe" "$installDir\runos.exe" -Force
Remove-Item -Recurse -Force $tmpDir

Write-Host "Installed to $installDir\runos.exe" -ForegroundColor Green

# Add to PATH if not already there
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
    Write-Host "Added $installDir to PATH" -ForegroundColor Yellow
}

$version = & "$installDir\runos.exe" version 2>$null
Write-Host ""
Write-Host "============================================================" -ForegroundColor Green
Write-Host "  RunOS CLI Installation Complete" -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Green
Write-Host ""
if ($version) {
    Write-Host "  Version:     $version" -ForegroundColor Yellow
    Write-Host ""
}
Write-Host "  Next steps:" -ForegroundColor Yellow
Write-Host "    1. Open a new terminal window"
Write-Host "    2. Run 'runos login' to sign in"
Write-Host "    3. Run 'runos mcp configure claude' in your project directory"
Write-Host ""
Write-Host "  You can close this window now." -ForegroundColor DarkGray
Write-Host ""
