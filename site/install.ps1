$ErrorActionPreference = 'Stop'

$Repo = "agents-first/clawdchan"
$InstallDir = Join-Path $HOME ".clawdchan"
$BinDir = Join-Path $InstallDir "bin"

# 1. Resolve Latest Version
Write-Host "==> Resolving latest release..." -ForegroundColor Cyan
$Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$Tag = $Release.tag_name
# Remove 'v' prefix if present for the filename, but keep Tag for the URL
$Version = $Tag.TrimStart('v')

# 2. Determine Architecture
# GoReleaser uses x86_64 for amd64
$Is64 = [Environment]::Is64BitOperatingSystem
$Arch = if ($Is64) { "x86_64" } else { "i386" }
$Archive = "clawdchan_$($Version)_Windows_$($Arch).zip"
$Url = "https://github.com/$Repo/releases/download/$Tag/$Archive"

Write-Host "==> Installing ClawdChan $Tag ($Arch)..." -ForegroundColor Cyan
Write-Host "    $Url"

# 3. Download and Extract
$TmpDir = Join-Path $env:TEMP "clawdchan-install"
if (Test-Path $TmpDir) { Remove-Item $TmpDir -Recurse -Force }
New-Item -ItemType Directory -Path $TmpDir | Out-Null

$ZipPath = Join-Path $TmpDir $Archive
Write-Host "==> Downloading..." -ForegroundColor Cyan
Invoke-WebRequest -Uri $Url -OutFile $ZipPath

Write-Host "==> Extracting to $BinDir..." -ForegroundColor Cyan
if (!(Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }
Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force

$Binaries = @("clawdchan.exe", "clawdchan-mcp.exe", "clawdchan-relay.exe")
foreach ($Bin in $Binaries) {
    $Src = Join-Path $TmpDir $Bin
    if (Test-Path $Src) {
        Move-Item -Path $Src -Destination (Join-Path $BinDir $Bin) -Force
    } else {
        Write-Warning "Could not find $Bin in the archive."
    }
}

# 4. Update PATH
Write-Host "==> Updating PATH..." -ForegroundColor Cyan
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$BinDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$UserPath;$BinDir", "User")
    $env:Path += ";$BinDir"
    Write-Host "    Added $BinDir to User PATH."
} else {
    Write-Host "    $BinDir is already in PATH."
}

# 5. Run Setup
Write-Host "==> Running: clawdchan setup" -ForegroundColor Cyan
& (Join-Path $BinDir "clawdchan.exe") setup

Write-Host "`nClawdChan has been installed successfully!" -ForegroundColor Green
Write-Host "You may need to restart your terminal for PATH changes to take full effect."
