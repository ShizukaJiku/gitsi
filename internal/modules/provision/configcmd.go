package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// `gitsi config` shows and changes the configuration without editing the JSON by hand. Values are validated as during setup,
// and those that affect the connection (host, user, port, key, alias) also regenerate the ~/.ssh/config block git uses: that way
// there are no two sources of truth drifting apart.

var hostRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,253}$`)

type setting struct {
	name    string
	summary string
	get     func(*config.Config) string
	set     func(*config.Config, string) error
	ssh     bool // affects the ~/.ssh/config block
	// hubOnly: decided by the hub (its folder layout); on an edge it is not configured but discovered.
	hubOnly bool
}

func settings() []setting {
	return []setting{
		{"hub.host", "hub IP or name", func(c *config.Config) string { return c.Hub.Host },
			func(c *config.Config, v string) error {
				if !hostRe.MatchString(v) {
					return fmt.Errorf("invalid host: %q (letters, digits, dot, dash or colon)", v)
				}
				c.Hub.Host = v
				return nil
			}, true, false},
		{"hub.user", "user on the hub", func(c *config.Config) string { return c.Hub.User },
			func(c *config.Config, v string) error {
				if err := safe.Name("user", v); err != nil {
					return err
				}
				c.Hub.User = v
				return nil
			}, true, false},
		{"hub.port", "hub's SSH port (0 = 22)", func(c *config.Config) string { return strconv.Itoa(c.Hub.Port) },
			func(c *config.Config, v string) error {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n > 65535 {
					return fmt.Errorf("invalid port: %q (0-65535)", v)
				}
				c.Hub.Port = n
				return nil
			}, true, false},
		{"hub.key_file", "private key this machine uses", func(c *config.Config) string { return c.Hub.KeyFile },
			func(c *config.Config, v string) error {
				if v == "" || strings.ContainsAny(v, "'\"`;\r\n") {
					return fmt.Errorf("invalid key path: %q", v)
				}
				c.Hub.KeyFile = v
				return nil
			}, true, false},
		{"hub.alias", "SSH alias git uses", func(c *config.Config) string { return c.Hub.Alias },
			func(c *config.Config, v string) error {
				if err := safe.Name("alias", v); err != nil {
					return err
				}
				c.Hub.Alias = v
				return nil
			}, true, false},
		{name: "home", summary: "(hub) gitsi's root: binary, mailbox and jobs", get: func(c *config.Config) string { return c.Home },
			set: func(c *config.Config, v string) error {
				if err := safe.Path("home", v); err != nil {
					return err
				}
				c.Home = v
				return nil
			}, hubOnly: true},
		{name: "repos_dir", summary: "(hub) folder of the bridge repos (default <home>/repos)", get: func(c *config.Config) string { return c.ReposDir },
			set: func(c *config.Config, v string) error {
				if err := safe.Path("repos_dir", v); err != nil {
					return err
				}
				c.ReposDir = v
				return nil
			}, hubOnly: true},
		{"watch.auto_update", "(edge) watch installs the hub's new version on its own: on | off", func(c *config.Config) string { return onOff(c.Watch.AutoUpdate) },
			func(c *config.Config, v string) error { return setOnOff(&c.Watch.AutoUpdate, v) }, false, false},
		{"watch.auto_take", "(edge) folder where watch receives on its own what reaches the outbox, or off", func(c *config.Config) string { return orOff(c.Watch.AutoTake) },
			func(c *config.Config, v string) error {
				if v == "off" || v == "" {
					c.Watch.AutoTake = ""
					return nil
				}
				if !filepath.IsAbs(v) || strings.ContainsAny(v, "\r\n") {
					return fmt.Errorf(`watch.auto_take: give a folder with a full path (e.g. C:\Users\me\Downloads\gitsi) or off: %q`, v)
				}
				c.Watch.AutoTake = v
				return nil
			}, false, false},
		{"watch.report", "(edge) watch leaves each reaction's result in the hub's inbox: on | off", func(c *config.Config) string { return onOff(c.Watch.Report) },
			func(c *config.Config, v string) error { return setOnOff(&c.Watch.Report, v) }, false, false},
		{"watch.auto_fetch", "(edge) watch fetches (git fetch) its folder's repo when it changes on the hub: on | off", func(c *config.Config) string { return onOff(c.Watch.AutoFetch) },
			func(c *config.Config, v string) error { return setOnOff(&c.Watch.AutoFetch, v) }, false, false},
		{"remote_name", "name of the bridge remote in your repos", func(c *config.Config) string { return c.RemoteName },
			func(c *config.Config, v string) error {
				if err := safe.Name("remote_name", v); err != nil {
					return err
				}
				c.RemoteName = v
				return nil
			}, false, false},
	}
}

func findSetting(name string) (setting, bool) {
	for _, s := range settings() {
		if s.name == name {
			return s, true
		}
	}
	return setting{}, false
}

func keyNames() string {
	var n []string
	for _, s := range settings() {
		n = append(n, s.name)
	}
	return strings.Join(n, ", ")
}

