// Package services exposes hub services (postgres, redis, ...) on the edge's 127.0.0.1 through local tunnels (-L). Policy: it
// requires Policy.AllowServices. Only -L, always loopback: the edge exposes nothing.
package services

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/safe"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "services" }

func (mod) Commands() []module.Command {
	return []module.Command{
		{Group: "svc", Name: "add", Summary: "Add a service: <name> [hub-port] [-local N]", Roles: module.Edge, Run: addCmd, Flags: []string{"-local"}},
		{Group: "svc", Name: "rm", Summary: "Remove a service: <name>", Roles: module.Edge, Run: rmCmd, Complete: completeNames(false)},
		{Group: "svc", Name: "ls", Summary: "Services: local tunnel and whether they answer on the hub", Roles: module.Edge, Run: lsCmd},
		{Group: "svc", Name: "up", Summary: "Open the tunnels in the foreground (Ctrl+C closes them)", Roles: module.Edge, Feature: policy.Services, NeedsHub: true, Run: upCmd, Complete: completeNames(true), Flags: []string{"-docker"}},
	}
}

// Presets are common ports so nobody has to remember them; `add <name>` uses them when no port is given.
var Presets = map[string]int{
	"postgres": 5432, "mysql": 3306, "mariadb": 3306, "redis": 6379, "mongo": 27017,
	"rabbitmq": 5672, "kafka": 9092, "elasticsearch": 9200, "http": 8080, "minio": 9000,
}

// Add adds a validated service. local==0 uses the same port as the hub.
func Add(cfg *config.Config, name string, remote, local int) (config.Service, error) {
	if err := safe.Name("service", name); err != nil {
		return config.Service{}, err
	}
	if remote == 0 {
		p, ok := Presets[strings.ToLower(name)]
		if !ok {
			return config.Service{}, fmt.Errorf("%q has no predefined port: give the hub's port (gitsi svc add %s <port>)", name, name)
		}
		remote = p
	}
	if local == 0 {
		local = remote
	}
	if remote < 1 || remote > 65535 {
		return config.Service{}, fmt.Errorf("invalid hub port: %d", remote)
	}
	if local < 1024 || local > 65535 {
		return config.Service{}, fmt.Errorf("invalid local port: %d (use 1024-65535; privileged ports need admin rights)", local)
	}
	for _, s := range cfg.Services {
		if s.Name == name {
			return config.Service{}, fmt.Errorf("%q already exists (local port %d); use 'gitsi svc rm %s' before changing it", name, s.LocalPort, name)
		}
		if s.LocalPort == local {
			return config.Service{}, fmt.Errorf("local port %d is already used by %q; choose another with -local", local, s.Name)
		}
	}
	svc := config.Service{Name: name, RemotePort: remote, LocalPort: local}
	cfg.Services = append(cfg.Services, svc)
	return svc, nil
}

