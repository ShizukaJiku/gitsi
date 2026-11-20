package dockercmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// hubTr is a fake hub: it answers the hash script with what is in its folder and "downloads" by copying.
type hubTr struct{ home string }

func (h hubTr) Run(_ context.Context, cmd string) (transport.Result, error) {
	var b strings.Builder
	for _, rel := range []string{"docker.exe", "cli-plugins/docker-compose.exe", "cli-plugins/docker-buildx.exe"} {
		p := filepath.Join(h.home, "docker-cli", filepath.FromSlash(rel))
		if sum, err := install.FileSHA256(p); err == nil {
			st, _ := os.Stat(p)
			fmt.Fprintf(&b, "%s|%s|%d\r\n", rel, strings.ToUpper(sum), st.Size())
		} else {
			fmt.Fprintf(&b, "%s|MISSING\r\n", rel)
		}
	}
	return transport.Result{Stdout: b.String()}, nil
}
func (hubTr) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (hubTr) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}
func (hubTr) Fetch(_ context.Context, remote, local string) error {
	b, err := os.ReadFile(filepath.FromSlash(remote))
	if err != nil {
		return err
	}
	return os.WriteFile(local, b, 0o755)
}

func TestDockerCommandEndToEndWithFakes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows registry")
	}
	user := t.TempDir()
	t.Setenv("USERPROFILE", user)
	t.Setenv("HOME", user)
	t.Setenv("LOCALAPPDATA", filepath.Join(user, "AppData", "Local"))
	hubHome := t.TempDir()
	os.MkdirAll(filepath.Join(hubHome, "docker-cli", "cli-plugins"), 0o755)
	os.WriteFile(filepath.Join(hubHome, "docker-cli", "docker.exe"), []byte("client"), 0o755)
	os.WriteFile(filepath.Join(hubHome, "docker-cli", "cli-plugins", "docker-compose.exe"), []byte("compose"), 0o755)

	key := fmt.Sprintf(`HKCU:\Software\gitsi-test-dockercmd-%d`, os.Getpid())
	t.Cleanup(func() {
		winps.Command(context.Background(), "-NoProfile", "-Command", "Remove-Item -LiteralPath '"+key+"' -Recurse -Force -ErrorAction SilentlyContinue").Run()
	})
	var calls []string
	m := mod{pathKey: key, run: func(_ context.Context, docker string, args ...string) (string, error) {
		calls = append(calls, filepath.Base(docker)+" "+strings.Join(args, " "))
		if args[0] == "--context" {
			return "29.9.3 (linux)", nil
		}
		return "", nil
	}}
	cfg := config.Default()
	cfg.Hub = config.Hub{Host: "192.0.2.10", User: "user", Alias: "gitsi-hub"}
	cfg.Policy.AllowExec = true
	var out bytes.Buffer
	env := &module.Env{Stdout: &out, Stderr: &out, Config: cfg, Transport: hubTr{home: hubHome},
		LayoutFn: func(context.Context) (layout.Layout, error) {
			return layout.Layout{Home: filepath.ToSlash(hubHome)}, nil
		}}

	if err := m.cmd(context.Background(), env, nil); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	clientDir := filepath.Join(user, "AppData", "Local", "gitsi", "docker", "docker-cli")
	if b, _ := os.ReadFile(filepath.Join(clientDir, "docker.exe")); string(b) != "client" {
		t.Fatalf("docker.exe must end up in %s", clientDir)
	}
	if b, _ := os.ReadFile(filepath.Join(user, ".docker", "cli-plugins", "docker-compose.exe")); string(b) != "compose" {
		t.Fatal("compose must end up in ~/.docker/cli-plugins")
	}
	s := out.String()
	for _, want := range []string{"Account PATH: added", "Context: gitsi-hub -> ssh://gitsi-hub (active). Hub engine: 29.9.3 (linux)", "open a new one"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(strings.Join(calls, "\n"), "docker.exe context use gitsi-hub") {
		t.Fatalf("%v", calls)
	}

	// second time: everything up to date and the PATH was already there
	out.Reset()
	if err := m.cmd(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "docker.exe: up to date") || !strings.Contains(out.String(), "Account PATH: already included") {
		t.Fatalf("%s", out.String())
	}
}

func TestDockerDeclaresExecPolicyAndEdgeRole(t *testing.T) {
	c := mod{}.Commands()[0]
	if c.Feature != policy.Exec || c.Roles != module.Edge || !c.NeedsHub {
		t.Fatalf("%+v", c)
	}
}
