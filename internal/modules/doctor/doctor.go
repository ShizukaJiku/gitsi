// Package doctor checks that the environment is ready (tools, VPN, connectivity, login, listening ports).
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "doctor" }

func (mod) Commands() []module.Command {
	return []module.Command{
		{Name: "check", Summary: "Diagnose without changing anything; --active tests send and run", Roles: module.Both, Run: check, Flags: []string{"-active", "-out"}},
	}
}

// report prints the results and returns an error when any is FAIL.
func report(w io.Writer, rs []Result, m Mode) error {
	var pass, info, warn, fail int
	for _, r := range rs {
		if !m.JSON && !(m.Quiet && (r.Level == Pass || r.Level == Info)) { // -q: warnings and failures only
			fmt.Fprintf(w, "[%s] %-26s %s\n", r.Level, r.Name, r.Detail)
		}
		switch r.Level {
		case Pass:
			pass++
		case Info:
			info++
		case Warn:
			warn++
		case Fail:
			fail++
		}
	}
	if m.JSON {
		if err := writeJSON(w, rs, pass, info, warn, fail); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(w, "\nSummary: %d OK, %d warnings, %d failures\n", pass, warn, fail)
	}
	if fail > 0 {
		return errors.New("some checks failed")
	}
	return nil
}

// EdgeChecks diagnoses the edge: tools, configuration, VPN, connectivity, login, hub and ports.
func EdgeChecks(ctx context.Context, cfg *config.Config, tr transport.Transport, d Deps, o Options) []Result {
	var rs []Result
	toolChecks(ctx, d, &rs, "ssh", "git", "scp")

	h := cfg.Hub
	if h.Host == "" || h.User == "" {
		add(&rs, Fail, "configuration", "the hub is missing: run 'gitsi setup edge -host <hub-ip> -user <user>'")
		return rs
	}
	if err := h.Validate(); err != nil {
		add(&rs, Fail, "configuration", "%v: fix it with 'gitsi config set <key> <value>'", err)
		return rs
	}
	add(&rs, Pass, "configuration", "%s@%s", h.User, h.Host)
	if h.KeyFile != "" {
		if d.FileExists(h.KeyFile) {
			add(&rs, Pass, "SSH key", "%s", h.KeyFile)
		} else {
			add(&rs, Fail, "SSH key", "%s does not exist", h.KeyFile)
		}
	}

	vpnCheck(ctx, d, &rs)

	port := h.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(h.Host, strconv.Itoa(port))
	if err := d.Dial(addr, 4*time.Second); err != nil {
		add(&rs, Fail, "TCP to the hub", "%s does not answer: %v (is the network, a VPN or the hub's firewall blocking it?)", addr, err)
		listenerChecks(ctx, d, &rs, servicePorts(cfg), false)
		return rs
	}
	add(&rs, Pass, "TCP to the hub", "%s reachable", addr)

	if tr == nil {
		add(&rs, Fail, "transport", "no SSH transport configured")
		return rs
	}
	res, err := tr.Run(ctx, "echo gitsi-ok")
	switch {
	case err != nil:
		add(&rs, Fail, "SSH login", "could not run ssh: %v", err)
		return rs
	case !strings.Contains(res.Stdout, "gitsi-ok"):
		add(&rs, Fail, "SSH login", "failed (exit code %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		return rs
	}
	add(&rs, Pass, "SSH login", "key authentication works")

	// The folder layout is decided by the hub: it is asked (never configured on the edge).
	lay, lerr := resolveLayout(ctx, cfg, o)
	switch {
	case lerr == nil:
		add(&rs, Pass, "hub layout", "repos: %s | mailbox: %s | gitsi: %s", lay.Repos, lay.Mailbox, lay.Binary)
	default:
		add(&rs, Warn, "hub layout", "could not be learned: %s", describe(lerr))
	}

	reposProbe := "'?'"
	if lerr == nil {
		reposProbe = "$(Test-Path '" + lay.Repos + "')"
	}
	probe := `"ps=$($PSVersionTable.PSVersion.ToString()) git=$([bool](Get-Command git -ErrorAction SilentlyContinue)) repos=` + reposProbe + `"`
	res, err = tr.Run(ctx, probe)
	out := strings.TrimSpace(res.Stdout)
	if err != nil || !strings.HasPrefix(out, "ps=") {
		add(&rs, Fail, "hub shell", "it does not look like PowerShell (Git will fail on the quotes); run 'gitsi setup hub' on the hub")
	} else {
		add(&rs, Pass, "hub shell", "PowerShell %s", field(out, "ps"))
		if field(out, "git") == "True" {
			add(&rs, Pass, "git on the hub", "installed")
		} else {
			add(&rs, Fail, "git on the hub", "not on the SSH session's PATH")
		}
		if lerr == nil {
			if field(out, "repos") == "True" {
				add(&rs, Pass, "repos folder", "%s exists", lay.Repos)
			} else {
				add(&rs, Info, "repos folder", "%s does not exist yet (the hub creates it with the first 'share')", lay.Repos)
			}
		}
	}

	listenerChecks(ctx, d, &rs, servicePorts(cfg), false)
	policyChecks(cfg, &rs)

	if o.Tunnel {
		tunnelCheck(ctx, d, tr, port, &rs)
	}
	if o.Serve {
		serveCheck(ctx, cfg, tr, &rs)
	}
	return rs
}

// LayoutFunc returns the hub's layout (normally env.HubLayout).
type LayoutFunc func(ctx context.Context) (layout.Layout, error)

// resolveLayout gets the hub's layout with the given function (the hub announces it).
func resolveLayout(ctx context.Context, cfg *config.Config, o Options) (layout.Layout, error) {
	if o.Layout != nil {
		return o.Layout(ctx)
	}
	return layout.Layout{}, fault.New(fault.NotConfigured, "there is no way to learn the hub's layout", "")
}

// describe summarizes an error (with its action when it is a fault) in one line.
func describe(err error) string {
	if fe, ok := fault.As(err); ok {
		if fe.Hint != "" {
			return fe.Error() + " -> " + fe.Hint
		}
		return fe.Error()
	}
	return err.Error()
}

func field(s, key string) string {
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, key+"=") {
			return strings.TrimPrefix(f, key+"=")
		}
	}
	return ""
}

