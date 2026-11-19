package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// BuildInfo identifies a build. Release builds get these values from
// -ldflags; `go install` builds fall back to the module's build info.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// resolve fills in what -ldflags did not set from the Go build info.
func (b BuildInfo) resolve() BuildInfo {
	if b.Version == "" {
		b.Version = "dev"
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	if b.Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		b.Version = info.Main.Version // go install github.com/Ri4dH/failtrace/cmd/failtrace@v0.1.0
	}
	for _, s := range info.Settings {
		switch {
		case s.Key == "vcs.revision" && b.Commit == "":
			b.Commit = s.Value
		case s.Key == "vcs.time" && b.Date == "":
			b.Date = s.Value
		}
	}
	return b
}

func (b BuildInfo) String() string {
	s := b.Version
	if b.Commit != "" {
		commit := b.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		s += " (" + commit
		if b.Date != "" {
			s += ", " + b.Date
		}
		s += ")"
	}
	return s
}

func newVersionCmd(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the FailTrace version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "failtrace %s %s/%s %s\n", info, runtime.GOOS, runtime.GOARCH, runtime.Version())
		},
	}
}