const configUsage = "usage: gitsi config [show] | get <key> | set <key> <value> | path"

func configCmd(_ context.Context, env *module.Env, args []string) error {
	if len(args) == 0 || args[0] == "show" {
		b, err := json.MarshalIndent(env.Config, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(env.Stdout, string(b))
		return nil
	}
	switch args[0] {
	case "path":
		p, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Fprintln(env.Stdout, p)
		return nil
	case "get":
		if len(args) != 2 {
			return errors.New(configUsage)
		}
		s, ok := findSetting(args[1])
		if !ok {
			return fmt.Errorf("unknown key %q. Keys: %s", args[1], keyNames())
		}
		if s.hubOnly && env.Config.Role != config.RoleHub {
			fmt.Fprintln(env.Stdout, "(decided by the hub: 'gitsi check' shows its layout)")
			return nil
		}
		fmt.Fprintln(env.Stdout, s.get(env.Config))
		return nil
	case "set":
		if len(args) != 3 {
			return errors.New(configUsage)
		}
		return setSetting(env, args[1], args[2])
	}
	return errors.New(configUsage)
}

func setSetting(env *module.Env, key, value string) error {
	if key == "role" {
		return errors.New("the role is not changed here: use 'gitsi setup edge' or 'gitsi setup hub'")
	}
	s, ok := findSetting(key)
	if !ok {
		return fmt.Errorf("unknown key %q. Keys: %s", key, keyNames())
	}
	if s.hubOnly && env.Config.Role != config.RoleHub {
		return fmt.Errorf("%s is decided by the hub (its folder layout): run 'gitsi config set %s <path>' on the hub; the edges discover it on their own", key, key)
	}
	old := s.get(env.Config)
	if err := s.set(env.Config, value); err != nil {
		return err
	}
	if err := env.Config.Save(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "%s: %q -> %q\n", key, old, s.get(env.Config))

	if s.hubOnly {
		home, _ := os.UserHomeDir()
		lay := layout.ForHub(env.Config, home)
		fmt.Fprintln(env.Stdout, "This hub's new layout: repos "+lay.Repos+" | mailbox "+lay.Mailbox+" | gitsi "+lay.Binary)
		fmt.Fprintln(env.Stdout, "The edges discover it on their own (up to 10 min because of their cache). Move your existing folders to the new paths and run 'gitsi update -publish'.")
	}
	if key == "hub.key_file" {
		if _, err := os.Stat(value); err != nil {
			fmt.Fprintf(env.Stdout, "Warning: %s does not exist yet.\n", value)
		}
	}
	if s.ssh && env.Config.Role == config.RoleEdge && env.Config.Hub.Host != "" && env.Config.Hub.User != "" {
		if err := syncSSHConfig(env.Config); err != nil {
			return fmt.Errorf("the configuration was saved, but ~/.ssh/config could not be updated: %w", err)
		}
		fmt.Fprintln(env.Stdout, "Updated the ~/.ssh/config block git uses.")
	}
	return nil
}

// syncSSHConfig regenerates the hub block in ~/.ssh/config from the configuration (only gitsi's block is replaced).
func syncSSHConfig(cfg *config.Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return writeSSHConfig(filepath.Join(home, ".ssh"), cfg)
}

// writeSSHConfig is the ONLY place that writes ~/.ssh/config (git uses it to reach the hub): it replaces only gitsi's block and
// does it atomically, so a power loss or two gitsi processes at once never leave the file half written.
func writeSSHConfig(sshDir string, cfg *config.Config) error {
	file := filepath.Join(sshDir, "config")
	var existing []string
	if b, err := os.ReadFile(file); err == nil {
		existing = strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	}
	alias := cfg.Hub.HostAlias()
	block := HostBlock(alias, cfg.Hub.Host, cfg.Hub.User, cfg.Hub.Port, cfg.Hub.KeyFile)
	return fsx.WriteFileAtomic(file, []byte(strings.Join(MergeSSHConfig(existing, alias, block), "\n")+"\n"), 0o600)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orOff(s string) string {
	if s == "" {
		return "off"
	}
	return s
}

func setOnOff(dst *bool, v string) error {
	switch v {
	case "on":
		*dst = true
	case "off":
		*dst = false
	default:
		return fmt.Errorf("the value must be on or off: %q", v)
	}
	return nil
}

// completeConfig proposes 'config”s actions, the keys for get/set and on/off for the keys that accept it.
func completeConfig(_ context.Context, env *module.Env, args []string) []string {
	switch {
	case len(args) == 0:
		return []string{"show", "get", "set", "path"}
	case len(args) == 1 && (args[0] == "get" || args[0] == "set"):
		var keys []string
		for _, s := range settings() {
			if !s.hubOnly || env.Config.Role == config.RoleHub {
				keys = append(keys, s.name)
			}
		}
		return keys
	case len(args) == 2 && args[0] == "set":
		if s, ok := findSetting(args[1]); ok && (s.get(env.Config) == "on" || s.get(env.Config) == "off") {
			return []string{"on", "off"}
		}
	}
	return nil
}
