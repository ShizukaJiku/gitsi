package watchcmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

func TestListeningCountsOnlyEdgesWithARecentPing(t *testing.T) {
	cfg := config.Default()
	cfg.Role, cfg.Home = config.RoleHub, t.TempDir()
	env := &module.Env{Config: cfg}
	if got, err := Listening(context.Background(), env); err != nil || got != "no edge listening (on the edge: gitsi watch)" {
		t.Fatalf("%q %v", got, err)
	}
	dir := stateDir(cfg)
	os.MkdirAll(dir, 0o700)
	put := func(user string, ping time.Time) {
		b, _ := json.Marshal(watch.EdgeState{Edge: "WORKSTATION", User: user, Connected: true, LastPing: ping})
		os.WriteFile(filepath.Join(dir, user+"@WORKSTATION.json"), b, 0o600)
	}
	put("ana", time.Now())
	put("bea", time.Now().Add(-time.Hour)) // the session died without closing: it does not count
	if got, _ := Listening(context.Background(), env); got != "1 edge listening (gitsi edges)" {
		t.Fatalf("%q", got)
	}
}
