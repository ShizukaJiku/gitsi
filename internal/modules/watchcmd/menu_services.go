package watchcmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// svcRow is a service as `gitsi --json svc ls` lists it.
type svcRow struct {
	Name       string `json:"name"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Tunnel     string `json:"tunnel"` // open | closed
	Hub        string `json:"hub"`
}

// startTunnel runs `gitsi svc up <name>` until stop is called, together with the ssh.exe it starts (procx.StartTree), and sends
// on done why it ended (a variable in tests). Tunnels opened from the menu stay visible in it and close with it: Quit closes them
// all (docs/PHILOSOPHY.md: nothing hidden or unattended).
var startTunnel = func(name string) (stop func(), done <-chan string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	var errb bytes.Buffer
	cmd := exec.Command(exe, "svc", "up", name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(""), &bytes.Buffer{}, &errb
	stop, err = procx.StartTree(procx.Background(cmd))
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan string, 1)
	go func() {
		err := cmd.Wait()
		if err == nil {
			ch <- "it ended"
			return
		}
		ch <- lastLine(errb.String(), err)
	}()
	return stop, ch, nil
}

// tunnel is a tunnel the menu opened.
type tunnel struct {
	stop    func()
	closing bool // closed from the menu: its end is not news
}

// tunnels are the tunnels the menu opened, by service name.
type tunnels struct {
	mu sync.Mutex
	m  map[string]*tunnel
}

func (t *tunnels) open(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[name] != nil
}

func (t *tunnels) names() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for n := range t.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// closeAll closes every tunnel (Quit, the end of the session).
func (t *tunnels) closeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tn := range t.m {
		tn.closing = true
		tn.stop()
	}
}

func (s *session) refreshServices(ctx context.Context) {
	if !s.tray || s.isHub() {
		return
	}
	s.services.load(func() ([]svcRow, string) {
		var rows, ok []svcRow
		if reason := listJSON(ctx, &rows, "svc", "ls"); reason != "" {
			return nil, reason
		}
		for _, r := range rows {
			if safe.Name("service", r.Name) == nil {
				ok = append(ok, r)
			}
		}
		return ok, ""
	})
	s.refresh()
}

func (s *session) servicesMenu(ctx context.Context) desktop.MenuItem {
	rows, reason, at := s.services.get()
	var sub []desktop.MenuItem
	if !s.featureOn(policy.Services) {
		sub = append(s.offNote(ctx, policy.Services, "Opening tunnels to the hub's services"), desktop.MenuItem{})
	}
	switch {
	case at.IsZero():
		sub = append(sub, desktop.MenuItem{Label: "Reading the services...", Disabled: true})
	case reason != "":
		sub = append(sub, desktop.MenuItem{Label: "Could not read them: " + menuText(reason, 60), Disabled: true})
	case len(rows) == 0:
		sub = append(sub, desktop.MenuItem{Label: "No services yet. Add one in a terminal: gitsi svc add postgres", Disabled: true})
	default:
		sub = append(sub, desktop.MenuItem{Label: "Tunnels to the hub, on this machine only (127.0.0.1)", Disabled: true})
		for _, r := range rows {
			sub = append(sub, s.serviceEntry(ctx, r))
		}
	}
	open := s.tunnels.names()
	sub = append(sub, desktop.MenuItem{})
	if len(open) > 0 {
		sub = append(sub, desktop.MenuItem{Label: "Close all tunnels", Icon: desktop.GlyphStop, Action: func() {
			s.tunnels.closeAll()
			s.refresh()
		}})
	}
	sub = append(sub, desktop.MenuItem{Label: "Refresh", Icon: desktop.GlyphRefresh, Action: func() { s.refreshServices(ctx) }})
	return desktop.MenuItem{Label: countLabel("Services", len(open), "open"), Icon: desktop.GlyphPlug, Sub: sub}
}

// serviceEntry is a service: checked while the menu's tunnel is open; choosing it opens or closes that tunnel. A tunnel opened
// elsewhere (svc up in a terminal) is shown but left alone.
func (s *session) serviceEntry(ctx context.Context, r svcRow) desktop.MenuItem {
	where := fmt.Sprintf("localhost:%d", r.LocalPort)
	if s.tunnels.open(r.Name) {
		return desktop.MenuItem{Label: menuText(r.Name, 40) + "\t" + where, Checked: true, Action: func() { s.closeTunnel(r.Name) }}
	}
	if r.Tunnel == "open" {
		return desktop.MenuItem{Label: menuText(r.Name, 40) + "\topen elsewhere", Disabled: true}
	}
	return desktop.MenuItem{Label: menuText(r.Name, 40) + "\t" + where, Icon: desktop.GlyphPlug, Action: func() { s.openTunnel(ctx, r) }}
}

func (s *session) openTunnel(ctx context.Context, r svcRow) {
	if !s.featureOn(policy.Services) && !s.enableFeature(ctx, policy.Services) {
		return
	}
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing || s.tunnels.open(r.Name) {
		return
	}
	stop, done, err := startTunnel(r.Name)
	if err != nil {
		s.notify("gitsi: could not open the tunnel to "+r.Name, err.Error(), true)
		return
	}
	tn := &tunnel{stop: stop}
	s.tunnels.mu.Lock()
	if s.tunnels.m == nil {
		s.tunnels.m = map[string]*tunnel{}
	}
	s.tunnels.m[r.Name] = tn
	s.tunnels.mu.Unlock()
	s.printf("tray: tunnel to %s open on localhost:%d", r.Name, r.LocalPort)
	s.notify("gitsi", fmt.Sprintf("%s is on localhost:%d while the tunnel stays open (Services in this menu).", r.Name, r.LocalPort), false)
	s.refresh()
	s.background(func() {
		why := <-done
		s.tunnels.mu.Lock()
		closing := tn.closing
		if s.tunnels.m[r.Name] == tn {
			delete(s.tunnels.m, r.Name)
		}
		s.tunnels.mu.Unlock()
		s.printf("tray: tunnel to %s closed (%s)", r.Name, why)
		if !closing {
			s.notify("gitsi: the tunnel to "+r.Name+" closed", why, true)
		}
		s.refresh()
	})
}

func (s *session) closeTunnel(name string) {
	s.tunnels.mu.Lock()
	tn := s.tunnels.m[name]
	if tn != nil {
		tn.closing = true
		tn.stop()
	}
	s.tunnels.mu.Unlock()
	s.refresh()
}
