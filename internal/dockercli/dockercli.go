// Package dockercli gathers what the edge needs to use the hub's Docker with the OFFICIAL, unmodified Docker client: the hub
// publishes docker.exe and its plugins next to gitsi; the edge downloads them verifying their SHA256, adds the client folder to its
// account's PATH and creates the context host=ssh://<alias>, which uses the same alias and key as gitsi. Docker talks over SSH on
// its own (docker system dial-stdio): gitsi neither touches that traffic nor opens ports.
package dockercli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// Dir is the client folder inside the hub's home (and its name on the edge).
const Dir = "docker-cli"

// File is a piece of the client: its relative path on the hub and whether it is essential.
type File struct {
	Rel      string // with '/', relative to <home>/docker-cli
	Required bool
}

// Files are the client's pieces: docker.exe and the plugins used by 'docker compose' and 'docker build'.
var Files = []File{
	{Rel: "docker.exe", Required: true},
	{Rel: "cli-plugins/docker-compose.exe"},
	{Rel: "cli-plugins/docker-buildx.exe"},
}

// ContextName is the docker context 'gitsi docker' creates.
const ContextName = "gitsi-hub"

// ---------------- hub: publish

// PluginDirs are the folders where Docker Desktop leaves the plugins on the hub.
var PluginDirs = func() []string {
	home, _ := os.UserHomeDir()
	return []string{`C:\Program Files\Docker\cli-plugins`, filepath.Join(home, ".docker", "cli-plugins")}
}

// LookDocker finds the hub's docker.exe (a variable for tests).
var LookDocker = func() (string, error) { return exec.LookPath("docker") }

// Publish copies the hub's docker client to <home>/docker-cli for the edges to download. When Docker is not installed it does
// nothing (and says so). It returns how many files it published.
func Publish(home string, out io.Writer) (int, error) {
	docker, err := LookDocker()
	if err != nil {
		fmt.Fprintln(out, "Docker is not installed on this hub: 'gitsi docker' will not be available to the edges.")
		return 0, nil
	}
	dst := filepath.Join(filepath.FromSlash(home), Dir)
	n := 0
	for _, f := range Files {
		src := ""
		if f.Rel == "docker.exe" {
			src = docker
		} else {
			name := path.Base(f.Rel)
			for _, d := range PluginDirs() {
				if _, err := os.Stat(filepath.Join(d, name)); err == nil {
					src = filepath.Join(d, name)
					break
				}
			}
		}
		if src == "" {
			continue // a plugin this hub lacks: the edge works without it (no compose or no buildx)
		}
		if _, err := install.PublishSelf(src, filepath.Join(dst, filepath.FromSlash(f.Rel))); err != nil {
			return n, fmt.Errorf("publishing %s: %w", f.Rel, err)
		}
		n++
	}
	fmt.Fprintf(out, "Published the docker client (%d file(s)) in %s\n", n, dst)
	return n, nil
}

// ---------------- edge: download and verify

// Remote is what the hub reports about a published piece.
type Remote struct {
	SHA256 string
	Size   int64
	Found  bool
}

// HashScript asks the hub, in a single call, for the SHA256 and size of each piece: "rel|HASH|BYTES" or "rel|MISSING".
func HashScript(home string) string {
	var b strings.Builder
	base := strings.TrimRight(home, "/") + "/" + Dir + "/"
	for _, f := range Files {
		fmt.Fprintf(&b, "$p = '%s%s'; if (Test-Path -LiteralPath $p) { $i = Get-Item -LiteralPath $p; '%s|' + (Get-FileHash -Algorithm SHA256 -LiteralPath $i.FullName).Hash + '|' + $i.Length } else { '%s|MISSING' }; ",
			base, f.Rel, f.Rel, f.Rel)
	}
	return b.String()
}

// ParseHashes interprets HashScript's output.
func ParseHashes(out string) map[string]Remote {
	m := map[string]Remote{}
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(l), "|")
		if len(p) == 2 && p[1] == "MISSING" {
			m[p[0]] = Remote{}
			continue
		}
		if len(p) != 3 || len(p[1]) != 64 {
			continue
		}
		n, _ := strconv.ParseInt(p[2], 10, 64)
		m[p[0]] = Remote{SHA256: strings.ToLower(p[1]), Size: n, Found: true}
	}
	return m
}

// LocalPath is where each piece lives on the edge: docker.exe in the client folder (which goes on the PATH) and the plugins in
// ~/.docker/cli-plugins, where the official client looks for them.
func LocalPath(clientDir, dockerHome string, f File) string {
	if strings.HasPrefix(f.Rel, "cli-plugins/") {
		return filepath.Join(dockerHome, "cli-plugins", path.Base(f.Rel))
	}
	return filepath.Join(clientDir, filepath.FromSlash(f.Rel))
}

// Fetch downloads a file from the hub (scp).
type Fetch func(ctx context.Context, remote, local string) error

