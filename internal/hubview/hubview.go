// Package hubview is how the edge discovers the hub's layout (where its repos, mailbox, jobs and binary are).
//
// The edge configures no hub path: it asks for it in a protocol greeting (proto.Hello.Info) and caches it for a while so it does
// not add a round trip to the hub on every command. The cache is keyed by user@host:port, so pointing at another hub invalidates
// it on its own.
package hubview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// DefaultTTL is how long the cached layout is trusted.
const DefaultTTL = 10 * time.Minute

// Dialer opens a session with the hub. It is injected to test without SSH.
type Dialer func(ctx context.Context, tr transport.Transport, caps ...string) (*hubclient.Conn, error)

// Resolver discovers and caches the hub's layout.
type Resolver struct {
	Transport transport.Transport
	Config    *config.Config
	CachePath string // default ~/.gitsi/hub-layout.json
	TTL       time.Duration
	Now       func() time.Time
	Dial      Dialer
	// Debugf, when not nil, reports what was done (cache or greeting). gitsi -v uses it.
	Debugf func(format string, a ...any)
}

type cacheFile struct {
	Key        string        `json:"key"`
	Fetched    time.Time     `json:"fetched"`
	Layout     layout.Layout `json:"layout"`
	HubVersion string        `json:"hub_version,omitempty"`
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Resolver) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return DefaultTTL
}

func (r *Resolver) debugf(f string, a ...any) {
	if r.Debugf != nil {
		r.Debugf(f, a...)
	}
}

func (r *Resolver) key() string {
	h := r.Config.Hub
	port := h.Port
	if port == 0 {
		port = 22
	}
	return h.User + "@" + h.Host + ":" + strconv.Itoa(port)
}

func (r *Resolver) cachePath() string {
	if r.CachePath != "" {
		return r.CachePath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gitsi", "hub-layout.json")
}

// Layout returns the hub's layout: from the cache when it is recent, otherwise by asking the hub.
func (r *Resolver) Layout(ctx context.Context) (layout.Layout, error) {
	if c, ok := r.readCache(); ok && r.now().Sub(c.Fetched) < r.ttl() {
		r.debugf("hub layout from the cache (%s ago)", r.now().Sub(c.Fetched).Round(time.Second))
		return c.Layout, nil
	}
	l, _, err := r.Refresh(ctx)
	return l, err
}

// Refresh ignores the cache: it greets the hub, reads what it announces and updates the cache. It also returns the version of the
// hub's gitsi.
func (r *Resolver) Refresh(ctx context.Context) (layout.Layout, string, error) {
	dial := r.Dial
	if dial == nil {
		dial = hubclient.Dial
	}
	conn, err := dial(ctx, r.Transport)
	if err != nil {
		return layout.Layout{}, "", err
	}
	defer conn.Close()
	r.debugf("hub layout requested in the greeting (method %s)", conn.Method)

	hubVersion := conn.Hello.Info["version"]
	l, err := layout.FromInfo(conn.Hello.Info)
	if errors.Is(err, layout.ErrNotAnnounced) {
		return layout.Layout{}, "", fault.New(fault.HubBinary, "the hub's gitsi does not announce its folder layout (it is outdated)",
			"on the hub: gitsi update -publish (with the current gitsi)")
	}
	if err != nil {
		return layout.Layout{}, "", fault.New(fault.Other, err.Error(), "update gitsi on the hub: gitsi update -publish")
	}
	r.writeCache(cacheFile{Key: r.key(), Fetched: r.now(), Layout: l, HubVersion: hubVersion})
	return l, hubVersion, nil
}

func (r *Resolver) readCache() (cacheFile, bool) {
	p := r.cachePath()
	if p == "" {
		return cacheFile{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return cacheFile{}, false
	}
	var c cacheFile
	if json.Unmarshal(b, &c) != nil || c.Key != r.key() {
		return cacheFile{}, false
	}
	// the cache lives on disk: it is validated just like what arrives from the hub
	if _, err := layout.FromInfo(c.Layout.Info()); err != nil {
		return cacheFile{}, false
	}
	return c, true
}

func (r *Resolver) writeCache(c cacheFile) {
	p := r.cachePath()
	if p == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	if b, err := json.MarshalIndent(c, "", "  "); err == nil {
		_ = os.WriteFile(p, b, 0o600) // only a cache: if it cannot be written, the hub is asked again
	}
}
