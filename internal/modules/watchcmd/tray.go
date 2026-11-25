package watchcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/clip"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/procx"
)

// runHub shares the hub's clipboard: the hotkey (or every copy) stores it in the slot the edges' sessions deliver from, and what
// an edge stores there is written to this clipboard. It opens no connection.
func (s *session) runHub(ctx context.Context) error {
	if err := policy.Check(s.env.Config.Policy, policy.Clip); err != nil {
		return err // on the hub, watch only shares the clipboard: without the policy there is nothing to do
	}
	slot := hubSlot(s.env.Config)
	s.sharer.Send = func(_ context.Context, it clip.Item, data []byte) error {
		it.Origin, it.Time = clip.OriginHub, time.Now()
		return slot.Put(it, data)
	}
	if err := s.startUI(ctx); err != nil {
		return err
	}
	s.printf("sharing the hub's clipboard with the edges that run gitsi watch. %s", s.quitHint())
	s.audit("watch.clip", "hub")
	return pollSlot(ctx, slot, s.sharer, time.Now(), slotPoll)
}

// slotPoll is how often the hub's watch checks the slot (a copy from an edge should land almost at once).
var slotPoll = 100 * time.Millisecond

// pollSlot writes to the clipboard each new item an edge stores in the slot after start, until ctx ends.
func pollSlot(ctx context.Context, slot *clip.Slot, sh *clip.Sharer, start time.Time, every time.Duration) error {
	seen := ""
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		cur, ok := slot.Current(time.Now())
		if !ok || cur.ID == seen {
			continue
		}
		seen = cur.ID
		if cur.Origin == clip.OriginHub || !cur.Time.After(start) {
			continue // the hub's own copy, or one stored before this watch started
		}
		data, err := slot.Data(cur)
		if err != nil {
			continue // replaced in the meantime: the newer one comes next
		}
		_ = sh.Receive(ctx, cur, data)
	}
}

// detach starts 'gitsi watch -tray' as a process of its own that stays in the tray, and returns: the terminal is free. It waits
// a moment so a child that stops right away (configuration, policy, already running) is reported here instead of vanishing.
var detach = func(env *module.Env) error {
	if release, err := lock(); errors.Is(err, desktop.ErrRunning) {
		return errors.New("watch is already running on this machine (in the tray or in another terminal)")
	} else if err == nil {
		release()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p, err := logPath()
	if err != nil {
		return err
	}
	cmd := procx.Detached(exec.Command(exe, "watch", "-tray"))
	cmd.Env = append(os.Environ(), childEnv+"=1")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start watch in the tray: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
		return fmt.Errorf("watch stopped as soon as it started. Its last lines (%s):\n%s", p, tail(p, 5))
	case <-time.After(2 * time.Second):
	}
	fmt.Fprintf(env.Stdout, "watch is running in the tray (PID %d): quit it from the icon's menu. Its output goes to %s\n", cmd.Process.Pid, p)
	return nil
}

// autostarter is the logon entry (a fake in tests).
type autostarter interface {
	Set(command string) error
	Remove() error
	Get() (string, bool, error)
}

var autostart autostarter = desktop.Autostart{Key: desktop.RunKey, Name: "gitsi-watch"}

// autostartCmd adds or removes the logon entry that starts 'gitsi watch -tray' on the hub. The edge never gets one: nothing
// starts on its own there (docs/PHILOSOPHY.md).
func autostartCmd(env *module.Env, v string) error {
	if env.Config.Role != config.RoleHub {
		return errors.New("-autostart is for the hub: on the edge nothing starts on its own. Start it by hand: gitsi watch -tray")
	}
	switch v {
	case "on":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if strings.ContainsAny(exe, `"`) {
			return fmt.Errorf("the path of gitsi cannot go in a logon entry: %s", exe)
		}
		cmd := `"` + exe + `" watch -tray`
		if err := autostart.Set(cmd); err != nil {
			return fmt.Errorf("could not add the logon entry: %w", err)
		}
		_ = policy.Audit(env.Config.Policy, "watch.autostart", "on")
		fmt.Fprintf(env.Stdout, "When you log on, Windows starts: %s\n", cmd)
		if policy.Check(env.Config.Policy, policy.Clip) != nil {
			fmt.Fprintln(env.Stdout, "The clip policy is off, so it would stop at once: gitsi policy set clip on")
		}
		return nil
	case "off":
		if err := autostart.Remove(); err != nil {
			return fmt.Errorf("could not remove the logon entry: %w", err)
		}
		_ = policy.Audit(env.Config.Policy, "watch.autostart", "off")
		fmt.Fprintln(env.Stdout, "watch no longer starts when you log on.")
		return nil
	}
	return fmt.Errorf("-autostart takes on or off, not %q", v)
}
