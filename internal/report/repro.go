package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/repro"
)

// WriteRepro renders the outcome of a reproduction.
func WriteRepro(w io.Writer, res *repro.Result, opts TextOptions) error {
	c := painter{on: opts.Color}
	var b strings.Builder

	head := map[repro.Verdict]string{
		repro.VerdictReproduced:    c.green("✓ Reproduced") + ": the step fails locally the same way it failed in CI",
		repro.VerdictDifferent:     c.yellow("≠ Different failure") + ": the step fails locally, but not the way it failed in CI",
		repro.VerdictNotReproduced: c.red("✗ Not reproduced") + ": the step that failed in CI passed locally",
		repro.VerdictDiverged:      c.yellow("⚠ Diverged") + ": an earlier step failed locally, so the failing step never ran",
	}[res.Verdict]
	fmt.Fprintf(&b, "\n%s\n", c.bold(head))
	for _, r := range res.Reasons {
		fmt.Fprintf(&b, "  • %s\n", r)
	}

	if res.Local != nil || res.CI != nil {
		fmt.Fprintf(&b, "\n  %-14s %s %s\n", "", c.dim(fmt.Sprintf("%-28s", "CI")), c.dim("Local"))
		ci, local := res.CI, res.Local
		row := func(label string, get func(*joblog.Failure) string) {
			fmt.Fprintf(&b, "  %-14s %-28s %s\n", label, orDash(get(ci)), orDash(get(local)))
		}
		row("Exit code", func(f *joblog.Failure) string {
			if f == nil || f.ExitCode == nil {
				return ""
			}
			return fmt.Sprint(*f.ExitCode)
		})
		row("Failure", func(f *joblog.Failure) string {
			if f == nil {
				return ""
			}
			return categoryLabels[f.Category]
		})
		if (ci != nil && len(ci.FailedTests) > 0) || (local != nil && len(local.FailedTests) > 0) {
			row("Failed tests", func(f *joblog.Failure) string {
				if f == nil || len(f.FailedTests) == 0 {
					return ""
				}
				return truncateList(f.FailedTests, 26)
			})
		}
	}

	counts := map[repro.StepStatus]int{}
	for _, s := range res.Steps {
		counts[s.Status]++
	}
	var parts []string
	for _, st := range []repro.StepStatus{repro.StepPassed, repro.StepFailed, repro.StepSkipped, repro.StepNotRun} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	fmt.Fprintf(&b, "\n  %-10s %s %s\n", "Steps", strings.Join(parts, " · "), c.dim(fmt.Sprintf("(%.0fs total)", res.Seconds)))
	fmt.Fprintf(&b, "  %-10s %s %s\n", "Image", res.Image, c.dim(res.Platform))

	if len(res.Differences) > 0 {
		fmt.Fprintf(&b, "\n  %s\n", c.bold("Differences from CI"))
		for _, d := range res.Differences {
			fmt.Fprintf(&b, "    %s %s\n", c.yellow("-"), shortenRefs(d))
		}
	}
	if res.Verdict == repro.VerdictNotReproduced {
		fmt.Fprintf(&b, "\n  %s\n", c.dim("If none of the differences explain it, the failure may be flaky or depend on state from other CI jobs."))
	}
	if res.Container != "" {
		fmt.Fprintf(&b, "\n  Kept container %s and workspace %s\n", res.Container, res.Workspace)
		fmt.Fprintf(&b, "  %s\n", c.cyan("docker exec -it "+res.Container+" bash"))
		fmt.Fprintf(&b, "  %s\n", c.dim("Remove them with: docker rm -f "+res.Container+" && rm -rf "+res.Workspace))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func truncateList(items []string, width int) string {
	s := strings.Join(items, ", ")
	if len(s) <= width {
		return s
	}
	return fmt.Sprintf("%s… (%d)", s[:width-6], len(items))
}
