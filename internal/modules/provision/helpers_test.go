package provision

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/layout"
)

func TestPortOr22(t *testing.T) {
	if portOr22(0) != 22 || portOr22(2222) != 2222 {
		t.Fatal("0 = default port; any other value is kept")
	}
}

func TestEnsureHubDirsIsIdempotent(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	lay := layout.Layout{Home: root + "/gitsi", Repos: root + "/gitsi/repos", Mailbox: root + "/gitsi/mailbox", Jobs: root + "/gitsi/jobs"}
	for i := 0; i < 2; i++ { // the second time it must neither fail nor touch anything
		if err := EnsureHubDirs(lay); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{lay.Home, lay.Repos, lay.Mailbox, lay.Jobs} {
		if st, err := os.Stat(filepath.FromSlash(d)); err != nil || !st.IsDir() {
			t.Errorf("folder %s is missing: %v", d, err)
		}
	}
	// an impossible path (under a file) returns the error instead of ignoring it
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHubDirs(layout.Layout{Home: filepath.ToSlash(file) + "/inside"}); err == nil {
		t.Fatal("a folder cannot be created inside a file")
	}
}

func TestPrintLayoutOnePathPerLine(t *testing.T) {
	var b bytes.Buffer
	printLayout(&b, layout.Layout{Repos: "D:/r", Mailbox: "D:/m", Binary: "D:/g/gitsi.exe"})
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "D:/r") || !strings.Contains(lines[1], "D:/m") || !strings.Contains(lines[2], "D:/g/gitsi.exe") {
		t.Fatalf("%q", b.String())
	}
}

func TestDescribeAddsTheActionOfAFault(t *testing.T) {
	plain := errors.New("no network")
	if describe(plain) != "no network" {
		t.Fatal(describe(plain))
	}
	fe := fault.New(fault.NotConfigured, "the hub is missing", "gitsi setup edge")
	if got := describe(fe); !strings.Contains(got, "the hub is missing") || !strings.Contains(got, "-> gitsi setup edge") {
		t.Fatal(got)
	}
	if got := describe(fault.New(fault.Other, "message only", "")); strings.Contains(got, "->") {
		t.Fatalf("without an action it must add no arrow: %s", got)
	}
}