// Remove removes a service by name.
func Remove(cfg *config.Config, name string) error {
	for i, s := range cfg.Services {
		if s.Name == name {
			cfg.Services = append(cfg.Services[:i], cfg.Services[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("there is no service %q", name)
}

// Select returns the requested services (all when names is empty).
func Select(cfg *config.Config, names []string) ([]config.Service, error) {
	if len(names) == 0 {
		if len(cfg.Services) == 0 {
			return nil, errors.New("the catalog is empty: use 'gitsi svc add <name> [port]'")
		}
		return cfg.Services, nil
	}
	var out []config.Service
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] { // a repeated name would open two tunnels on the same local port
			continue
		}
		seen[n] = true
		found := false
		for _, s := range cfg.Services {
			if s.Name == n {
				out = append(out, s)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("there is no service %q (see 'gitsi svc ls')", n)
		}
	}
	return out, nil
}

// Forwards turns services into local tunnels. The target is always the hub's loopback.
func Forwards(svcs []config.Service) []transport.Forward {
	var f []transport.Forward
	for _, s := range svcs {
		f = append(f, transport.Forward{LocalPort: s.LocalPort, RemotePort: s.RemotePort})
	}
	return f
}

func parseFlags(name string, env *module.Env, args []string, setup func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	setup(fs)
	return module.ParseInterleaved(fs, args)
}

func addCmd(_ context.Context, env *module.Env, args []string) error {
	var local int
	pos, err := parseFlags("add", env, args, func(fs *flag.FlagSet) {
		fs.IntVar(&local, "local", 0, "local port on 127.0.0.1 (default: the hub's port)")
	})
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New("usage: gitsi svc add <name> [hub-port] [-local N]")
	}
	remote := 0
	if len(pos) == 2 {
		if remote, err = strconv.Atoi(pos[1]); err != nil {
			return fmt.Errorf("invalid port: %q", pos[1])
		}
	}
	svc, err := Add(env.Config, pos[0], remote, local)
	if err != nil {
		return err
	}
	if err := env.Config.Save(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Added: %s  127.0.0.1:%d -> hub:%d\n", svc.Name, svc.LocalPort, svc.RemotePort)
	return nil
}

func rmCmd(_ context.Context, env *module.Env, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gitsi svc rm <name>")
	}
	if err := Remove(env.Config, args[0]); err != nil {
		return err
	}
	if err := env.Config.Save(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Removed: %s\n", args[0])
	return nil
}

// hubProbeCmd builds the PowerShell command that checks, on the hub, whether each port accepts local connections.
func hubProbeCmd(svcs []config.Service) string {
	var b strings.Builder
	for _, s := range svcs {
		fmt.Fprintf(&b, "$p=%d;try{$c=New-Object Net.Sockets.TcpClient('127.0.0.1',$p);$c.Close();\"$p=up\"}catch{\"$p=down\"};", s.RemotePort)
	}
	return b.String()
}

// parseProbe reads "5432=up" per line.
func parseProbe(out string) map[int]bool {
	m := map[int]bool{}
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		if p, err := strconv.Atoi(k); err == nil {
			m[p] = v == "up"
		}
	}
	return m
}

func lsCmd(ctx context.Context, env *module.Env, _ []string) error {
	svcs := append([]config.Service(nil), env.Config.Services...)
	if len(svcs) == 0 {
		if env.JSON {
			return env.PrintJSON([]svcRow{})
		}
		fmt.Fprintln(env.Stdout, "Empty catalog. Example: gitsi svc add postgres")
		return nil
	}
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })

	var hub map[int]bool
	if env.Transport != nil {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if res, err := env.Transport.Run(pctx, hubProbeCmd(svcs)); err == nil && res.ExitCode == 0 {
			hub = parseProbe(res.Stdout)
		}
	}
	enabled := "off"
	if policy.Check(env.Config.Policy, policy.Services) == nil {
		enabled = "enabled"
	}
	var rows []svcRow
	if !env.JSON {
		fmt.Fprintf(env.Stdout, "Services policy: %s\n\n%-14s %-26s %-10s %s\n", enabled, "NAME", "LOCAL -> HUB", "TUNNEL", "SERVICE ON THE HUB")
	}
	for _, s := range svcs {
		tun := "closed"
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.LocalPort)), 300*time.Millisecond); err == nil {
			c.Close()
			tun = "open"
		}
		hs := "?"
		if hub != nil {
			hs = map[bool]string{true: "answers", false: "no answer"}[hub[s.RemotePort]]
		}
		rows = append(rows, svcRow{Name: s.Name, LocalPort: s.LocalPort, RemotePort: s.RemotePort, Tunnel: tun, Hub: hs})
		if !env.JSON {
			fmt.Fprintf(env.Stdout, "%-14s %-26s %-10s %s\n", s.Name, fmt.Sprintf("127.0.0.1:%d -> :%d", s.LocalPort, s.RemotePort), tun, hs)
		}
	}
	if env.JSON {
		return env.PrintJSON(rows)
	}
	return nil
}

