package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ri4dH/failtrace/internal/plan"
	"github.com/Ri4dH/failtrace/internal/report"
)

type planOptions struct {
	json  bool
	jobID int64
}

func newPlanCmd() *cobra.Command {
	opts := planOptions{}
	cmd := &cobra.Command{
		Use:   "plan <run-url>",
		Short: "Show how a failed job would be reproduced locally (dry run)",
		Long: "Builds a reproduction plan for a failed job: the container image, the commit CI\n" +
			"actually tested, toolchains with the versions CI resolved, environment, and the\n" +
			"steps to replay up to the one that failed. Nothing is executed.",
		Example: "  failtrace plan https://github.com/owner/repo/actions/runs/123456789\n" +
			"  failtrace plan --job 987654321 <run-url>\n" +
			"  failtrace plan --json <run-url> | jq .toolchains",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd.Context(), cmd.OutOrStdout(), args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "print the plan as JSON")
	cmd.Flags().Int64Var(&opts.jobID, "job", 0, "plan this job ID (default: the first failed job)")
	return cmd
}

// planOutput is the JSON shape of `failtrace plan --json`.
type planOutput struct {
	*plan.Plan
	OtherFailedJobs []plan.JobRef `json:"other_failed_jobs,omitempty"`
}

func runPlan(ctx context.Context, out io.Writer, rawURL string, opts planOptions) error {
	f, err := fetchRun(ctx, rawURL, opts.jobID, 1)
	if err != nil {
		return err
	}
	rep := f.report
	if len(rep.Jobs) == 0 {
		msg := fmt.Sprintf("no failed jobs in this run (%s), so there is nothing to plan", rep.Run.Conclusion)
		if len(rep.Warnings) > 0 {
			msg += ": " + strings.Join(rep.Warnings, "; ")
		}
		return errors.New(msg)
	}

	job := rep.Jobs[0]
	p, err := plan.Build(ctx, plan.Input{Owner: f.ref.Owner, Repo: f.ref.Repo, Run: rep.Run, Job: job}, f.client)
	if err != nil {
		return fmt.Errorf("plan job %q: %w", job.Name, err)
	}
	var others []plan.JobRef
	for _, j := range rep.Jobs[1:] {
		others = append(others, plan.JobRef{ID: j.ID, Name: j.Name, URL: j.URL, WorkflowJob: j.WorkflowJob, Matrix: j.Matrix})
	}

	if opts.json {
		return writeJSON(out, planOutput{Plan: p, OtherFailedJobs: others})
	}
	if err := report.WritePlan(out, p, report.TextOptions{Color: colorEnabled(out)}); err != nil {
		return err
	}
	if len(others) > 0 {
		fmt.Fprintf(out, "\n  %d other failed job(s) in this run; plan one with --job <id>:\n", len(others))
		for _, o := range others {
			fmt.Fprintf(out, "    %d  %s\n", o.ID, o.Name)
		}
	}
	return nil
}