func servicePorts(cfg *config.Config) []int {
	var p []int
	for _, s := range cfg.Services {
		p = append(p, s.LocalPort)
	}
	return p
}

func policyChecks(cfg *config.Config, rs *[]Result) {
	p := cfg.Policy
	var on []string
	for name, v := range map[string]bool{"send": p.AllowSend, "take": p.AllowTake, "services": p.AllowServices, "exec": p.AllowExec} {
		if v {
			on = append(on, name)
		}
	}
	if len(on) == 0 {
		add(rs, Pass, "optional features", "all off (Git only)")
		return
	}
	add(rs, Info, "optional features", "enabled: %s", strings.Join(on, ", "))
}

// tunnelCheck opens a temporary -L to the hub's sshd, checks that 127.0.0.1 answers and closes it.
func tunnelCheck(ctx context.Context, d Deps, tr transport.Transport, hubPort int, rs *[]Result) {
	lp, err := freePort()
	if err != nil {
		add(rs, Fail, "-L tunnel", "no free local port: %v", err)
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	t, err := tr.Forward(tctx, []transport.Forward{{LocalPort: lp, RemotePort: hubPort}})
	if err != nil {
		add(rs, Fail, "-L tunnel", "could not open: %v", err)
		return
	}
	defer t.Close()
	local := net.JoinHostPort("127.0.0.1", strconv.Itoa(lp))
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if d.Dial(local, time.Second) == nil {
			add(rs, Pass, "-L tunnel", "127.0.0.1:%d -> hub's sshd works (tunnel closed)", lp)
			return
		}
	}
	add(rs, Fail, "-L tunnel", "the tunnel did not answer in 8 s (AllowTcpForwarding on the hub or a network rule)")
}

