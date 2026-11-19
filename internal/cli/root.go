// Package cli wires FailTrace's commands together.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCmd builds the top-level command.
func NewRootCmd(info BuildInfo) *cobra.Command {
	info = info.resolve()
	root := &cobra.Command{
		Use:           "failtrace",
		Short:         "Stop guessing why CI failed. Reproduce it locally.",
		Long:          "FailTrace takes a failed GitHub Actions run URL, finds the step that failed, and explains why.",
		Version:       info.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("failtrace {{.Version}}\n")
	root.AddCommand(newInspectCmd(), newPlanCmd(), newReproCmd(), newVersionCmd(info))
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, info BuildInfo) int {
	return exitCode(NewRootCmd(info).ExecuteContext(ctx), os.Stderr)
}

// exitStatus ends a command that succeeded with a non-zero exit code, such
// as repro's verdicts. It prints nothing.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// exitCode turns a command's error into the process exit code: 0 on
// success, the code of an exitStatus, and 1 (with the error printed) otherwise.
func exitCode(err error, stderr io.Writer) int {
	var status exitStatus
	switch {
	case err == nil:
		return 0
	case errors.As(err, &status):
		return int(status)
	}
	fmt.Fprintln(stderr, "failtrace:", err)
	return 1
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
