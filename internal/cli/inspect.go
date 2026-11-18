package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Ri4dH/failtrace/internal/github"
	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/report"
	"github.com/Ri4dH/failtrace/internal/runurl"
)

const maxExcerptLines = 500

type inspectOptions struct {
	json  bool
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
			"  failtrace inspect --json <run-url> | jq .failed_jobs[0].failure",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspect(cmd.Context(), cmd.OutOrStdout(), args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "print the report as JSON")
	cmd.Flags().IntVarP(&opts.lines, "lines", "n", inspect.DefaultExcerptLines, "log lines to show from the failed step")
	cmd.Flags().Int64Var(&opts.jobID, "job", 0, "inspect only this job ID (default: all failed jobs)")
	return cmd
}

func runInspect(ctx context.Context, out io.Writer, rawURL string, opts inspectOptions) error {
	if opts.lines < 1 || opts.lines > maxExcerptLines {
		return fmt.Errorf("--lines must be between 1 and %d", maxExcerptLines)
	}
	ref, err := runurl.Parse(rawURL)
	if err != nil {
		return err
	}
	if opts.jobID < 0 {
		return fmt.Errorf("--job must be a positive job ID")
	}
	if opts.jobID != 0 {
		ref.JobID = opts.jobID
	}
	token, err := github.ResolveToken(ctx, ref.Host)
	if err != nil {
		return err
	}
	client := github.NewClient(ref.APIBase(), token)

	rep, err := inspect.Run(ctx, client, ref, inspect.Options{ExcerptLines: opts.lines})
	if err != nil {
		return err
	}
	if opts.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	return report.WriteText(out, rep, report.TextOptions{Color: colorEnabled(out)})
}
