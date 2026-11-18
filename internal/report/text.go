// Package report renders inspection results for humans.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/joblog"
)

// maxErrors caps how many error annotations are listed before "… and N more".
const maxErrors = 8

// TextOptions controls text rendering.
type TextOptions struct {
	Color bool
}

var categoryLabels = map[joblog.Category]string{
	joblog.CategoryTest:       "test failure",
	joblog.CategoryBuild:      "build / compile error",
	joblog.CategoryDependency: "dependency / install error",
	joblog.CategoryLint:       "lint / format check",
	joblog.CategoryDrift:      "files out of date (a check produced a git diff)",
	joblog.CategoryTimeout:    "timeout or cancellation",
	joblog.CategoryOOM:        "out of memory",
	joblog.CategoryUnknown:    "unclassified",
}

type painter struct{ on bool }

func (p painter) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p painter) bold(s string) string   { return p.wrap("1", s) }
func (p painter) dim(s string) string    { return p.wrap("2", s) }
func (p painter) red(s string) string    { return p.wrap("31", s) }
func (p painter) green(s string) string  { return p.wrap("32", s) }
func (p painter) yellow(s string) string { return p.wrap("33", s) }
func (p painter) cyan(s string) string   { return p.wrap("36", s) }

// WriteText renders rep as a terminal report.
func WriteText(w io.Writer, rep *inspect.Report, opts TextOptions) error {
	p := painter{on: opts.Color}
	var b strings.Builder
	r := rep.Run

	fmt.Fprintf(&b, "%s %s · %s\n", p.red("✗"), p.bold(r.Workflow), r.Repository)
	if r.Title != "" {
		fmt.Fprintf(&b, "  %q\n", r.Title)
	}
	meta := []string{r.Event}
	if r.Branch != "" {
		meta = append(meta, "on "+r.Branch)
	}
	meta = append(meta, "@ "+short(r.SHA), fmt.Sprintf("attempt %d", r.Attempt), conclusionText(r.Status, r.Conclusion))
	fmt.Fprintf(&b, "  %s\n", strings.Join(meta, " · "))
	if r.URL != "" {
		fmt.Fprintf(&b, "  %s\n", p.dim(r.URL))
	}

	if len(rep.Jobs) == 0 {
		fmt.Fprintf(&b, "\nNo failed jobs found in this run (%s).\n", conclusionText(r.Status, r.Conclusion))
	}
	for _, j := range rep.Jobs {
		writeJob(&b, p, j, r.WorkflowPath)
	}
	if len(rep.Warnings) > 0 {
		fmt.Fprintf(&b, "\n%s\n", p.yellow("Warnings"))
		for _, wn := range rep.Warnings {
			fmt.Fprintf(&b, "  ! %s\n", wn)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeJob(b *strings.Builder, p painter, j inspect.JobReport, wfPath string) {
	fmt.Fprintf(b, "\n%s\n", p.dim(strings.Repeat("─", 60)))
	fmt.Fprintf(b, "%s %s  %s\n", p.red("✗"), p.bold("Job: "+j.Name), p.dim("("+j.Conclusion+")"))
	if j.URL != "" {
		fmt.Fprintf(b, "  %s\n", p.dim(j.URL))
	}
	if runner := runnerText(j); runner != "" {
		fmt.Fprintf(b, "  %-10s %s\n", "Runner", runner)
	}
	if j.WorkflowJob != "" {
		loc := fmt.Sprintf("%s → jobs.%s", wfPath, j.WorkflowJob)
		if j.JobGuessed {
			loc += p.dim(" (best guess)")
		}
		fmt.Fprintf(b, "  %-10s %s\n", "Workflow", loc)
	}
	if len(j.Matrix) > 0 {
		fmt.Fprintf(b, "  %-10s %s\n", "Matrix", formatMap(j.Matrix, ", "))
	}

	if len(j.Steps) > 0 {
		fmt.Fprintf(b, "\n  %s\n", p.bold("Steps"))
		for _, s := range j.Steps {
			fmt.Fprintf(b, "    %s %2d  %s\n", stepIcon(p, s.Conclusion), s.Number, stepName(p, s, j.FailedStep))
		}
	}

	if fs := j.FailedStep; fs != nil {
		fmt.Fprintf(b, "\n  %s  #%d %s\n", p.bold("Failed step"), fs.Number, fs.Name)
		if fs.Uses != "" {
			fmt.Fprintf(b, "    uses: %s\n", fs.Uses)
			if len(fs.With) > 0 {
				fmt.Fprintf(b, "    with: %s\n", formatMap(fs.With, ", "))
			}
		}
		shell := fs.Shell
		if j.Failure != nil && j.Failure.Shell != "" {
			shell = j.Failure.Shell
		}
		if shell != "" {
			fmt.Fprintf(b, "    shell: %s\n", shell)
		}
		if fs.WorkingDirectory != "" {
			fmt.Fprintf(b, "    working-directory: %s\n", fs.WorkingDirectory)
		}
		for _, line := range commandLines(j) {
			fmt.Fprintf(b, "    %s %s\n", p.cyan("$"), line)
		}
	}

	f := j.Failure
	if f == nil {
		writeNotes(b, p, j.Notes)
		return
	}
	diag := categoryLabels[f.Category]
	if f.ExitCode != nil {
		diag += fmt.Sprintf(" · exit code %d", *f.ExitCode)
	}
	fmt.Fprintf(b, "\n  %s  %s\n", p.bold("Diagnosis"), p.red(diag))
	if len(f.FailedTests) > 0 {
		fmt.Fprintf(b, "\n  %s\n", p.bold("Failed tests"))
		for _, t := range f.FailedTests {
			fmt.Fprintf(b, "    • %s\n", t)
		}
	}
	if len(f.Errors) > 0 {
		fmt.Fprintf(b, "\n  %s\n", p.bold("Errors"))
		for i, e := range f.Errors {
			if i == maxErrors {
				fmt.Fprintf(b, "    %s\n", p.dim(fmt.Sprintf("… and %d more", len(f.Errors)-maxErrors)))
				break
			}
			fmt.Fprintf(b, "    • %s\n", e)
		}
	}
	if len(f.Excerpt) > 0 {
		where := fmt.Sprintf("last %d lines of %q", len(f.Excerpt), f.Section)
		if f.ExcerptAnchored {
			where = fmt.Sprintf("%d lines around the first error in %q", len(f.Excerpt), f.Section)
		}
		fmt.Fprintf(b, "\n  %s %s\n", p.bold("Log excerpt"), p.dim("("+where+")"))
		for _, line := range f.Excerpt {
			fmt.Fprintf(b, "    %s %s\n", p.dim("│"), line)
		}
	}
	writeNotes(b, p, j.Notes)
}

func writeNotes(b *strings.Builder, p painter, notes []string) {
	if len(notes) == 0 {
		return
	}
	fmt.Fprintf(b, "\n  %s\n", p.bold("Reproduction notes"))
	for _, n := range notes {
		fmt.Fprintf(b, "    %s %s\n", p.yellow("!"), n)
	}
}

// commandLines prefers the evaluated script printed in the log over the raw
// workflow `run:`, because expressions like ${{ matrix.go }} are resolved there.
func commandLines(j inspect.JobReport) []string {
	if j.Failure != nil && len(j.Failure.Command) > 0 && j.FailedStep != nil && j.FailedStep.Uses == "" {
		return j.Failure.Command
	}
	if j.FailedStep != nil && j.FailedStep.Run != "" {
		return strings.Split(strings.TrimRight(j.FailedStep.Run, "\n"), "\n")
	}
	return nil
}

func runnerText(j inspect.JobReport) string {
	var parts []string
	if len(j.RunsOn) > 0 {
		parts = append(parts, strings.Join(j.RunsOn, ", "))
	}
	if j.Runner.Image != "" {
		img := j.Runner.Image
		if j.Runner.ImageVersion != "" {
			img += " (" + j.Runner.ImageVersion + ")"
		}
		parts = append(parts, "→ "+img)
	}
	if j.Runner.Version != "" {
		parts = append(parts, "· runner "+j.Runner.Version)
	}
	return strings.Join(parts, " ")
}

func stepIcon(p painter, conclusion string) string {
	switch conclusion {
	case "success":
		return p.green("✓")
	case "failure", "timed_out":
		return p.red("✗")
	case "cancelled":
		return p.yellow("⊘")
	case "skipped":
		return p.dim("○")
	}
	return p.dim("·")
}

func stepName(p painter, s inspect.StepStatus, failed *inspect.FailedStep) string {
	switch {
	case failed != nil && s.Number == failed.Number:
		return p.red(s.Name) + p.red("  ← failed here")
	case s.Conclusion == "skipped":
		return p.dim(s.Name + " (skipped)")
	}
	return s.Name
}

func conclusionText(status, conclusion string) string {
	if conclusion != "" {
		return conclusion
	}
	return status
}

func formatMap(m map[string]string, sep string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, sep)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
