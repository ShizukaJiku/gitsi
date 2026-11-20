package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

func cfgEnv(t *testing.T, role config.Role) (*module.Env, *bytes.Buffer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	c := config.Default()
	c.Role = role
	c.Hub = config.Hub{Host: "192.0.2.3", User: "user", KeyFile: "C:/k/key"}
	c.ReposDir = "C:/Users/user/repos"
	var out bytes.Buffer
	return &module.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Config: c}, &out, home
}

func TestConfigShowGetPath(t *testing.T) {
	env, out, home := cfgEnv(t, config.RoleEdge)
	if err := configCmd(context.Background(), env, nil); err != nil || !strings.Contains(out.String(), `"host": "192.0.2.3"`) || !strings.Contains(out.String(), `"version": 1`) {
		t.Fatalf("%v\n%s", err, out)
	}
	out.Reset()
	if err := configCmd(context.Background(), env, []string{"get", "hub.user"}); err != nil || strings.TrimSpace(out.String()) != "user" {
		t.Fatalf("%v %q", err, out)
	}
	out.Reset()
	configCmd(context.Background(), env, []string{"path"})
	if !strings.Contains(out.String(), filepath.Join(home, ".gitsi", "config.json")) {
		t.Fatalf("%q", out)
	}
	if err := configCmd(context.Background(), env, []string{"get", "nothing"}); err == nil || !strings.Contains(err.Error(), "hub.host") {
		t.Fatalf("an unknown key must list the valid ones: %v", err)
	}
}

func TestConfigSetValidatesAndPersists(t *testing.T) {
	env, _, _ := cfgEnv(t, config.RoleEdge)
	ctx := context.Background()
	for _, bad := range [][]string{
		{"hub.host", "a b"}, {"hub.host", "x;y"}, {"hub.user", "a;b"}, {"hub.port", "99999"}, {"hub.port", "abc"},
		{"hub.key_file", "C:/x'; calc; '"}, {"repos_dir", "C:/a/../b"}, {"remote_name", "a b"}, {"hub.alias", "a/b"},
	} {
		if err := configCmd(ctx, env, append([]string{"set"}, bad...)); err == nil {
			t.Errorf("%v should be rejected", bad)
		}
	}
	if env.Config.Hub.Host != "192.0.2.3" || env.Config.Hub.User != "user" {
		t.Fatal("an invalid value must change nothing")
	}
	if err := configCmd(ctx, env, []string{"set", "remote_name", "bridge"}); err != nil {
		t.Fatal(err)
	}
	got, _ := config.Load()
	if got.RemoteName != "bridge" {
		t.Fatalf("it was not persisted: %+v", got)
	}
}

func TestConfigSetRefusesRole(t *testing.T) {
	env, _, _ := cfgEnv(t, config.RoleEdge)
	err := configCmd(context.Background(), env, []string{"set", "role", "hub"})
	if err == nil || !strings.Contains(err.Error(), "gitsi setup") {
		t.Fatalf("%v", err)
	}
}

// Changing the host also updates the ~/.ssh/config block: git uses that alias, not gitsi's config.
func TestConfigSetKeepsSSHConfigInSync(t *testing.T) {
	env, out, home := cfgEnv(t, config.RoleEdge)
	ssh := filepath.Join(home, ".ssh", "config")
	os.MkdirAll(filepath.Dir(ssh), 0o700)
	os.WriteFile(ssh, []byte("Host other\n    HostName 1.1.1.1\n"), 0o600)

	if err := configCmd(context.Background(), env, []string{"set", "hub.host", "10.0.0.9"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ssh)
	s := string(b)
	if !strings.Contains(s, "HostName 10.0.0.9") || !strings.Contains(s, "Host other") || strings.Contains(s, "192.0.2.3") {
		t.Fatalf("the hub block must be updated without touching the rest:\n%s", s)
	}
	if !strings.Contains(out.String(), "~/.ssh/config") {
		t.Errorf("it must say it updated the ssh config: %s", out)
	}
	// idempotent: repeating does not duplicate the block
	configCmd(context.Background(), env, []string{"set", "hub.port", "2222"})
	b, _ = os.ReadFile(ssh)
	if strings.Count(string(b), "# >>> gitsi >>>") != 1 || !strings.Contains(string(b), "Port 2222") {
		t.Fatalf("%s", b)
	}
	// a value that does not affect the connection does not touch the ssh config
	before, _ := os.ReadFile(ssh)
	configCmd(context.Background(), env, []string{"set", "remote_name", "x"})
	after, _ := os.ReadFile(ssh)
	if !bytes.Equal(before, after) {
		t.Error("remote_name must not modify ~/.ssh/config")
	}
}

func TestConfigIsATopLevelCommandForBothRoles(t *testing.T) {
	for _, c := range (mod{}).Commands() {
		if c.Name == "config" {
			if c.Group != "" || c.Roles != module.Both || c.Hidden {
				t.Fatalf("%+v", c)
			}
			return
		}
	}
	t.Fatal("the config command is missing")
}

// watch's reactions are enabled with config set (all off by default).
func TestWatchSettings(t *testing.T) {
	env, _, home := cfgEnv(t, config.RoleEdge)
	dir := filepath.Join(home, "Downloads", "gitsi")
	for _, args := range [][]string{{"set", "watch.auto_update", "on"}, {"set", "watch.report", "on"}, {"set", "watch.auto_take", dir}} {
		if err := configCmd(context.Background(), env, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	w := env.Config.Watch
	if !w.AutoUpdate || !w.Report || w.AutoTake != dir {
		t.Fatalf("%+v", w)
	}
	if err := configCmd(context.Background(), env, []string{"set", "watch.auto_take", "relative"}); err == nil {
		t.Fatal("the folder must be a full path")
	}
	if err := configCmd(context.Background(), env, []string{"set", "watch.auto_update", "yes"}); err == nil {
		t.Fatal("only on or off")
	}
	configCmd(context.Background(), env, []string{"set", "watch.auto_take", "off"})
	if env.Config.Watch.AutoTake != "" {
		t.Fatal("off turns it off")
	}
}
