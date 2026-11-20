<#
.SYNOPSIS
  Runs gitsi's tests with the race detector (-race) inside a Linux Docker container.

.DESCRIPTION
  On Windows, 'go test -race' needs a C compiler (cgo), and the detector's runtime fails on some system builds. On Linux it
  just works, so the official Go image is used:
    - the code is mounted READ-ONLY (nothing in the project is modified)
    - the build cache lives inside the container, which is removed at the end (--rm)
  Every package runs. The few tests that depend on Windows paths (C:\ProgramData, USERPROFILE) skip on Linux with an explicit
  reason (t.Skip), so the expected result is clean: no failures and no "DATA RACE".

  The "no races" result is only given when tests really ran: a build error, a Docker error, a missing package or a -Run that
  matches nothing ends with an error, not with a false "all good".

.EXAMPLE
  .\scripts\race.ps1                 # the whole project, once
  .\scripts\race.ps1 -Count 40 -Run "Put|Resume" -Package ./internal/mailbox
#>
[CmdletBinding()]
param(
  [string]$Package = "./...",
  [int]$Count = 1,
  [string]$Run = "",
  [string]$Image = "golang:1.27"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot          # repository root

# The values end up inside an 'sh -c' command: only characters with no meaning to the shell are accepted.
if ($Package -notmatch '^[A-Za-z0-9_./-]+$') { throw "-Package only accepts letters, digits and . / _ - (got: '$Package')" }
if ($Run -and $Run -notmatch '^[A-Za-z0-9_|^$()*+?.\[\]-]+$') { throw "-Run only accepts a simple regular expression, without quotes or spaces (got: '$Run')" }
if ($Image -notmatch '^[A-Za-z0-9_./:@-]+$') { throw "-Image is not a valid image name: '$Image'" }
if ($Count -lt 1) { throw "-Count must be at least 1" }

# In Windows PowerShell 5.1, with 'Stop', whatever a native program writes to stderr becomes a raw error (NativeCommandError)
# before its exit code can be checked. The docker commands use 'Continue' and the decision is made on the exit code.
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw "docker was not found." }
$prevEAP = $ErrorActionPreference
$ErrorActionPreference = "Continue"
try { & docker info *> $null; $dockerUp = ($LASTEXITCODE -eq 0) } finally { $ErrorActionPreference = $prevEAP }
if (-not $dockerUp) { throw "The Docker engine is not running." }

$testArgs = "go test -race -count=$Count"
if ($Run) { $testArgs += " -run '$Run'" }
$testArgs += " $Package"

Write-Host "== $testArgs  (image $Image, read-only code)"
$ErrorActionPreference = "Continue"
try {
  $out = & docker run --rm -v "${root}:/src:ro" -w /src -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local $Image sh -c "$testArgs 2>&1" 2>&1
  $dockerExit = $LASTEXITCODE
} finally { $ErrorActionPreference = $prevEAP }
$out | ForEach-Object { $_ }

# 125-127: docker could not create or start the container (missing image, no permissions...); it is not a test result
if ($dockerExit -in 125, 126, 127) { throw "Docker could not run the container (code $dockerExit)." }

$text = ($out | Out-String)
$races = ([regex]::Matches($text, "DATA RACE")).Count
if ($races -gt 0) { throw "The detector found $races data race(s)." }
if ($text -match '\[build failed\]|\[setup failed\]|no Go files|no packages to test|build constraints exclude') {
  throw "What was to be tested could not be built or set up: there is no race result."
}
# "ok ... [no tests to run]" is what go prints when -Run matches nothing: it does not count as a test that ran
$ok = ([regex]::Matches($text, '(?m)^ok\s(?!.*\[no tests to run\])')).Count
if ($ok -eq 0) { throw "No test ran successfully (check -Run and -Package): there is no race result." }

$failed = ([regex]::Matches($text, '(?m)^FAIL\s+\S+')).Count   # one "FAIL <package> ..." line per package (the bare final "FAIL" does not count)
if ($failed -gt 0) {
  Write-Host "No data races in $ok package(s). $failed package(s) fail for other reasons."
  exit 1
} else {
  Write-Host "No data races in $ok package(s)."
}
