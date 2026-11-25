package watchcmd

import (
	"context"
	"fmt"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/policy"
)

// settingsMenu holds the settings people change most, read from the configuration as it is now. Each change goes through gitsi
// (`config set`, `policy set`, `watch -autostart`), as at a terminal.
func (s *session) settingsMenu(ctx context.Context) desktop.MenuItem {
	cfg := s.liveConfig()
	var sub []desktop.MenuItem
	if s.clipOn {
		sub = append(sub, desktop.MenuItem{Label: "Send every copy", Checked: s.sharer.Auto(), Action: func() { s.toggleAuto() }})
	}
	if !s.isHub() {
		on := cfg.Watch.AutoUpdate
		sub = append(sub, desktop.MenuItem{Label: "Update automatically", Checked: on, Action: func() {
			s.act(ctx, "", "change the setting", "Update automatically: "+onOff(!on), "config", "set", "watch.auto_update", onOff(!on))
			s.refresh()
		}})
	} else if _, on, err := autostart.Get(); err == nil {
		sub = append(sub, desktop.MenuItem{Label: "Start when I log on", Checked: on, Action: func() {
			s.act(ctx, "", "change the setting", "Start when I log on: "+onOff(!on), "watch", "-autostart", onOff(!on))
			s.refresh()
		}})
	}
	if s.clipOn {
		sub = append(sub, desktop.MenuItem{Label: "Hotkey\t" + desktop.HotkeyLabel(cfg.Clip.HotkeyOrDefault()), Disabled: true})
	}
	sub = append(sub, desktop.MenuItem{}, s.featuresMenu(ctx), desktop.MenuItem{},
		desktop.MenuItem{Label: "Open the configuration file", Icon: desktop.GlyphFile, Action: func() {
			p, err := config.Path()
			if err == nil {
				err = openFile(p)
			}
			if err != nil {
				s.notify("gitsi", "Could not open the configuration: "+err.Error(), true)
			}
		}})
	return desktop.MenuItem{Label: "Settings", Icon: desktop.GlyphSettings, Sub: sub}
}

// featuresMenu lists the optional features (policies) with a check mark. Turning one on explains it and asks; turning it off
// does not ask. On the hub only the shared clipboard has a policy that matters.
func (s *session) featuresMenu(ctx context.Context) desktop.MenuItem {
	list := policy.All
	if s.isHub() {
		list = []policy.Feature{policy.Clip}
	}
	var sub []desktop.MenuItem
	on := 0
	for _, f := range list {
		enabled := s.featureOn(f)
		if enabled {
			on++
		}
		sub = append(sub, desktop.MenuItem{Label: featureTitles[f] + "\t" + string(f), Checked: enabled, Action: func() { s.toggleFeature(ctx, f, enabled) }})
	}
	sub = append(sub, desktop.MenuItem{}, desktop.MenuItem{Label: "Each change is recorded in audit.log", Disabled: true})
	return desktop.MenuItem{Label: fmt.Sprintf("Features\t%d of %d on", on, len(list)), Icon: desktop.GlyphLock, Sub: sub}
}

func (s *session) toggleFeature(ctx context.Context, f policy.Feature, enabled bool) {
	if !enabled {
		s.enableFeature(ctx, f)
		return
	}
	if s.act(ctx, "", "turn it off", featureTitles[f]+": off", "policy", "set", string(f), "off") && f == policy.Clip {
		s.notify("gitsi", "The shared clipboard stops when watch restarts: quit it from this menu and start it again.", false)
	}
	s.refresh()
}

func (s *session) toggleAuto() {
	on := !s.sharer.Auto()
	s.sharer.SetAuto(on)
	if err := saveAuto(on); err != nil {
		s.printf("could not save clip.auto: %v", err)
	}
	s.printf("send every copy: %s", map[bool]string{true: "on", false: "off (only the hotkey sends)"}[on])
	s.refresh()
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
