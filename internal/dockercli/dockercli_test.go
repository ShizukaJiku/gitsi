package dockercli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

func needWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell and registry")
	}
}

func ps(t *testing.T, script string) string {
	t.Helper()
	out, err := winps.Command(context.Background(), "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// The account's PATH grows without turning entries with %VARIABLES% into fixed text and without duplicates. On a test key, never
// on the real PATH.
func TestUserPathKeepsVariablesAndDoesNotDuplicate(t *testing.T) {
	needWindows(t)
	key := fmt.Sprintf(`HKCU:\Software\gitsi-test-path-%d`, os.Getpid())
	t.Cleanup(func() {
		winps.Command(context.Background(), "-NoProfile", "-Command", "Remove-Item -LiteralPath '"+key+"' -Recurse -Force -ErrorAction SilentlyContinue").Run()
	})
	ps(t, "New-Item -Path '"+key+"' -Force | Out-Null; Set-ItemProperty -LiteralPath '"+key+"' -Name Path -Type ExpandString -Value '%USERPROFILE%\\bin;C:\\other'")

	dir := `C:\Users\someone\AppData\Local\gitsi\docker\docker-cli`
	added, err := AddToUserPath(context.Background(), key, dir)
	if err != nil || !added {
		t.Fatalf("it must add it: %v %v", added, err)
	}
	raw := ps(t, "(Get-Item -LiteralPath '"+key+"').GetValue('Path', '', 'DoNotExpandEnvironmentNames')")
	kind := ps(t, "(Get-Item -LiteralPath '"+key+"').GetValueKind('Path')")
	if raw != `%USERPROFILE%\bin;C:\other;`+dir || kind != "ExpandString" {
		t.Fatalf("variables must stay unexpanded and the type ExpandString: %q (%s)", raw, kind)
	}
	// again (and spelled differently: upper case and trailing backslash): already there
	if added, err := AddToUserPath(context.Background(), key, strings.ToUpper(dir)+`\`); err != nil || added {
		t.Fatalf("it must not duplicate it: %v %v", added, err)
	}
	// key without a Path value: it creates it
	ps(t, "Remove-ItemProperty -LiteralPath '"+key+"' -Name Path")
	if added, err := AddToUserPath(context.Background(), key, dir); err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	if got := ps(t, "(Get-Item -LiteralPath '"+key+"').GetValue('Path', '', 'DoNotExpandEnvironmentNames')"); got != dir {
		t.Fatalf("%q", got)
	}
}

// fakeHub is the hub's home with the client published.
func fakeHub(t *testing.T, files map[string]string) string {
	home := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(home, Dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o755)
	}
	return home
}

func TestHashScriptAndParseAgainstRealPowerShell(t *testing.T) {
	needWindows(t)
	home := fakeHub(t, map[string]string{"docker.exe": "cliente", "cli-plugins/docker-compose.exe": "compose"})
	got := ParseHashes(ps(t, HashScript(filepath.ToSlash(home))))
	want, _ := install.FileSHA256(filepath.Join(home, Dir, "docker.exe"))
	if r := got["docker.exe"]; !r.Found || r.SHA256 != want || r.Size != 7 {
		t.Fatalf("%+v (want %s)", r, want)
	}
	if !got["cli-plugins/docker-compose.exe"].Found || got["cli-plugins/docker-buildx.exe"].Found {
		t.Fatalf("buildx is not published: %+v", got)
	}
}

// copyFetch plays scp: it copies from the fake hub's home; corrupt alters what is downloaded.
func copyFetch(corrupt bool) Fetch {
	return func(_ context.Context, remote, local string) error {
		b, err := os.ReadFile(filepath.FromSlash(remote))
		if err != nil {
			return err
		}
		if corrupt {
			b = append(b, '!')
		}
		return os.WriteFile(local, b, 0o755)
	}
}

func remoteOf(t *testing.T, home string) map[string]Remote {
	m := map[string]Remote{}
	for _, f := range Files {
		p := filepath.Join(home, Dir, filepath.FromSlash(f.Rel))
		if sum, err := install.FileSHA256(p); err == nil {
			st, _ := os.Stat(p)
			m[f.Rel] = Remote{SHA256: sum, Size: st.Size(), Found: true}
		} else {
			m[f.Rel] = Remote{}
		}
	}
	return m
}

func TestSyncInstallsVerifiesAndSkipsWhatIsUpToDate(t *testing.T) {
	home := fakeHub(t, map[string]string{"docker.exe": "client v1", "cli-plugins/docker-compose.exe": "compose v1"})
	edge := t.TempDir()
	clientDir, dockerHome := filepath.Join(edge, "docker-cli"), filepath.Join(edge, ".docker")
	ctx := context.Background()

	lines, err := Sync(ctx, filepath.ToSlash(home), clientDir, dockerHome, remoteOf(t, home), copyFetch(false))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(clientDir, "docker.exe")); string(b) != "client v1" {
		t.Fatal("docker.exe goes to the client folder")
	}
	if b, _ := os.ReadFile(filepath.Join(dockerHome, "cli-plugins", "docker-compose.exe")); string(b) != "compose v1" {
		t.Fatal("the plugins go to ~/.docker/cli-plugins")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "docker-buildx.exe: not published by the hub") {
		t.Fatalf("a missing plugin is skipped: %v", lines)
	}

	// again: up to date, nothing downloaded
	lines, err = Sync(ctx, filepath.ToSlash(home), clientDir, dockerHome, remoteOf(t, home),
		func(context.Context, string, string) error { return errors.New("it should download nothing") })
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "docker.exe: up to date") {
		t.Fatalf("%v %v", lines, err)
	}

	// a new version on the hub, but it arrives altered: nothing is put in place
	os.WriteFile(filepath.Join(home, Dir, "docker.exe"), []byte("client v2"), 0o755)
	_, err = Sync(ctx, filepath.ToSlash(home), clientDir, dockerHome, remoteOf(t, home), copyFetch(true))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(clientDir, "docker.exe")); string(b) != "client v1" {
		t.Fatal("with a wrong hash, the previous version stays intact")
	}
	if _, err := os.Stat(filepath.Join(clientDir, "docker.exe.new")); err == nil {
		t.Fatal("the temporary file must not remain")
	}
	// and correct: it is updated
	if _, err := Sync(ctx, filepath.ToSlash(home), clientDir, dockerHome, remoteOf(t, home), copyFetch(false)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(clientDir, "docker.exe")); string(b) != "client v2" {
		t.Fatal("it must be updated")
	}
}

func TestSyncNeedsThePublishedClient(t *testing.T) {
	_, err := Sync(context.Background(), "C:/h", t.TempDir(), t.TempDir(), map[string]Remote{}, copyFetch(false))
	if err == nil || !strings.Contains(err.Error(), "gitsi update -publish") {
		t.Fatalf("%v", err)
	}
}

func TestPublishCopiesClientAndPlugins(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "docker.exe"), []byte("client"), 0o755)
	plug := filepath.Join(src, "plugins")
	os.MkdirAll(plug, 0o755)
	os.WriteFile(filepath.Join(plug, "docker-compose.exe"), []byte("compose"), 0o755)
	oldLook, oldDirs := LookDocker, PluginDirs
	defer func() { LookDocker, PluginDirs = oldLook, oldDirs }()
	LookDocker = func() (string, error) { return filepath.Join(src, "docker.exe"), nil }
	PluginDirs = func() []string { return []string{filepath.Join(src, "missing"), plug} }

	home := t.TempDir()
	var out bytes.Buffer
	n, err := Publish(filepath.ToSlash(home), &out)
	if err != nil || n != 2 {
		t.Fatalf("%d %v %s", n, err, out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(home, Dir, "cli-plugins", "docker-compose.exe")); string(b) != "compose" {
		t.Fatal("the plugin is published in cli-plugins")
	}
	// without docker: it does not fail, it says so
	LookDocker = func() (string, error) { return "", errors.New("no") }
	out.Reset()
	if n, err := Publish(filepath.ToSlash(home), &out); err != nil || n != 0 || !strings.Contains(out.String(), "not installed") {
		t.Fatalf("%d %v %s", n, err, out.String())
	}
}

// SetupContext creates the context the first time, updates it afterwards, makes it active and asks the engine; when the engine
// does not answer, it points at Docker Desktop.
func TestSetupContextCreatesOrUpdatesAndChecksTheEngine(t *testing.T) {
	var calls []string
	exists, engineUp := false, true
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case args[0] == "context" && args[1] == "inspect":
			if exists {
				return "[]", nil
			}
			return "", errors.New("does not exist")
		case args[0] == "context" && args[1] == "create":
			exists = true
		case args[0] == "--context" && !engineUp:
			return "Cannot connect", errors.New("exit 1")
		case args[0] == "--context":
			return "29.9.3 (linux)\n", nil
		}
		return "", nil
	}
	srv, err := SetupContext(context.Background(), run, "docker.exe", "gitsi-hub")
	if err != nil || srv != "29.9.3 (linux)" {
		t.Fatalf("%q %v", srv, err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "context create gitsi-hub --docker host=ssh://gitsi-hub") ||
		!strings.Contains(strings.Join(calls, "\n"), "context use gitsi-hub") {
		t.Fatalf("%v", calls)
	}
	calls = nil
	if _, err := SetupContext(context.Background(), run, "docker.exe", "other-alias"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "context update gitsi-hub --docker host=ssh://other-alias") {
		t.Fatalf("the second time it is updated: %v", calls)
	}
	engineUp = false
	if _, err := SetupContext(context.Background(), run, "docker.exe", "gitsi-hub"); err == nil || !strings.Contains(err.Error(), "Docker Desktop") {
		t.Fatalf("%v", err)
	}
	_ = io.Discard
}
