// Package cli wires FailTrace's commands together.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCmd builds the top-level command.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "failtrace",
		Short:         "Stop guessing why CI failed. Reproduce it locally.",
		Long:          "FailTrace takes a failed GitHub Actions run URL, finds the step that failed, and explains why.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInspectCmd(), newPlanCmd(), newReproCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, version string) int {
	if err := NewRootCmd(version).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "failtrace:", err)
		return 1
	}
	return 0
}

// colorEnabled reports whether w is a terminal and NO_COLOR is unset.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
