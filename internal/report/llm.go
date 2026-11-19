package report

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/Ri4dH/failtrace/internal/inspect"
)

// WriteLLM renders an inspection as compact plain text for a language
// model's prompt: what failed and why, without colours, step lists or runner
// metadata. Error lines are printed once, and jobs that failed the same way
// (typically matrix variants) are collapsed into one line.
func WriteLLM(w io.Writer, rep *inspect.Report) error {
	var b strings.Builder
	r := rep.Run
	fmt.Fprintf(&b, "# CI failure: %s, workflow %q", r.Repository, r.Workflow)
	if r.WorkflowPath != "" {
		fmt.Fprintf(&b, " (%s)", r.WorkflowPath)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Run: %s (attempt %d, %s)\n", r.URL, r.Attempt, r.Event)
	commit := "Commit: " + r.SHA
	if r.Branch != "" {
		commit += " on " + r.Branch
	}
	if r.Title != "" {
		commit += fmt.Sprintf(" (%q)", r.Title)
	}
	b.WriteString(commit + "\n")
	for _, warn := range rep.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", warn)
	}
	if len(rep.Jobs) == 0 {
		fmt.Fprintf(&b, "\nNo failed jobs (run conclusion: %s).\n", orDash(r.Conclusion))
	}

	seen := map[string]string{} // failure key → first job with it
	for _, j := range rep.Jobs {
		b.WriteString("\n")
		writeLLMJob(&b, j, seen)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeLLMJob(b *strings.Builder, j inspect.JobReport, seen map[string]string) {
	fmt.Fprintf(b, "## Job: %s\n", j.Name)
	if key := failureKey(j); key != "" {
		if first, ok := seen[key]; ok {
			fmt.Fprintf(b, "Same failure as job %q.\n", first)
			return
		}
		seen[key] = j.Name
	}
	var meta []string
	if j.WorkflowJob != "" {
		meta = append(meta, "workflow job: "+j.WorkflowJob)
	}
	if len(j.Matrix) > 0 {
		meta = append(meta, "matrix: "+sortedPairs(j.Matrix))
	}
	if j.Runner.Image != "" {
		meta = append(meta, "runner: "+j.Runner.Image)
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, "; ") + "\n")
	}
	for _, n := range j.Notes {
		fmt.Fprintf(b, "Note: %s\n", n)
	}

	fs, f := j.FailedStep, j.Failure
	switch {
	case fs != nil:
		fmt.Fprintf(b, "Failed step #%d: %s\n", fs.Number, fs.Name)
	case f != nil:
		fmt.Fprintf(b, "Failed step: %s\n", f.Section)
	default:
		b.WriteString("Failed step: unknown (no step failed in the API's view; see the run page)\n")
		return
	}
	if fs != nil {
		if fs.Uses != "" {
			fmt.Fprintf(b, "Action: %s\n", fs.Uses)
		}
		if fs.WorkingDirectory != "" {
			fmt.Fprintf(b, "Working directory: %s\n", fs.WorkingDirectory)
		}
	}
	if f == nil {
		b.WriteString("No log details available.\n")
		return
	}

	command := f.Command
	if len(command) == 0 && fs != nil && fs.Run != "" {
		command = strings.Split(strings.TrimRight(fs.Run, "\n"), "\n")
	}
	if len(command) > 0 {
		label := "Command"
		if f.Shell != "" {
			label += fmt.Sprintf(" (shell: %s)", f.Shell)
		}
		writeBlock(b, label, command)
	}
	if len(f.With) > 0 {
		fmt.Fprintf(b, "Inputs: %s\n", sortedPairs(f.With))
	}
	if env := relevantEnv(f.Env); len(env) > 0 {
		fmt.Fprintf(b, "Env: %s\n", sortedPairs(env))
	}

	result := "Result: " + categoryLabels[f.Category]
	if f.ExitCode != nil {
		result += fmt.Sprintf(", exit code %d", *f.ExitCode)
	}
	b.WriteString(result + "\n")
	if len(f.FailedTests) > 0 {
		fmt.Fprintf(b, "Failed tests: %s\n", strings.Join(f.FailedTests, ", "))
	}
	errs := meaningfulErrors(f.Errors)
	if len(errs) > 0 {
		b.WriteString("Errors:\n")
		for _, e := range errs {
			fmt.Fprintf(b, "- %s\n", e)
		}
	}
	if excerpt := dropRepeats(f.Excerpt, errs); len(excerpt) > 0 {
		label := "Log (end of the step)"
		if f.ExcerptAnchored {
			label = "Log (from the first error)"
		}
		writeBlock(b, label, excerpt)
	}
}

