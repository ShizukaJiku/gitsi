package remote

// Hub side of background jobs: the PowerShell scripts that run there and the parsing of their answers. The edge commands that use
// them are in jobs.go.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Background jobs. Everything runs through Transport.Run (PowerShell on the hub), with no new protocol.
//
// Each job lives in <jobs>/<id>/ with: cmd.enc (encoded command), runner.ps1, out.log (combined output), exit (exit code, only
// present once finished), pid and meta. The process is started through WMI (Win32_Process.Create), so it does NOT hang from the
// SSH session's process tree and survives the edge disconnecting.

const maxCommandLen = 6000 // the command travels inside ssh's command line

var jobIDRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

func newJobID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// encodePS encodes a script for `powershell -EncodedCommand` (UTF-16LE in base64).
func encodePS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// exeName takes the command's first token, reduced to harmless characters, to show it in `job ls`.
func exeName(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return "?"
	}
	var b strings.Builder
	for _, r := range f[0] {
		if r < 128 && (r == '.' || r == '_' || r == '-' || r == ':' || r == '/' || r == '\\' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// runnerScript is what the detached process runs: it runs the command in a child powershell, dumps the combined output to
// out.log and stores its exit code. d is the job's folder (with '/').
func runnerScript(d string) string {
	return `$d = '` + d + `'
$enc = (Get-Content -LiteralPath "$d/cmd.enc" -Raw).Trim()
$log = ("$d/out.log").Replace('/', '\')
$p = Start-Process -FilePath 'cmd.exe' -ArgumentList ('/c powershell.exe -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand ' + $enc + ' > "' + $log + '" 2>&1') -Wait -PassThru -WindowStyle Hidden
Set-Content -LiteralPath "$d/exit" -Value $p.ExitCode
`
}

// startScript creates the job's folder, writes its files and starts the runner outside the session's tree.
func startScript(jobsDir, id, cwd, command string) string {
	d := jobsDir + "/" + id
	// the child writes to a file: no progress bar (PowerShell serializes it as CLIXML when redirected) and in UTF-8
	inner := "$ProgressPreference = 'SilentlyContinue'; [Console]::OutputEncoding = [Text.Encoding]::UTF8; " + wrap(cwd, command)
	return strings.Join([]string{
		`$ErrorActionPreference = 'Stop'`, // any failure aborts: STARTED is never reported if something failed before
		`$d = '` + d + `'`,
		`[void][IO.Directory]::CreateDirectory($d)`, // New-Item does not accept -LiteralPath in PowerShell 5.1
		`Set-Content -LiteralPath "$d/cmd.enc" -Value '` + encodePS(inner) + `'`,
		`[IO.File]::WriteAllText("$d/runner.ps1", [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + b64(runnerScript(d)) + `')))`,
		`Set-Content -LiteralPath "$d/meta" -Value '` + time.Now().Format(time.RFC3339) + `|` + exeName(command) + `|` + cwd + `'`,
		`$cl = 'powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + ("$d/runner.ps1").Replace('/', '\') + '"'`,
		`$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $cl }`,
		`if ($r.ReturnValue -ne 0) { throw ("could not start the job (WMI " + $r.ReturnValue + ")") }`,
		`Set-Content -LiteralPath "$d/pid" -Value $r.ProcessId`,
		// The start time identifies the process even if its PID is reused (e.g. after restarting the hub): see stateExpr.
		`$p0 = Get-Process -Id $r.ProcessId -ErrorAction SilentlyContinue`,
		`if ($p0) { Set-Content -LiteralPath "$d/pidstart" -Value $p0.StartTime.ToUniversalTime().Ticks }`,
		`"STARTED|` + id + `|" + $r.ProcessId`,
	}, "; ")
}

// stateExpr computes a job's state; it expects $d to be defined. A live PID is not enough: Windows reuses PIDs (after restarting
// the hub, another process may have the job's), and `stop` would kill that unrelated process. So the process must also have the
// start time saved when it was started (pidstart); without it the job does not count as alive. Each statement goes on a single
// line: oneLine joins them with "; ".
const stateExpr = `$exit = ''; if (Test-Path -LiteralPath "$d/exit") { $exit = (Get-Content -LiteralPath "$d/exit" -Raw).Trim() }
$jpid = Get-Content -LiteralPath "$d/pid" -ErrorAction SilentlyContinue
$jstart = Get-Content -LiteralPath "$d/pidstart" -ErrorAction SilentlyContinue
$state = 'lost'
$pr = $null; if ($exit -eq '' -and $jpid) { $pr = Get-Process -Id ([int]$jpid) -ErrorAction SilentlyContinue }
$same = $false; if ($pr -and $jstart) { try { $same = ([string]$pr.StartTime.ToUniversalTime().Ticks) -eq ([string]$jstart).Trim() } catch { $same = $false } }
if ($exit -ne '') { $state = 'exited' } elseif ($same) { $state = 'running' }`

func listScript(jobsDir string) string {
	return `Get-ChildItem -LiteralPath '` + jobsDir + `' -Directory -ErrorAction SilentlyContinue | ForEach-Object { $d = $_.FullName.Replace('\', '/'); $id = $_.Name; ` +
		oneLine(stateExpr) + `; $meta = Get-Content -LiteralPath "$d/meta" -ErrorAction SilentlyContinue; "JOB|$id|$state|$exit|$meta" }`
}

func logsScript(jobsDir, id string, offset int64) string {
	return `$d = '` + jobsDir + `/` + id + `'; if (-not (Test-Path -LiteralPath $d)) { "NOJOB"; return }; ` + oneLine(stateExpr) + `; ` +
		`$off = ` + strconv.FormatInt(offset, 10) + `; $f = "$d/out.log"; $data = ''; $new = $off; ` +
		`if (Test-Path -LiteralPath $f) { $s = [IO.File]::Open($f, 'Open', 'Read', 'ReadWrite'); try { if ($off -lt 0) { $off = [Math]::Max(0, $s.Length + $off) }; if ($off -gt $s.Length) { $off = 0 }; ` +
		`[void]$s.Seek($off, 'Begin'); $n = [int][Math]::Min($s.Length - $off, 1048576); $buf = New-Object byte[] $n; $got = $s.Read($buf, 0, $n); ` +
		`$data = [Convert]::ToBase64String($buf, 0, $got); $new = $off + $got } finally { $s.Dispose() } }; ` +
		`"STATE|$state|$exit|$new"; "DATA|$data"`
}

func stopScript(jobsDir, id string) string {
	return `$d = '` + jobsDir + `/` + id + `'; if (-not (Test-Path -LiteralPath $d)) { "NOJOB"; return }; ` + oneLine(stateExpr) + `; ` +
		`if ($state -eq 'running') { & taskkill.exe /PID ([int]$jpid) /T /F | Out-Null; ` +
		`if (-not (Test-Path -LiteralPath "$d/exit")) { Set-Content -LiteralPath "$d/exit" -Value 137 }; "STOPPED" } else { "NOTRUNNING" }`
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", "; ")), " ")
}

// Job is one row of `job ls`.
type Job struct {
	ID      string `json:"id"`
	State   string `json:"state"` // running | exited | lost
	Exit    string `json:"exit"`
	Started string `json:"started"`
	Exe     string `json:"exe"`
	Cwd     string `json:"cwd,omitempty"` // hub folder where it runs (empty: the session's starting folder)
}

// parseJobs interprets listScript's output.
func parseJobs(out string) []Job {
	var jobs []Job
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(l), "|")
		if len(p) < 6 || p[0] != "JOB" {
			continue
		}
		j := Job{ID: p[1], State: p[2], Exit: p[3], Started: p[4], Exe: p[5]}
		if len(p) > 6 {
			j.Cwd = p[6]
		}
		jobs = append(jobs, j)
	}
	// By start time: the order `job ls` numbers and `job logs <n>` and `job stop <n>` use.
	sort.SliceStable(jobs, func(i, j int) bool { return startOf(jobs[i]).Before(startOf(jobs[j])) })
	return jobs
}

// startOf is a job's start time; one without readable meta goes first.
func startOf(j Job) time.Time {
	t, _ := time.Parse(time.RFC3339, j.Started)
	return t
}

// logChunk is the result of a logs query.
type logChunk struct {
	State, Exit string
	Offset      int64
	Data        []byte
	NoJob       bool
}

func parseLogs(out string) (logChunk, error) {
	var c logChunk
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "NOJOB":
			c.NoJob = true
			return c, nil
		case strings.HasPrefix(l, "STATE|"):
			p := strings.Split(l, "|")
			if len(p) < 4 {
				return c, errors.New("invalid logs answer")
			}
			c.State, c.Exit = p[1], p[2]
			c.Offset, _ = strconv.ParseInt(p[3], 10, 64)
		case strings.HasPrefix(l, "DATA|"):
			b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(l, "DATA|"))
			if err != nil {
				return c, fmt.Errorf("invalid logs data: %w", err)
			}
			c.Data = b
		}
	}
	if c.State == "" {
		return c, fmt.Errorf("unexpected answer from the hub: %.80q", out)
	}
	return c, nil
}