// svcRow is one row of `svc ls` (also its --json form).
type svcRow struct {
	Name       string `json:"name"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Tunnel     string `json:"tunnel"` // open | closed
	Hub        string `json:"hub"`    // answers | no answer | ?
}

// portFree checks that 127.0.0.1:port can be listened on.
func portFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func upCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := flag.NewFlagSet("svc up", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	docker := fs.Bool("docker", false, "one tunnel per port the hub's containers publish, on the same local port (no catalog)")
	wanted, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	var svcs []config.Service
	if *docker {
		if len(wanted) > 0 {
			return errors.New("-docker opens the ports of the hub's containers: it does not combine with catalog names")
		}
		found, err := dockerServices(ctx, env)
		if err != nil {
			return err
		}
		for _, s := range found {
			if !portFree(s.LocalPort) {
				fmt.Fprintf(env.Stderr, "warning: %s skipped: local port %d is already in use\n", s.Name, s.LocalPort)
				continue
			}
			svcs = append(svcs, s)
		}
		if len(svcs) == 0 {
			return errors.New("no hub container publishes a TCP port that is free here (on the hub: docker run -p <port>:<port> ...)")
		}
	} else {
		if err := addKnown(env, wanted); err != nil {
			return err
		}
		if svcs, err = Select(env.Config, wanted); err != nil {
			return err
		}
		for _, s := range svcs {
			if !portFree(s.LocalPort) {
				return fmt.Errorf("local port %d (%s) is already in use; change it with 'gitsi svc rm' and 'gitsi svc add -local'", s.LocalPort, s.Name)
			}
		}
	}
	t, err := env.Transport.Forward(ctx, Forwards(svcs))
	if err != nil {
		return fmt.Errorf("could not open the tunnels: %w", err)
	}
	var names []string
	for _, s := range svcs {
		names = append(names, fmt.Sprintf("%s:%d->%d", s.Name, s.LocalPort, s.RemotePort))
		fmt.Fprintf(env.Stdout, "%-14s 127.0.0.1:%d -> hub:%d\n", s.Name, s.LocalPort, s.RemotePort)
	}
	_ = policy.Audit(env.Config.Policy, "services.up", strings.Join(names, ","))
	fmt.Fprintln(env.Stdout, "Tunnels open (on 127.0.0.1 only). Ctrl+C to close.")

	done := make(chan error, 1)
	go func() { done <- t.Wait() }()
	select {
	case <-ctx.Done():
		_ = t.Close()
		<-done
		_ = policy.Audit(env.Config.Policy, "services.down", strings.Join(names, ","))
		fmt.Fprintln(env.Stdout, "Tunnels closed.")
		return nil
	case err := <-done:
		_ = policy.Audit(env.Config.Policy, "services.down", strings.Join(names, ","))
		if err != nil {
			if td, ok := t.(transport.TunnelDiagnostics); ok {
				if fe := td.Fault(); fe != nil {
					return fe
				}
			}
			return fmt.Errorf("the tunnel ended: %w (did the hub refuse forwarding, or did the network drop?)", err)
		}
		return nil
	}
}

// addKnown adds to the catalog the services asked of 'svc up' that are missing but have a known port (postgres, redis...): that
// way 'gitsi svc up postgres' works without a previous 'svc add'.
func addKnown(env *module.Env, names []string) error {
	changed := false
	for _, n := range names {
		if _, err := Select(env.Config, []string{n}); err == nil {
			continue
		}
		if _, ok := Presets[strings.ToLower(n)]; !ok {
			continue // neither present nor known: Select will say so
		}
		svc, err := Add(env.Config, n, 0, 0)
		if err != nil {
			return err
		}
		changed = true
		fmt.Fprintf(env.Stdout, "Added to the catalog: %s  127.0.0.1:%d -> hub:%d\n", svc.Name, svc.LocalPort, svc.RemotePort)
	}
	if changed {
		return env.Config.Save()
	}
	return nil
}

// completeNames proposes the catalog's services (and, for 'svc up', also the known ones that get added on their own).
func completeNames(known bool) func(context.Context, *module.Env, []string) []string {
	return func(_ context.Context, env *module.Env, _ []string) []string {
		var names []string
		for _, s := range env.Config.Services {
			names = append(names, s.Name)
		}
		if known {
			for n := range Presets {
				names = append(names, n)
			}
		}
		return names
	}
}