// failureKey identifies a failure's content, so identical failures in
// several jobs are printed once. Jobs without log details are never merged.
func failureKey(j inspect.JobReport) string {
	f := j.Failure
	if f == nil {
		return ""
	}
	exit := -1
	if f.ExitCode != nil {
		exit = *f.ExitCode
	}
	return strings.Join([]string{
		f.Section, strings.Join(f.Command, "\n"), fmt.Sprint(exit),
		strings.Join(f.Errors, "\n"), strings.Join(f.FailedTests, "\n"), strings.Join(f.Excerpt, "\n"),
	}, "\x00")
}

// meaningfulErrors drops GitHub's generic exit-code annotation, which the
// Result line already states.
func meaningfulErrors(errs []string) []string {
	var out []string
	for _, e := range errs {
		if !strings.HasPrefix(e, "Process completed with exit code ") {
			out = append(out, e)
		}
	}
	return out
}

// relevantEnv drops variables that setup actions export to locate their
// tools (pythonLocation, PKG_CONFIG_PATH, ...): they explain nothing about a
// failure. Long values are shortened.
func relevantEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if strings.Contains(v, "hostedtoolcache") || strings.Contains(v, "_temp") || strings.Contains(v, "_actions") {
			continue
		}
		if len(v) > maxEnvValue {
			v = v[:maxEnvValue] + "…"
		}
		out[k] = strings.ReplaceAll(v, "\n", " ")
	}
	return out
}

const maxEnvValue = 120

// goProgressRe matches go test -v progress lines; the failures themselves
// are reported on --- FAIL lines and in the test's output.
var goProgressRe = regexp.MustCompile(`^\s*(?:=== (?:RUN|PAUSE|CONT|NAME)\s|--- (?:PASS|SKIP): )`)

// dropRepeats removes excerpt lines that carry nothing new: errors already
// listed, test progress, and progress bars. Runs of blank lines are
// collapsed. It returns nil if nothing is left.
func dropRepeats(excerpt, errs []string) []string {
	known := map[string]bool{}
	for _, e := range errs {
		known[strings.TrimSpace(e)] = true
	}
	var out []string
	for _, l := range excerpt {
		t := strings.TrimSpace(l)
		switch {
		case known[t] || known[strings.TrimPrefix(t, "Error: ")] || strings.HasPrefix(t, "Error: Process completed with exit code "),
			goProgressRe.MatchString(l), isProgressBar(t):
			continue
		case t == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == ""):
			continue
		}
		out = append(out, l)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isProgressBar reports whether t is a long run of progress or separator
// characters, such as the dots test runners print per test.
func isProgressBar(t string) bool {
	if len([]rune(t)) < 20 {
		return false
	}
	for _, r := range t {
		if !strings.ContainsRune("·•.-_=─━⎯*~+xX ", r) {
			return false
		}
	}
	return true
}

func writeBlock(b *strings.Builder, label string, lines []string) {
	fence := codeFence(lines)
	fmt.Fprintf(b, "%s:\n%s\n%s\n%s\n", label, fence, strings.Join(lines, "\n"), fence)
}

func sortedPairs(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + m[k]
	}
	return strings.Join(pairs, ", ")
}
