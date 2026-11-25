package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/desktop"
)

// The icon resources linked into the executable are the ones the current drawing produces: after changing the icon, run
// go generate ./cmd/gitsi.
func TestIconResourcesAreCurrent(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		want, err := desktop.ResourceObject(arch)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile("rsrc_windows_" + arch + ".syso")
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("rsrc_windows_%s.syso is missing or old (%v): run go generate ./cmd/gitsi", arch, err)
		}
	}
}
