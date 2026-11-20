package remote

// prepareScript runs on the hub: it makes the working clone <work> equal to the snapshot the edge pushed to the bridge <bridge>
// (runRef). What .gitignore ignores (node_modules, bin...) is kept to build fast; anything else not in the snapshot is removed.
// git runs with safe.directory='*' because folders created over SSH are owned by the Administrators group (dubious ownership).
// No $ErrorActionPreference = 'Stop': in PowerShell 5.1 git's error output (2>&1) would be an exception; each step checks
// $LASTEXITCODE. The paths arrive validated (safe.Path) and in single quotes.
func prepareScript(bridge, work string) string {
	return `$b = '` + bridge + `'; $w = '` + work + `'
function Step($what) { if ($LASTEXITCODE -ne 0) { "FAIL|$what|" + ($out | Out-String).Trim(); exit 1 } }
if (-not (Test-Path -LiteralPath (Join-Path $w '.git'))) {
  [void][IO.Directory]::CreateDirectory((Split-Path -Parent $w))
  $out = & git -c safe.directory='*' clone --quiet --no-checkout $b $w 2>&1; Step 'clone'
}
Set-Location -LiteralPath $w
$out = & git -c safe.directory='*' fetch --quiet --force $b '+` + runRef + `:` + runRef + `' 2>&1; Step 'fetch'
$out = & git -c safe.directory='*' checkout --quiet --force --detach '` + runRef + `' 2>&1; Step 'checkout'
$out = & git -c safe.directory='*' clean --quiet -fd 2>&1; Step 'clean'
"READY"`
}

// getScript runs on the hub: it leaves <target> (file or folder) in its outbox with its own gitsi, or says NOPATH when it does
// not exist. The paths arrive validated (safe.Path) and in single quotes.
func getScript(bin, target string) string {
	return `$p = '` + target + `'; if (-not (Test-Path -LiteralPath $p)) { 'NOPATH'; exit 0 }; & '` + bin + `' send $p 2>&1 | ForEach-Object { "$_" }`
}