// Sync brings each piece up to date on the edge: when the local hash is already the published one it downloads nothing; otherwise
// it downloads to a temporary file, verifies its SHA256 and puts it in place (moving the previous one aside if it is in use). It
// returns one line per piece to show.
func Sync(ctx context.Context, home, clientDir, dockerHome string, remote map[string]Remote, fetch Fetch) ([]string, error) {
	var lines []string
	for _, f := range Files {
		r := remote[f.Rel]
		if !r.Found {
			if f.Required {
				return lines, errors.New("the hub does not publish the docker client: on the hub, with Docker Desktop installed, run 'gitsi update -publish'")
			}
			lines = append(lines, path.Base(f.Rel)+": not published by the hub (skipped)")
			continue
		}
		local := LocalPath(clientDir, dockerHome, f)
		if cur, err := install.FileSHA256(local); err == nil && cur == r.SHA256 {
			lines = append(lines, path.Base(f.Rel)+": up to date")
			continue
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return lines, err
		}
		tmp := local + ".new"
		_ = os.Remove(tmp)
		src := strings.TrimRight(home, "/") + "/" + Dir + "/" + f.Rel
		if err := fetch(ctx, src, tmp); err != nil {
			_ = os.Remove(tmp)
			return lines, fmt.Errorf("downloading %s: %w", f.Rel, err)
		}
		got, err := install.FileSHA256(tmp)
		if err != nil || got != r.SHA256 {
			_ = os.Remove(tmp)
			if err != nil {
				return lines, fmt.Errorf("%s: the download could not be read (removed by an antivirus?): %w", f.Rel, err)
			}
			return lines, fmt.Errorf("%s: the downloaded hash does not match (expected %s, got %s)", f.Rel, r.SHA256[:12], got[:12])
		}
		if _, err := install.PublishSelf(tmp, local); err != nil {
			_ = os.Remove(tmp)
			return lines, err
		}
		_ = os.Remove(tmp)
		lines = append(lines, fmt.Sprintf("%s: installed (%.1f MB, SHA256 %s)", path.Base(f.Rel), float64(r.Size)/(1<<20), r.SHA256[:12]))
	}
	return lines, nil
}

// ---------------- edge: account PATH

// UserPathScript adds dir to the account's PATH (HKCU\Environment) when missing. It reads and writes the value WITHOUT expanding it
// and as ExpandString, so entries with %VARIABLES% are not turned into fixed text (which [Environment]::SetEnvironmentVariable
// would do). At the end it notifies the system of the change (terminals opened afterwards see it). key is the registry key (a
// variable in tests). It prints ADDED or PRESENT.
func UserPathScript(key, dir string) string {
	q := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$key = '%s'; $dir = '%s'
if (-not (Test-Path -LiteralPath $key)) { New-Item -Path $key -Force | Out-Null }
$k = Get-Item -LiteralPath $key
$raw = [string]$k.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
$parts = @($raw -split ';' | Where-Object { $_ -ne '' })
if (@($parts | Where-Object { $_.TrimEnd('\') -ieq $dir.TrimEnd('\') }).Count -gt 0) { 'PRESENT'; exit 0 }
$new = (@($parts) + $dir) -join ';'
Set-ItemProperty -LiteralPath $key -Name Path -Value $new -Type ExpandString
[Environment]::SetEnvironmentVariable('GITSI_PATH_REFRESH', '1', 'User'); [Environment]::SetEnvironmentVariable('GITSI_PATH_REFRESH', $null, 'User')
'ADDED'`, q(key), q(dir))
}

// UserPathKey is the real key of the account's PATH.
const UserPathKey = `HKCU:\Environment`

// AddToUserPath runs UserPathScript with the system's powershell. It returns true when it added the folder (false when it was
// already there).
func AddToUserPath(ctx context.Context, key, dir string) (bool, error) {
	out, err := procx.Background(winps.Command(ctx, "-NoProfile", "-NonInteractive", "-Command", UserPathScript(key, dir))).CombinedOutput()
	s := strings.TrimSpace(string(out))
	switch {
	case err != nil:
		return false, fmt.Errorf("could not update the account's PATH: %v %s", err, s)
	case strings.HasSuffix(s, "ADDED"):
		return true, nil
	case strings.HasSuffix(s, "PRESENT"):
		return false, nil
	}
	return false, fmt.Errorf("unexpected answer while updating the PATH: %s", s)
}

// ---------------- edge: context

// Runner runs the docker client and returns its combined output (a variable in tests).
type Runner func(ctx context.Context, docker string, args ...string) (string, error)

// ExecRunner is the real Runner.
func ExecRunner(ctx context.Context, docker string, args ...string) (string, error) {
	out, err := procx.Background(exec.CommandContext(ctx, docker, args...)).CombinedOutput()
	return string(out), err
}

// SetupContext creates (or updates) the gitsi-hub context with host=ssh://<alias>, makes it active and asks the hub's engine for
// its version.
func SetupContext(ctx context.Context, run Runner, docker, alias string) (server string, err error) {
	host := "host=ssh://" + alias
	if _, err := run(ctx, docker, "context", "inspect", ContextName); err == nil {
		if out, err := run(ctx, docker, "context", "update", ContextName, "--docker", host); err != nil {
			return "", fmt.Errorf("docker context update: %v %s", err, strings.TrimSpace(out))
		}
	} else if out, err := run(ctx, docker, "context", "create", ContextName, "--docker", host); err != nil {
		return "", fmt.Errorf("docker context create: %v %s", err, strings.TrimSpace(out))
	}
	if out, err := run(ctx, docker, "context", "use", ContextName); err != nil {
		return "", fmt.Errorf("docker context use: %v %s", err, strings.TrimSpace(out))
	}
	vctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := run(vctx, docker, "--context", ContextName, "version", "--format", "{{.Server.Version}} ({{.Server.Os}})")
	if err != nil {
		return "", fmt.Errorf("the hub's Docker engine does not answer (is Docker Desktop running on the hub?): %s", strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}
