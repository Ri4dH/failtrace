// Command failtrace reproduces GitHub Actions failures locally.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/Ri4dH/failtrace/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, version)
	stop()
	os.Exit(code)
}
