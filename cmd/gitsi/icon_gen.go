//go:build ignore

// icon_gen writes the executable's icon resources (rsrc_windows_<arch>.syso) next to main.go: go generate ./cmd/gitsi
package main

import (
	"fmt"
	"os"

	"github.com/ShizukaJiku/gitsi/internal/desktop"
)

func main() {
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := desktop.ResourceObject(arch)
		if err == nil {
			err = os.WriteFile("rsrc_windows_"+arch+".syso", b, 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "icon_gen:", err)
			os.Exit(1)
		}
	}
}
