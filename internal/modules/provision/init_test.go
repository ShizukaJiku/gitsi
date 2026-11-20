package provision

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
)

func TestSetupEdgeValidatesBeforeTouchingAnything(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no host", []string{"-user", "u"}, "usage:"},
		{"no user", []string{"-host", "10.0.0.1"}, "usage:"},
		{"dangerous host", []string{"-host", "x';calc", "-user", "u"}, "host"},
		{"dangerous user", []string{"-host", "10.0.0.1", "-user", "u;rm"}, "user"},
		{"dangerous alias", []string{"-host", "10.0.0.1", "-user", "u", "-alias", "a b"}, "alias"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, _, home := cfgEnv(t, config.RoleEdge)
			err := initCmd(context.Background(), env, c.args)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected an error with %q: %v", c.want, err)
			}
			for _, p := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, ".gitsi", "config.json")} {
				if _, err := os.Stat(p); err == nil {
					t.Errorf("invalid input must not create %s", p)
				}
			}
		})
	}
	env, _, _ := cfgEnv(t, config.RoleEdge)
	if err := initCmd(context.Background(), env, []string{"-missing"}); err == nil {
		t.Fatal("an unknown flag must fail")
	}
}

func TestConfigSetEverySettingValidatesItsValue(t *testing.T) {
	cases := []struct {
		role  config.Role
		key   string
		good  string
		bad   []string
		check func(*config.Config) string
	}{
		{config.RoleEdge, "hub.host", "10.0.0.5", []string{"x';calc", "", "a b"}, func(c *config.Config) string { return c.Hub.Host }},
		{config.RoleEdge, "hub.user", "user1", []string{"u;rm", "a b"}, func(c *config.Config) string { return c.Hub.User }},
		{config.RoleEdge, "hub.port", "2222", []string{"abc", "-1", "70000", ""}, func(c *config.Config) string { return strconv.Itoa(c.Hub.Port) }},
		{config.RoleEdge, "hub.key_file", "C:/k/other", []string{"", "a;b", "a'b", "a\nb"}, func(c *config.Config) string { return c.Hub.KeyFile }},
		{config.RoleEdge, "hub.alias", "my-hub", []string{"a b", "x;y"}, func(c *config.Config) string { return c.Hub.Alias }},
		{config.RoleEdge, "remote_name", "bridge", []string{"a b", "x/y;"}, func(c *config.Config) string { return c.RemoteName }},
		{config.RoleHub, "home", "D:/gitsi-hub", []string{"x';calc", "a$b"}, func(c *config.Config) string { return c.Home }},
		{config.RoleHub, "repos_dir", "D:/gitsi-hub/repos", []string{"x';calc", "a$b"}, func(c *config.Config) string { return c.ReposDir }},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			env, _, _ := cfgEnv(t, c.role)
			for _, bad := range c.bad {
				before := c.check(env.Config)
				if err := setSetting(env, c.key, bad); err == nil {
					t.Errorf("%s=%q should be rejected", c.key, bad)
				}
				if c.check(env.Config) != before {
					t.Errorf("an invalid value must not change the configuration (%s=%q)", c.key, bad)
				}
			}
			if err := setSetting(env, c.key, c.good); err != nil {
				t.Fatalf("%s=%q: %v", c.key, c.good, err)
			}
			if got := c.check(env.Config); got != c.good {
				t.Fatalf("%s ended up as %q", c.key, got)
			}
		})
	}
}

func TestConfigSetHubOnlyKeysAreRefusedInAnEdgeAndExplainedInTheHub(t *testing.T) {
	env, _, _ := cfgEnv(t, config.RoleEdge)
	for _, k := range []string{"home", "repos_dir"} {
		if err := setSetting(env, k, "D:/x"); err == nil || !strings.Contains(err.Error(), "decided by the hub") {
			t.Errorf("%s on an edge must explain the hub decides it: %v", k, err)
		}
	}
	hub, out, _ := cfgEnv(t, config.RoleHub)
	hub.Config.ReposDir = "" // without an explicit repos_dir, the repos go under the new home (an explicit repos_dir wins)
	if err := setSetting(hub, "home", "D:/my-gitsi"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"This hub's new layout", "D:/my-gitsi/repos", "update -publish", "10 min"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestConfigSetWarnsAboutAMissingKeyFile(t *testing.T) {
	env, out, home := cfgEnv(t, config.RoleEdge)
	missing := filepath.ToSlash(filepath.Join(home, "missing"))
	if err := setSetting(env, "hub.key_file", missing); err != nil || !strings.Contains(out.String(), "does not exist yet") {
		t.Fatalf("%v\n%s", err, out)
	}
	out.Reset()
	present := filepath.Join(home, "exists")
	os.WriteFile(present, []byte("k"), 0o600)
	if err := setSetting(env, "hub.key_file", filepath.ToSlash(present)); err != nil || strings.Contains(out.String(), "does not exist yet") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestConfigSetRejectsUnknownKeysAndTheRole(t *testing.T) {
	env, _, _ := cfgEnv(t, config.RoleEdge)
	if err := setSetting(env, "role", "hub"); err == nil || !strings.Contains(err.Error(), "setup") {
		t.Fatalf("the role is not changed with config set: %v", err)
	}
	if err := setSetting(env, "made-up", "x"); err == nil || !strings.Contains(err.Error(), "hub.host") {
		t.Fatalf("an unknown key must list the valid ones: %v", err)
	}
}
