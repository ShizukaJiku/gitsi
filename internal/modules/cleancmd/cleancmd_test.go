package cleancmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

func env(t *testing.T, role config.Role) (*module.Env, *bytes.Buffer, string) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "AppData", "Local")) // os.UserCacheDir on Linux (race detector)
	c := config.Default()
	c.Role = role
	var out bytes.Buffer
	return &module.Env{Stdout: &out, Stderr: &out, Config: c}, &out, home
}

func old(t *testing.T, p string, days int) {
	ts := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	os.Chtimes(p, ts, ts)
}

func TestCleanOnHubDryRunThenForReal(t *testing.T) {
	e, out, home := env(t, config.RoleHub)
	jd := filepath.Join(home, "gitsi", "jobs", "aaaaaaaa")
	os.MkdirAll(jd, 0o755)
	os.WriteFile(filepath.Join(jd, "exit"), []byte("0"), 0o644)
	old(t, filepath.Join(jd, "exit"), 20)

	if err := run(context.Background(), e, []string{"-dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Would remove: job") || !strings.Contains(out.String(), "Without -dry-run they are removed") {
		t.Fatalf("%s", out)
	}
	if _, err := os.Stat(jd); err != nil {
		t.Fatal("-dry-run removes nothing")
	}
	out.Reset()
	if err := run(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jd); err == nil || !strings.Contains(out.String(), "Removed: job") {
		t.Fatalf("it must be removed: %s", out)
	}
	out.Reset()
	run(context.Background(), e, nil)
	if !strings.Contains(out.String(), "Nothing to clean") {
		t.Fatal(out.String())
	}
}

// The hub also has 'take -clip' (from the inbox): its old copies are cleaned up just like on the edge.
func TestCleanOnHubAlsoRemovesClipCopies(t *testing.T) {
	e, out, home := env(t, config.RoleHub)
	clip := filepath.Join(home, "AppData", "Local", "gitsi", "clipboard", "3333333333333333")
	os.MkdirAll(clip, 0o700)
	old(t, clip, 10)
	if err := run(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clip); err == nil || !strings.Contains(out.String(), "Removed: clipboard") {
		t.Fatalf("the old -clip copy is removed on the hub: %s", out)
	}
}

func TestCleanOnEdgeRemovesStalePartsAndClipCopies(t *testing.T) {
	e, out, home := env(t, config.RoleEdge)
	recv := filepath.Join(home, "Downloads", "gitsi")
	os.MkdirAll(recv, 0o755)
	part := filepath.Join(recv, ".gitsi-1111111111111111.part")
	os.WriteFile(part, []byte("x"), 0o644)
	old(t, part, 10)
	clip := filepath.Join(home, "AppData", "Local", "gitsi", "clipboard", "2222222222222222")
	os.MkdirAll(clip, 0o700)
	old(t, clip, 10)
	e.JSON = true
	if err := run(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"what": "partial"`) || !strings.Contains(out.String(), `"what": "clipboard"`) {
		t.Fatalf("%s", out)
	}
	if _, err := os.Stat(part); err == nil {
		t.Fatal("the old partial is removed")
	}
	if err := run(context.Background(), e, []string{"extra"}); err == nil {
		t.Fatal("no arguments allowed")
	}
}
