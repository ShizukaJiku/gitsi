// Package config stores gitsi's user configuration and policy.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// Role is the side this binary runs on.
type Role string

const (
	RoleEdge Role = "edge" // edge: only consumes (starts every connection)
	RoleHub  Role = "hub"  // hub: serves (repos, mailbox, services)
)

// Hub describes how to reach the hub from the edge.
type Hub struct {
	Host    string `json:"host"`
	User    string `json:"user"`
	Port    int    `json:"port,omitempty"`
	KeyFile string `json:"key_file,omitempty"`
	// PrevKeyFile is the key used before 'setup key': it stays registered on the hub until 'setup key -retire-old'.
	PrevKeyFile string `json:"prev_key_file,omitempty"`
	Alias       string `json:"alias,omitempty"` // alias in ~/.ssh/config used by git (default "gitsi-hub")
}

// HostAlias returns the SSH alias git uses to reach the hub.
func (h Hub) HostAlias() string {
	if h.Alias != "" {
		return h.Alias
	}
	return "gitsi-hub"
}

// Validate checks the values that end up in ssh's arguments and in ~/.ssh/config, with the same rules as setup and 'gitsi config
// set'. config.json may be edited by hand, so the edge checks again before connecting. It is not done in Load: an invalid value
// must not block the very commands that fix it.
func (h Hub) Validate() error {
	if err := safe.Host(h.Host); err != nil {
		return fmt.Errorf("hub.host: %w", err)
	}
	if err := safe.Name("user", h.User); err != nil {
		return fmt.Errorf("hub.user: %w", err)
	}
	if h.Alias != "" {
		if err := safe.Name("alias", h.Alias); err != nil {
			return fmt.Errorf("hub.alias: %w", err)
		}
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("hub.port: invalid port: %d (0-65535)", h.Port)
	}
	for key, v := range map[string]string{"hub.key_file": h.KeyFile, "hub.prev_key_file": h.PrevKeyFile} {
		if strings.ContainsAny(v, "'\"`;\r\n") {
			return fmt.Errorf("%s: invalid key path: %q", key, v)
		}
	}
	return nil
}

// Service is a hub service the edge sees on 127.0.0.1 through a local tunnel (-L).
type Service struct {
	Name       string `json:"name"`
	RemotePort int    `json:"remote_port"`
	LocalPort  int    `json:"local_port"`
}

// Policy enables sensitive features. Everything is off by default except Git.
type Policy struct {
	AllowSend     bool `json:"allow_send"`
	AllowTake     bool `json:"allow_take"`
	AllowServices bool `json:"allow_services"`
	AllowExec     bool `json:"allow_exec"`
	AllowClip     bool `json:"allow_clip"`
	Audit         bool `json:"audit"`
}

// SchemaVersion is the version of the config.json format. Raising it requires a migration in Load.
const SchemaVersion = 1

// Config is the content of ~/.gitsi/config.json.
type Config struct {
	// Version of the format.
	Version int  `json:"version"`
	Role    Role `json:"role"`
	Hub     Hub  `json:"hub"`
	// Home and ReposDir are decided by the HUB (in its own configuration) and announced to the edge in the protocol greeting;
	// the edge never configures them. Empty = defaults (see internal/layout). Unused on an edge.
	Home     string `json:"home,omitempty"`
	ReposDir string `json:"repos_dir,omitempty"`
	// RemoteName is the name of the remote that points to the bridge (hub) in the edge's repos.
	RemoteName string    `json:"remote_name"`
	Services   []Service `json:"services,omitempty"`
	Policy     Policy    `json:"policy"`
	Watch      Watch     `json:"watch"`
	Clip       Clip      `json:"clip"`
}

// Watch holds the automatic reactions of 'gitsi watch' on the edge (all off by default; docs/WATCH.md).
type Watch struct {
	AutoUpdate bool   `json:"auto_update,omitempty"` // install the new version the hub publishes on its own
	AutoTake   string `json:"auto_take,omitempty"`   // folder to receive whatever arrives in the outbox ("" = no)
	Report     bool   `json:"report,omitempty"`      // leave each reaction's result in the hub's inbox
	AutoFetch  bool   `json:"auto_fetch,omitempty"`  // git fetch the repo of watch's folder when it changes on the bridge
}

// Clip tunes the clipboard shared between hub and edge while watch runs (docs/CLIP.md). It also needs the clip policy.
type Clip struct {
	Hotkey    string `json:"hotkey,omitempty"`      // sends what is copied; "" = DefaultClipHotkey
	Auto      bool   `json:"auto,omitempty"`        // send every copy on its own, not only on the hotkey
	MaxSizeMB int    `json:"max_size_mb,omitempty"` // largest item sent or accepted; 0 = DefaultClipMaxSizeMB
}

// Clip defaults and limits.
const (
	DefaultClipHotkey    = "ctrl+shift+alt+c"
	DefaultClipMaxSizeMB = 20
	// MaxClipSizeMB caps clip.max_size_mb: an item is held in memory while it travels.
	MaxClipSizeMB = 256
)

// HotkeyOrDefault returns the configured hotkey or the default one.
func (c Clip) HotkeyOrDefault() string {
	if c.Hotkey == "" {
		return DefaultClipHotkey
	}
	return c.Hotkey
}

// MaxBytes returns the size limit in bytes (the default when unset, never above MaxClipSizeMB).
func (c Clip) MaxBytes() int64 {
	mb := c.MaxSizeMB
	if mb <= 0 {
		mb = DefaultClipMaxSizeMB
	}
	return int64(min(mb, MaxClipSizeMB)) << 20
}

// Default returns a safe configuration: edge role, closed policy, audit on.
func Default() *Config {
	return &Config{Version: SchemaVersion, Role: RoleEdge, RemoteName: "hub", Policy: Policy{Audit: true}}
}

// Path returns the path of the configuration file.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gitsi", "config.json"), nil
}

// Load reads the configuration; when it does not exist it returns Default().
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := fsx.ReadFileRetry(p) // another gitsi process may be replacing it (Save) right now
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	c := Default()
	c.Version = 0 // to detect a file without "version"
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("invalid config (%s): %w", p, err)
	}
	switch {
	case c.Version == 0: // no "version": gitsi did not write it
		return nil, fault.New(fault.NotConfigured,
			fmt.Sprintf("the configuration (%s) has no version: gitsi did not write it", p),
			"set this machine up again: gitsi setup edge ... (edge) or gitsi setup hub ... (hub)")
	case c.Version > SchemaVersion:
		return nil, fault.New(fault.NotConfigured,
			fmt.Sprintf("the configuration (%s) was created by a newer gitsi (format %d; this one understands up to %d)", p, c.Version, SchemaVersion),
			"update gitsi: gitsi update")
	}
	return c, nil
}

// Save writes the configuration atomically: to its own temporary file, then renamed over the target. Even if two gitsi processes
// save at once or power is lost, a reader sees either the old or the new version, never a mix or a half-written file.
func (c *Config) Save() error {
	c.Version = SchemaVersion
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p, b, 0o600)
}

// Exists reports whether a configuration file exists (that is, whether this machine was set up).
func Exists() bool {
	p, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
