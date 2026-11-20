package layout

import (
	"errors"
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
