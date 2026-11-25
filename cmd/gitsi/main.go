// Command gitsi bridges two machines: a hub that serves and an edge that only consumes.
package main

// The executable's icon: rsrc_windows_<arch>.syso, made from internal/desktop's drawing (main_test.go checks they are current).
//go:generate go run icon_gen.go

import (
	"context"
	"os"
	"os/signal"

	"github.com/ShizukaJiku/gitsi/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(app.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
