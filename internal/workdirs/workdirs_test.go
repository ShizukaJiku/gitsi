package workdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Run(m) }

func reset(t *testing.T) {
	t.Helper()
	p, _ := Path()
	_ = os.Remove(p)
}

// A folder is recorded once (the same path twice, in other case, is one entry), and a folder that disappears is forgotten.
func TestAddListAndForget(t *testing.T) {
	reset(t)
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []Dir{{"webapp", a}, {"gitsi", b}, {"webapp", a}} {
		if err := Add(d.Repo, d.Path); err != nil {
			t.Fatal(err)
		}
	}
	if err := Add("webapp", filepath.Join(a, ".", "")); err != nil {
		t.Fatal(err)
	}
	got, err := List()
	if err != nil || len(got) != 2 || got[0].Repo != "gitsi" || got[1].Repo != "webapp" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	if got, _ := List(); len(got) != 1 || got[0].Path != a {
		t.Fatalf("a missing folder is not listed: %+v", got)
	}
	if err := Add("", a); err == nil {
		t.Fatal("a folder needs its repo")
	}
}

// The list keeps the newest folders only.
func TestAddKeepsTheNewest(t *testing.T) {
	reset(t)
	root := t.TempDir()
	for i := 0; i < maxDirs+5; i++ {
		d := filepath.Join(root, fmt.Sprint(i))
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := Add(fmt.Sprintf("r%02d", i), d); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := List()
	if len(got) != maxDirs || got[0].Repo != "r05" {
		t.Fatalf("%d, first %+v", len(got), got[0])
	}
}

// A damaged file does not stop the commands that record folders: it is replaced.
func TestAddReplacesADamagedFile(t *testing.T) {
	reset(t)
	p, _ := Path()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil {
		t.Fatal("List says the file cannot be read")
	}
	if err := Add("webapp", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got, err := List(); err != nil || len(got) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}
