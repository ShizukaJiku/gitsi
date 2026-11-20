// Command gitsi bridges two machines: a hub that serves and an edge that only consumes.
package main

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
