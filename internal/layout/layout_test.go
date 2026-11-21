package layout

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
)

func TestForHubDefaultsUnderTheUserProfile(t *testing.T) {
	l := ForHub(&config.Config{}, `C:\Users\user`)
	want := Layout{
		Home: "C:/Users/user/gitsi", Repos: "C:/Users/user/gitsi/repos", Binary: "C:/Users/user/gitsi/gitsi.exe",
		Mailbox: "C:/Users/user/gitsi/mailbox", Jobs: "C:/Users/user/gitsi/jobs",
	}
	if l != want {
		t.Fatalf("%+v", l)
	}
}

// A hub that already has its repos elsewhere keeps them there (explicit repos_dir) without moving anything else.
func TestForHubKeepsAnExistingReposDir(t *testing.T) {
	l := ForHub(&config.Config{ReposDir: "C:/Users/user/repos"}, `C:\Users\user`)
	if l.Repos != "C:/Users/user/repos" || l.Binary != "C:/Users/user/gitsi/gitsi.exe" || l.Mailbox != "C:/Users/user/gitsi/mailbox" {
		t.Fatalf("%+v", l)
	}
}

func TestForHubHomeOverridesEverythingUnderIt(t *testing.T) {
	l := ForHub(&config.Config{Home: `D:\data\gitsi`}, `C:\Users\x`)
	if l.Home != "D:/data/gitsi" || l.Repos != "D:/data/gitsi/repos" || l.Binary != "D:/data/gitsi/gitsi.exe" || l.Jobs != "D:/data/gitsi/jobs" {
		t.Fatalf("%+v", l)
	}
	// and repos_dir may live elsewhere
	l = ForHub(&config.Config{Home: "D:/g", ReposDir: "E:/repos"}, "C:/u")
	if l.Repos != "E:/repos" || l.Mailbox != "D:/g/mailbox" {
		t.Fatalf("%+v", l)
	}
}

func TestInfoRoundTrip(t *testing.T) {
	l := ForHub(&config.Config{ReposDir: "C:/r"}, "C:/Users/u")
	got, err := FromInfo(l.Info())
	if err != nil || got != l {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFromInfoDistinguishesNotAnnouncedFromInvalid(t *testing.T) {
	if _, err := FromInfo(nil); !errors.Is(err, ErrNotAnnounced) {
		t.Fatalf("a greeting without a layout: %v", err)
	}
	if _, err := FromInfo(map[string]string{"version": "0.1"}); !errors.Is(err, ErrNotAnnounced) {
		t.Fatalf("%v", err)
	}
	// incomplete
	l := ForHub(&config.Config{}, "C:/u").Info()
	delete(l, keyJobs)
	if _, err := FromInfo(l); err == nil || errors.Is(err, ErrNotAnnounced) || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("%v", err)
	}
}

// A profile folder that safe.Path rejects (an accented letter) is reached through its 8.3 short name; one that is valid is never
// shortened; and without a safe short name the layout stays as is, and Check reports it.
func TestForHubUsesTheShortNameOfAnUnsafeProfile(t *testing.T) {
	asked := 0
	old := ShortPath
	t.Cleanup(func() { ShortPath = old })

	ShortPath = func(p string) string { asked++; return `C:\Users\JOSPRE~1` }
	l := ForHub(&config.Config{}, `C:\Users\José Pérez`)
	if l.Home != "C:/Users/JOSPRE~1/gitsi" || l.Jobs != "C:/Users/JOSPRE~1/gitsi/jobs" || l.Check() != nil {
		t.Fatalf("%+v", l)
	}
	if _, err := FromInfo(l.Info()); err != nil {
		t.Fatalf("an edge must accept it: %v", err)
	}

	asked = 0
	if l := ForHub(&config.Config{}, `C:\Users\user`); asked != 0 || l.Home != "C:/Users/user/gitsi" {
		t.Fatalf("a valid profile must not be shortened: asked=%d %+v", asked, l)
	}
	// an explicit home is the owner's choice: never replaced
	if l := ForHub(&config.Config{Home: "D:/g"}, `C:\Users\José Pérez`); asked != 0 || l.Home != "D:/g" {
		t.Fatalf("asked=%d %+v", asked, l)
	}

	ShortPath = func(string) string { return "" } // 8.3 names disabled on the volume
	l = ForHub(&config.Config{}, `C:\Users\José Pérez`)
	if l.Home != "C:/Users/José Pérez/gitsi" || l.Check() == nil {
		t.Fatalf("without a short name it must stay as is and fail Check: %+v", l)
	}
	// a short name that is still unsafe (8.3 names keep apostrophes) is not used either
	ShortPath = func(string) string { return `C:\Users\O'BRIE~1` }
	if l := ForHub(&config.Config{}, `C:\Users\O'Brien`); l.Home != "C:/Users/O'Brien/gitsi" {
		t.Fatalf("%+v", l)
	}
}

// The real GetShortPathNameW over temporary folders: an accented profile is fixed by its short name; an apostrophe is not (8.3
// names keep it: O'BRIE~1), so the layout stays invalid and Check reports it.
func TestShortPathOnARealFolder(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("8.3 names are Windows only")
	}
	base := t.TempDir()
	mk := func(name string) string {
		t.Helper()
		d := filepath.Join(base, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	accent, apostrophe := mk("José Pérez"), mk("O'Brien")
	s := shortPath(accent)
	if s == "" || strings.EqualFold(filepath.Base(s), filepath.Base(accent)) {
		t.Skipf("the volume of %s has no 8.3 names", base)
	}
	if fi, err := os.Stat(s); err != nil || !fi.IsDir() {
		t.Fatalf("the short name %q must point to the same folder: %v", s, err)
	}
	l := ForHub(&config.Config{}, accent)
	if err := l.Check(); err != nil || !strings.HasPrefix(l.Home, filepath.ToSlash(s)) {
		t.Fatalf("an accented profile must be reached through its short name %q: %+v %v", s, l, err)
	}

	if l := ForHub(&config.Config{}, apostrophe); l.Check() == nil {
		t.Fatalf("an apostrophe survives in 8.3 names: the layout must stay invalid: %+v", l)
	}
	if shortPath(filepath.Join(base, "missing")) != "" {
		t.Fatal("a path that does not exist has no short name")
	}
}

// The hub's paths end up in PowerShell commands: what arrives is validated, not trusted.
func TestFromInfoRejectsDangerousPaths(t *testing.T) {
	for _, bad := range []string{"C:/x'; calc; '", "C:/a/../b", "C:/a b\"c", "C:/x\nC:/y", "C:/a;b", "`calc`"} {
		info := ForHub(&config.Config{}, "C:/u").Info()
		info[keyRepos] = bad
		if _, err := FromInfo(info); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
