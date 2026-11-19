package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/report"
)

const maxExcerptLines = 500

type inspectOptions struct {
	json  bool
	llm   bool
	lines int
	jobID int64
}

func newInspectCmd() *cobra.Command {
	opts := inspectOptions{}
	cmd := &cobra.Command{
		Use:   "inspect <run-url>",
		Short: "Explain why a GitHub Actions run failed",
		Long: "Fetches a workflow run, finds the failed jobs and steps, matches them to the\n" +
			"workflow file at the run's commit, and extracts the failure from the logs.",
		Example: "  failtrace inspect https://github.com/owner/repo/actions/runs/123456789\n" +
			"  failtrace inspect https://github.com/owner/repo/actions/runs/123456789/job/987654321\n" +
			"  failtrace inspect --json <run-url> | jq .failed_jobs[0].failure\n" +
			"  failtrace inspect --llm <run-url> | pbcopy       # paste into a coding assistant",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspect(cmd.Context(), cmd.OutOrStdout(), args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "print the report as JSON")
	cmd.Flags().BoolVar(&opts.llm, "llm", false, "print a compact plain-text summary for a language model's prompt")
	cmd.Flags().IntVarP(&opts.lines, "lines", "n", inspect.DefaultExcerptLines, "log lines to show from the failed step")
	cmd.Flags().Int64Var(&opts.jobID, "job", 0, "inspect only this job ID (default: all failed jobs)")
	return cmd
}

func runInspect(ctx context.Context, out io.Writer, rawURL string, opts inspectOptions) error {
	if opts.lines < 1 || opts.lines > maxExcerptLines {
		return fmt.Errorf("--lines must be between 1 and %d", maxExcerptLines)
	}
	if opts.json && opts.llm {
		return fmt.Errorf("--json and --llm are different output formats; pick one")
	}
	f, err := fetchRun(ctx, rawURL, opts.jobID, opts.lines)
	if err != nil {
		return err
	}
	switch {
	case opts.json:
		return writeJSON(out, f.report)
	case opts.llm:
		return report.WriteLLM(out, f.report)
	}
	return report.WriteText(out, f.report, report.TextOptions{Color: colorEnabled(out)})
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
