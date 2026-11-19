// Command failtrace reproduces GitHub Actions failures locally.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/Ri4dH/failtrace/internal/cli"
)

// Set at build time, e.g. -ldflags "-X main.version=v0.1.0 -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, cli.BuildInfo{Version: version, Commit: commit, Date: date})
	stop()
	os.Exit(code)
}
