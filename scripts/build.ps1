<#
.SYNOPSIS
  Checks, builds (with version, commit and date) and optionally publishes or packages gitsi.

.DESCRIPTION
  1) go vet + go test   (skip with -SkipTests; -Fast runs the tests with -short and the test cache)
  2) go build with -trimpath and -ldflags for version, commit and date, so 'gitsi version' tells which commit and date each
     binary comes from. -trimpath removes this machine's paths (user and folders) from the executable and makes the build
     reproducible; the date is the commit's, not today's, for the same reason. The symbol table is kept on purpose: it makes
     the binary more transparent to security tooling (see docs/SECURITY-REVIEW.md).
  3) -Publish runs 'gitsi update -publish' (on the HUB) so the edge can download it with 'gitsi update'.
  4) -Dist builds the release archives instead: dist\gitsi-<version>-windows-<arch>.zip (gitsi.exe, README.md, LICENSE)
     for amd64 and arm64, plus dist\SHA256SUMS.txt.

.EXAMPLE
  .\scripts\build.ps1                 # checks and builds bin\gitsi.exe
  .\scripts\build.ps1 -Publish        # ... and makes it available to the edge
  .\scripts\build.ps1 -Fast           # quick iteration: tests with -short (skips the slow ones) and the test cache
  .\scripts\build.ps1 -Dist           # release archives in dist\
#>
[CmdletBinding()]
param(
  [switch]$Publish,
  [switch]$Dist,
  [switch]$SkipTests,
  [switch]$Fast,
  [string]$Version = "",
  [string]$Go = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot          # repository root
Set-Location $root

if ($Publish -and $Dist) { throw "-Publish and -Dist do not combine: publish a local build, or package a release." }

function Resolve-Go {
  param([string]$Hint)
  $candidates = @()
  if ($Hint) { $candidates += $Hint }
  if ($env:GITSI_GO) { $candidates += $env:GITSI_GO }
  $cmd = Get-Command go -ErrorAction SilentlyContinue
  if ($cmd) { $candidates += $cmd.Source }
  foreach ($c in $candidates) {
    if (-not $c) { continue }
    try { & $c version *> $null; if ($LASTEXITCODE -eq 0) { return $c } } catch { }
  }
  throw "No working Go was found. Give its path with -Go or the GITSI_GO variable."
}

$go = Resolve-Go $Go
Write-Host "Go: $go"

if (-not $SkipTests) {
  Write-Host "== go vet"; & $go vet ./...
  if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
  $testArgs = @("./...")
  if ($Fast) { $testArgs += "-short" } else { $testArgs += "-count=1" }
  Write-Host "== go test $($testArgs -join ' ')"; & $go test @testArgs
  if ($LASTEXITCODE -ne 0) { throw "the tests failed" }
}

# Version: the given one, or the one in the code (internal\version\version.go)
if (-not $Version) {
  $m = Select-String -Path "internal\version\version.go" -Pattern 'Version = "([^"]+)"' | Select-Object -First 1
  if ($m) { $Version = $m.Matches[0].Groups[1].Value } else { $Version = "0.0.0-dev" }
}
if ($Version -notmatch '^[0-9A-Za-z.+-]+$') { throw "-Version is not a valid version: '$Version'" }

# Commit and date: only inside a git repo. The date is the commit's, so the same commit always builds the same binary.
$commit = ""
$date = ""
$prevEAP = $ErrorActionPreference
$ErrorActionPreference = "Continue"
try {
  $commit = (& git rev-parse --short HEAD 2>$null)
  if ($LASTEXITCODE -ne 0) { $commit = "" } else { $date = (& git log -1 --format=%cs 2>$null) }
} catch { $commit = "" } finally { $ErrorActionPreference = $prevEAP }
if (-not $date) { $date = Get-Date -Format "yyyy-MM-dd" }

$pkg = "github.com/ShizukaJiku/gitsi/internal/version"
$ld = "-X $pkg.Version=$Version -X $pkg.Date=$date"
if ($commit) { $ld += " -X $pkg.Commit=$commit" }

function Build-Gitsi {
  param([string]$Arch, [string]$Out)
  $prevOS, $prevArch, $prevCgo = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
  try {
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = "windows", $Arch, "0"
    & $go build -trimpath -ldflags $ld -o $Out .\cmd\gitsi
    if ($LASTEXITCODE -ne 0) { throw "go build failed ($Arch)" }
  } finally {
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $prevOS, $prevArch, $prevCgo
  }
}

if ($Dist) {
  $distDir = Join-Path $root "dist"
  if (Test-Path $distDir) { Remove-Item -Recurse -Force $distDir }
  New-Item -ItemType Directory -Force $distDir | Out-Null
  $sums = @()
  foreach ($arch in @("amd64", "arm64")) {
    $name = "gitsi-$Version-windows-$arch"
    $stage = Join-Path $distDir $name
    New-Item -ItemType Directory -Force $stage | Out-Null
    Write-Host "== go build windows/$arch ($Version, $date, commit '$commit')"
    Build-Gitsi $arch (Join-Path $stage "gitsi.exe")
    Copy-Item README.md, LICENSE $stage
    $zip = Join-Path $distDir "$name.zip"
    Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zip
    Remove-Item -Recurse -Force $stage
    $hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
    $sums += "$hash  $name.zip"
    Write-Host "Packaged:  dist\$name.zip  $hash"
  }
  # LF line endings and no BOM, as sha256sum -c expects
  [IO.File]::WriteAllText((Join-Path $distDir "SHA256SUMS.txt"), (($sums -join "`n") + "`n"))
  Write-Host "Checksums: dist\SHA256SUMS.txt"
  return
}

New-Item -ItemType Directory -Force bin | Out-Null
Write-Host "== go build ($Version, $date, commit '$commit')"
$arch = (& $go env GOARCH)
Build-Gitsi $arch "bin\gitsi.exe"

$hash = (Get-FileHash bin\gitsi.exe -Algorithm SHA256).Hash
Write-Host ("Built:     bin\gitsi.exe  " + (& .\bin\gitsi.exe version))
Write-Host ("Size:      {0:N2} MB" -f ((Get-Item bin\gitsi.exe).Length / 1MB))
Write-Host "SHA256:    $hash"

if ($Publish) {
  Write-Host "== gitsi update -publish"
  & .\bin\gitsi.exe update -publish
  if ($LASTEXITCODE -ne 0) { throw "update -publish failed (it runs on the HUB, after 'gitsi setup hub')" }
  Write-Host "On the edge: gitsi update"
}
