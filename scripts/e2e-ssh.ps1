<#
.SYNOPSIS
  Runs gitsi's end-to-end tests against a REAL Windows sshd, started separately and thrown away afterwards.

.DESCRIPTION
  It does not touch the system's sshd service, the firewall or authorized_keys:
    - creates a temporary folder with keys, an sshd_config and an isolated hub profile (SetEnv USERPROFILE)
    - starts 'sshd.exe -D' on 127.0.0.1 on a free port
    - runs  go test ./internal/e2e -run OpenSSH  with the GITSI_E2E_SSH_* variables
    - at the end (or on failure) stops that sshd and deletes the temporary folder

  With -RealClipboard it also tests 'send -clip'. NOTE: that OVERWRITES your session's clipboard.

.EXAMPLE
  .\scripts\e2e-ssh.ps1
  .\scripts\e2e-ssh.ps1 -RealClipboard
#>
[CmdletBinding()]
param(
  [switch]$RealClipboard,
  [string]$Go = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot          # repository root
Set-Location $root

$sshDir = Join-Path $env:SystemRoot "System32\OpenSSH"
$sshd = Join-Path $sshDir "sshd.exe"
$keygen = Join-Path $sshDir "ssh-keygen.exe"
if (-not (Test-Path $sshd)) { throw "$sshd was not found (install the Windows OpenSSH server)." }
if (-not (Test-Path $keygen)) { throw "$keygen was not found." }

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

# A free port on loopback
$probe = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 0)
$probe.Start(); $port = $probe.LocalEndpoint.Port; $probe.Stop()

$work = Join-Path $env:TEMP ("gitsi-e2e-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
$hubhome = Join-Path $work "hubhome"
New-Item -ItemType Directory -Force $hubhome | Out-Null
$proc = $null
$exit = 1

try {
  # Keys without a passphrase. 'cmd /c' passes the empty argument correctly in both PowerShell 5.1 and 7.
  foreach ($n in @("hostkey", "client")) {
    $f = Join-Path $work $n
    cmd.exe /c "`"$keygen`" -q -t ed25519 -N `"`" -f `"$f`""
    if ($LASTEXITCODE -ne 0) { throw "ssh-keygen failed for $n" }
  }
  Copy-Item (Join-Path $work "client.pub") (Join-Path $work "authorized_keys")

  $w = $work.Replace("\", "/")
  $cfg = @(
    "Port $port",
    "ListenAddress 127.0.0.1",
    "HostKey $w/hostkey",
    "PidFile none",
    "AuthorizedKeysFile $w/authorized_keys",
    "PasswordAuthentication no",
    "PubkeyAuthentication yes",
    "StrictModes no",
    "AllowTcpForwarding local",
    "SetEnv USERPROFILE=$hubhome",
    "LogLevel VERBOSE"
  )
  Set-Content -Path (Join-Path $work "sshd_config") -Value $cfg -Encoding Ascii

  $proc = Start-Process -FilePath $sshd -ArgumentList @("-D", "-e", "-f", (Join-Path $work "sshd_config")) `
    -RedirectStandardError (Join-Path $work "sshd.log") -PassThru -WindowStyle Hidden

  # Wait until it listens
  $ok = $false
  for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Milliseconds 250
    if ($proc.HasExited) { break }
    try { $c = New-Object System.Net.Sockets.TcpClient; $c.Connect("127.0.0.1", $port); $c.Close(); $ok = $true; break } catch { }
  }
  if (-not $ok) { throw "the temporary sshd did not start. Log: " + (Get-Content (Join-Path $work "sshd.log") -ErrorAction SilentlyContinue | Out-String) }
  Write-Host "Temporary sshd listening on 127.0.0.1:$port (pid $($proc.Id)); folder $work"

  $env:GITSI_E2E_SSH_PORT = "$port"
  $env:GITSI_E2E_SSH_KEY = (Join-Path $work "client")
  $env:GITSI_E2E_SSH_HUBHOME = $hubhome
  if ($RealClipboard) { $env:GITSI_REAL_CLIPBOARD = "1"; Write-Host "WARNING: -RealClipboard overwrites the clipboard." }

  & $go test ./internal/e2e -run OpenSSH -v -count=1
  $exit = $LASTEXITCODE
}
finally {
  if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
  Start-Sleep -Milliseconds 300
  Remove-Item Env:GITSI_E2E_SSH_PORT, Env:GITSI_E2E_SSH_KEY, Env:GITSI_E2E_SSH_HUBHOME, Env:GITSI_REAL_CLIPBOARD -ErrorAction SilentlyContinue
  # git stores its objects as read-only files and Directory.Delete cannot remove them: clear the attribute first
  try { Get-ChildItem -LiteralPath $work -Recurse -Force -File -ErrorAction SilentlyContinue | ForEach-Object { $_.IsReadOnly = $false } } catch { }
  try { [System.IO.Directory]::Delete($work, $true) } catch { Write-Warning "Could not delete $work : $($_.Exception.Message)" }
  Write-Host "Temporary sshd stopped and temporary folder deleted."
}
exit $exit