// HubChecks diagnoses the hub: tools, sshd, firewall, repos and exposed ports.
func HubChecks(ctx context.Context, cfg *config.Config, d Deps) []Result {
	var rs []Result
	toolChecks(ctx, d, &rs, "git", "ssh")

	ps := func(script string) string {
		out, _ := d.Output(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
		return strings.TrimSpace(out)
	}
	if st := ps("(Get-Service sshd -ErrorAction SilentlyContinue).Status"); st == "Running" {
		add(&rs, Pass, "sshd service", "running")
	} else {
		add(&rs, Fail, "sshd service", "not running (%q); run 'gitsi setup hub'", st)
	}
	if ps("[bool](Get-NetFirewallRule -Name 'gitsi-ssh' -ErrorAction SilentlyContinue)") == "True" {
		add(&rs, Pass, "firewall", "gitsi-ssh rule present (edge's IP only)")
	} else {
		add(&rs, Warn, "firewall", "the gitsi-ssh rule does not exist; run 'gitsi setup hub'")
	}
	home, _ := os.UserHomeDir()
	lay := layout.ForHub(cfg, home) // the hub decides its layout (config.home / config.repos_dir, with defaults)
	add(&rs, Info, "layout", "home: %s | repos: %s | mailbox: %s", lay.Home, lay.Repos, lay.Mailbox)
	if err := lay.Check(); err != nil {
		// the edges would reject this layout (it ends up in PowerShell commands): say it here, with the way out
		add(&rs, Fail, "layout paths", "%v: edges cannot use it; %s", err, layout.FixHint)
	}
	if d.FileExists(filepath.FromSlash(lay.Repos)) {
		add(&rs, Pass, "repos folder", "%s", lay.Repos)
	} else {
		add(&rs, Info, "repos folder", "%s does not exist yet (created with the first 'share')", lay.Repos)
	}
	if d.FileExists(filepath.FromSlash(lay.Binary)) {
		add(&rs, Pass, "published gitsi", "%s (what the edges download with 'gitsi update')", lay.Binary)
	} else {
		add(&rs, Warn, "published gitsi", "no binary at %s: run 'gitsi update -publish'", lay.Binary)
	}
	var pids []string
	for _, f := range strings.Fields(ps("(Get-Process sshd -ErrorAction SilentlyContinue).Id")) {
		if _, err := strconv.Atoi(f); err == nil {
			pids = append(pids, f)
		}
	}
	listenerChecks(ctx, d, &rs, nil, true, pids...)
	return rs
}

// serveCheck opens a real session with `gitsi serve --stdio` on the hub and reports which launch form worked.
func serveCheck(ctx context.Context, cfg *config.Config, tr transport.Transport, rs *[]Result) {
	c, err := hubclient.Dial(ctx, tr, "drop")
	if err != nil {
		add(rs, Fail, "hub protocol", "%v", err)
		return
	}
	defer c.Close()
	add(rs, Pass, "hub protocol", "%s answers (method %s)", c.Hello.Agent, c.Method)
	if _, err := layout.FromInfo(c.Hello.Info); err != nil {
		add(rs, Warn, "announced layout", "%v (update the hub's gitsi: 'gitsi update -publish')", err)
	}
	if !c.Has("drop") {
		add(rs, Warn, "mailbox", "the hub does not announce the mailbox: on the hub: gitsi update -publish (and, if it was never set up, gitsi setup hub)")
	}
}

// Mode is the output mode of the diagnosis report.
type Mode struct {
	Quiet bool // warnings and failures only
	JSON  bool // a JSON object instead of text
}

type jsonResult struct {
	Level  string `json:"level"` // ok | info | warn | fail
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type jsonReport struct {
	Results []jsonResult   `json:"results"`
	Summary map[string]int `json:"summary"`
}

func writeJSON(w io.Writer, rs []Result, pass, info, warn, fail int) error {
	rep := jsonReport{Results: []jsonResult{}, Summary: map[string]int{"ok": pass, "info": info, "warn": warn, "fail": fail}}
	for _, r := range rs {
		rep.Results = append(rep.Results, jsonResult{Level: strings.ToLower(strings.TrimSpace(r.Level.String())), Name: r.Name, Detail: r.Detail})
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}
