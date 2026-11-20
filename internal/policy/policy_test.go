package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
)

func TestCheckIsOffByDefaultAndPerFeature(t *testing.T) {
	var p config.Policy // everything off
	for _, f := range []Feature{Send, Take, Services, Exec} {
		err := Check(p, f)
		var fe *fault.Error
		if !errors.As(err, &fe) || fe.Kind != fault.FeatureOff || !strings.Contains(fe.Hint, "policy set "+string(f)+" on") {
			t.Errorf("%s must be off and say how to enable it: %v", f, err)
		}
	}
	// each switch opens only its own feature
	cases := map[Feature]config.Policy{
		Send: {AllowSend: true}, Take: {AllowTake: true}, Services: {AllowServices: true}, Exec: {AllowExec: true},
	}
	for on, pol := range cases {
		for _, f := range []Feature{Send, Take, Services, Exec} {
			if got := Check(pol, f) == nil; got != (f == on) {
				t.Errorf("with only %s on, Check(%s) allowed = %v", on, f, got)
			}
		}
	}
	// fail closed: an unknown feature is never allowed
	if Check(config.Policy{AllowSend: true, AllowTake: true, AllowServices: true, AllowExec: true}, Feature("other")) == nil {
		t.Fatal("an unknown feature must be rejected")
	}
}

func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("USERPROFILE", h)
	t.Setenv("HOME", h)
	return filepath.Join(h, ".gitsi", "audit.log")
}

func TestAuditOffWritesNothing(t *testing.T) {
	log := home(t)
	if err := Audit(config.Policy{}, "send", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("with auditing off nothing is written")
	}
}

func TestAuditAppendsOneLinePerFact(t *testing.T) {
	log := home(t)
	p := config.Policy{Audit: true}
	if err := Audit(p, "send", "name=a.txt size=3"); err != nil {
		t.Fatal(err)
	}
	if err := Audit(p, "take", "name=b.txt"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines: %q", b)
	}
	f := strings.Split(lines[0], "\t")
	if len(f) != 3 || f[1] != "send" || f[2] != "name=a.txt size=3" {
		t.Fatalf("format: %q", lines[0])
	}
}

func TestAuditCannotForgeEntries(t *testing.T) {
	log := home(t)
	if err := Audit(config.Policy{Audit: true}, "send", "name=x\n2026-01-01T00:00:00Z\tshare\tforged"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Fatalf("a value with a newline must not create another entry: %q", b)
	}
	if n := strings.Count(string(b), "\t"); n != 2 {
		t.Fatalf("a value with tabs must not create new fields: %q", b)
	}
}

// audit.log does not grow without bound: past AuditMaxSize it is kept as audit.log.1 and a new one starts.
func TestAuditRotatesWhenTooBig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	old := AuditMaxSize
	AuditMaxSize = 200
	defer func() { AuditMaxSize = old }()
	p := config.Policy{Audit: true}
	for i := 0; i < 20; i++ {
		if err := Audit(p, "send", "a fairly long line of audit record"); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(home, ".gitsi", "audit.log")
	cur, err1 := os.Stat(log)
	prev, err2 := os.Stat(log + ".1")
	if err1 != nil || err2 != nil {
		t.Fatalf("it must have rotated: %v %v", err1, err2)
	}
	if cur.Size() > 2*AuditMaxSize || prev.Size() > 2*AuditMaxSize {
		t.Fatalf("neither may go far past the limit: %d %d", cur.Size(), prev.Size())
	}
}

func TestFieldAndSetCoverEveryFeature(t *testing.T) {
	cfg := config.Default()
	for _, f := range []Feature{Send, Take, Services, Exec} {
		if err := Set(cfg, f, true); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		v, err := Field(&cfg.Policy, f)
		if err != nil || !*v {
			t.Fatalf("%s: %v", f, err)
		}
		if Effects[f] == "" {
			t.Errorf("%s has no description of what it enables", f)
		}
	}
	if _, err := Field(&cfg.Policy, Feature("delete")); err == nil {
		t.Fatal("an unknown feature is an error")
	}
	saved, err := config.Load()
	if err != nil || !saved.Policy.AllowExec {
		t.Fatalf("Set saves the configuration: %v %+v", err, saved.Policy)
	}
	b, _ := os.ReadFile(filepath.Join(os.Getenv("USERPROFILE"), ".gitsi", "audit.log"))
	if !strings.Contains(string(b), "policy.set\texec=on") {
		t.Fatalf("Set leaves a record: %q", b)
	}
}
