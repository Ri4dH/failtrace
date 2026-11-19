package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ri4dH/failtrace/internal/docker"
	"github.com/Ri4dH/failtrace/internal/github"
	"github.com/Ri4dH/failtrace/internal/plan"
	"github.com/Ri4dH/failtrace/internal/report"
	"github.com/Ri4dH/failtrace/internal/repro"
	"github.com/Ri4dH/failtrace/internal/toolchain"
)

type reproOptions struct {
	json       bool
	jobID      int64
	keep       bool
	verbose    bool
	secrets    []string
	toolchains []string
	platform   string
	timeout    time.Duration
	workdir    string
	runs       int
	shell      bool
	report     string
}

var (
	envNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	toolSpecRe  = regexp.MustCompile(`^([a-z]+)=([A-Za-z0-9.+_~^*-]+)$`)
	platformsOK = map[string]bool{"": true, "linux/amd64": true, "linux/arm64": true}
)

func newReproCmd() *cobra.Command {
	opts := reproOptions{}
	cmd := &cobra.Command{
		Use:   "repro <run-url>",
		Short: "Reproduce a failed job locally in Docker",
		Long: "Builds the reproduction plan, then runs it: an environment image with the\n" +
			"toolchains CI used, the exact commit CI tested, service containers, and the\n" +
			"steps up to the one that failed. Then it compares the local result with CI's.\n\n" +
			"The steps run code from the repository (and from the pull request, if any)\n" +
			"inside a container. Only secrets you pass with --secret are made available.",
		Example: "  failtrace repro https://github.com/owner/repo/actions/runs/123456789\n" +
			"  failtrace repro --job 987654321 --keep <run-url>\n" +
			"  API_TOKEN=... failtrace repro --secret API_TOKEN <run-url>\n" +
			"  failtrace repro --toolchain node=20.11.1 <run-url>\n" +
			"  failtrace repro --runs 10 <run-url>              # is it flaky?\n" +
			"  failtrace repro --shell <run-url>                # debug inside the container\n" +
			"  failtrace repro --report repro.md <run-url>      # Markdown for an issue or PR",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRepro(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.json, "json", false, "print the plan and result as JSON (progress goes to stderr)")
	f.Int64Var(&opts.jobID, "job", 0, "reproduce this job ID (default: the first failed job)")
	f.BoolVar(&opts.keep, "keep", false, "keep the container and workspace for debugging")
	f.BoolVarP(&opts.verbose, "verbose", "v", false, "stream the output of every step and the image build")
	f.StringArrayVar(&opts.secrets, "secret", nil, "pass the secret NAME, read from your environment (repeatable)")
	f.StringArrayVar(&opts.toolchains, "toolchain", nil, "install tool=version, overriding the plan (repeatable)")
	f.StringVar(&opts.platform, "platform", "", "container platform: linux/amd64 or linux/arm64 (default: what CI used)")
	f.DurationVar(&opts.timeout, "timeout", 45*time.Minute, "give up after this long")
	f.StringVar(&opts.workdir, "workdir", "", "host directory for workspaces (default: your user cache directory)")
	f.IntVar(&opts.runs, "runs", 1, fmt.Sprintf("run the failing step this many times to detect flakiness (max %d)", repro.MaxRuns))
	f.BoolVar(&opts.shell, "shell", false, "open a shell in the container at the failing step when the run finishes")
	f.StringVar(&opts.report, "report", "", "also write a Markdown report to this file (- for stdout instead of the terminal summary)")
	return cmd
}

func (o reproOptions) parse() (secrets, tools map[string]string, err error) {
	if !platformsOK[o.platform] {
		return nil, nil, fmt.Errorf("--platform must be linux/amd64 or linux/arm64")
	}
	if o.timeout <= 0 {
		return nil, nil, fmt.Errorf("--timeout must be positive")
	}
	if o.runs < 1 || o.runs > repro.MaxRuns {
		return nil, nil, fmt.Errorf("--runs must be between 1 and %d", repro.MaxRuns)
	}
	if o.shell && (o.json || o.report == "-") {
		return nil, nil, fmt.Errorf("--shell cannot be combined with --json or --report - (both use stdout)")
	}
	if o.json && o.report == "-" {
		return nil, nil, fmt.Errorf("--json and --report - both write to stdout; pick one")
	}
	if err := checkReportPath(o.report); err != nil {
		return nil, nil, err
	}
	secrets = map[string]string{}
	for _, name := range o.secrets {
		if !envNameRe.MatchString(name) {
			return nil, nil, fmt.Errorf("--secret %q: expected a variable name; the value is read from your environment so it never appears in your shell history", name)
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			return nil, nil, fmt.Errorf("--secret %s: %s is not set in your environment", name, name)
		}
		secrets[name] = v
	}
	tools = map[string]string{}
	for _, t := range o.toolchains {
		m := toolSpecRe.FindStringSubmatch(t)
		if m == nil {
			return nil, nil, fmt.Errorf("--toolchain %q: expected tool=version, e.g. node=20.11.1", t)
		}
		tools[m[1]] = m[2]
	}
	return secrets, tools, nil
}

func runRepro(ctx context.Context, out, errOut io.Writer, rawURL string, opts reproOptions) error {
	secrets, tools, err := opts.parse()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	f, err := fetchRun(ctx, rawURL, opts.jobID, 25)
	if err != nil {
		return err
	}
	rep := f.report
	if len(rep.Jobs) == 0 {
		return fmt.Errorf("no failed jobs in this run (%s), so there is nothing to reproduce", rep.Run.Conclusion)
	}
	job := rep.Jobs[0]
	p, err := plan.Build(ctx, plan.Input{Owner: f.ref.Owner, Repo: f.ref.Repo, Run: rep.Run, Job: job}, f.client)
	if err != nil {
		return fmt.Errorf("plan job %q: %w", job.Name, err)
	}

	progress := out
	if opts.json {
		progress = errOut
	}
	fmt.Fprintf(progress, "Reproducing %s · %s · plan %s\n", p.Repository, p.Job.Name, p.Verdict)
	token, _ := github.ResolveToken(ctx, f.ref.Host)
	res, err := repro.Run(ctx,
		repro.Deps{Engine: &docker.CLI{}, Git: repro.HostGit{}, Resolver: toolchain.NewResolver()},
		p, job.Failure,
		repro.Options{
			Host: f.ref.Host, Token: token, Secrets: secrets, Toolchains: tools, Platform: opts.platform,
			WorkDir: opts.workdir, Keep: opts.keep, Verbose: opts.verbose, Out: progress,
			Runs: opts.runs, Shell: opts.shell,
			ShellIO: repro.ShellIO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, TTY: isTerminal(os.Stdin) && isTerminal(os.Stdout)},
		})
	if errors.Is(err, repro.ErrShell) && res != nil {
		fmt.Fprintf(errOut, "failtrace: %v\n", err) // the reproduction itself finished; still report it
		err = nil
	}
	if err != nil {
		if errors.Is(err, repro.ErrBlocked) {
			return fmt.Errorf("%w (run `failtrace plan %s` for details)", err, rawURL)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("gave up after %s (raise it with --timeout)", opts.timeout)
		}
		return err
	}
	if err := writeReport(out, opts.report, p, res); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(out, struct {
			Plan   *plan.Plan    `json:"plan"`
			Result *repro.Result `json:"result"`
		}{p, res})
	}
	if opts.report == "-" {
		return nil
	}
	if err := report.WriteRepro(out, res, report.TextOptions{Color: colorEnabled(out)}); err != nil {
		return err
	}
	if opts.report != "" {
		fmt.Fprintf(out, "\n  Markdown report written to %s\n", opts.report)
	}
	if others := len(rep.Jobs) - 1; others > 0 {
		var ids []string
		for _, j := range rep.Jobs[1:] {
			ids = append(ids, fmt.Sprint(j.ID))
		}
		fmt.Fprintf(out, "\n  %d other failed job(s) in this run; reproduce one with --job: %s\n", others, strings.Join(ids, ", "))
	}
	return nil
}

// checkReportPath fails early, before a long reproduction, if the report
// cannot be written.
func checkReportPath(p string) error {
	if p == "" || p == "-" {
		return nil
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return fmt.Errorf("--report %s is a directory", p)
	}
	dir := filepath.Dir(filepath.Clean(p))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("--report %s: directory %s does not exist", p, dir)
	}
	return nil
}

func writeReport(stdout io.Writer, p string, pl *plan.Plan, res *repro.Result) error {
	switch p {
	case "":
		return nil
	case "-":
		return report.WriteMarkdown(stdout, pl, res)
	}
	f, err := os.Create(filepath.Clean(p))
	if err != nil {
		return fmt.Errorf("--report: %w", err)
	}
	if err := report.WriteMarkdown(f, pl, res); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
