package repro

import (
	"fmt"
	"strings"

	"github.com/Ri4dH/failtrace/internal/joblog"
)

// Verdict is the outcome of a reproduction.
type Verdict string

const (
	VerdictReproduced    Verdict = "reproduced"        // the step failed locally the way it failed in CI
	VerdictDifferent     Verdict = "different-failure" // the step failed locally, but differently
	VerdictNotReproduced Verdict = "not-reproduced"    // the step passed locally
	VerdictDiverged      Verdict = "diverged"          // an earlier step failed locally, so the target never ran
)

// analyzeLocal summarises the failing step's local output with the same
// rules used for CI logs.
func analyzeLocal(title string, output []string, exitCode int) *joblog.Failure {
	sec := &joblog.Section{Title: title, Body: output}
	if exitCode != 0 {
		sec.Errors = []string{fmt.Sprintf("Process completed with exit code %d.", exitCode)}
	}
	return joblog.Analyze(sec, 25)
}

// compare decides whether the local run reproduced the CI failure.
func compare(ci, local *joblog.Failure, localExit int) (Verdict, []string) {
	if localExit == 0 {
		return VerdictNotReproduced, []string{"the step that failed in CI passed locally"}
	}
	if ci == nil {
		return VerdictReproduced, []string{fmt.Sprintf("the step failed locally with exit code %d (CI's failure details were unavailable)", localExit)}
	}
	if len(ci.FailedTests) > 0 && len(local.FailedTests) > 0 {
		same := intersect(ci.FailedTests, local.FailedTests)
		switch {
		case len(same) == len(ci.FailedTests) && len(same) == len(local.FailedTests):
			return VerdictReproduced, []string{"the same tests failed: " + list(same)}
		case len(same) > 0:
			return VerdictReproduced, []string{
				"some of the same tests failed: " + list(same),
				fmt.Sprintf("%d failed in CI, %d failed locally", len(ci.FailedTests), len(local.FailedTests)),
			}
		}
		return VerdictDifferent, []string{"different tests failed: CI " + list(ci.FailedTests) + ", locally " + list(local.FailedTests)}
	}
	ciExit := -1
	if ci.ExitCode != nil {
		ciExit = *ci.ExitCode
	}
	switch {
	case ci.Signature != "" && ci.Signature == local.Signature:
		return VerdictReproduced, []string{"the same error: " + local.Signature}
	case ciExit == localExit && ci.Category == local.Category && ci.Category != joblog.CategoryUnknown:
		return VerdictReproduced, []string{fmt.Sprintf("the same exit code (%d) and kind of failure (%s)", localExit, local.Category)}
	case ciExit == localExit:
		return VerdictReproduced, []string{fmt.Sprintf("the same exit code (%d); compare the output below with CI's to be sure", localExit)}
	}
	return VerdictDifferent, []string{fmt.Sprintf("exit code %d locally, %d in CI", localExit, ciExit)}
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}

func list(items []string) string {
	const max = 5
	if len(items) > max {
		return strings.Join(items[:max], ", ") + fmt.Sprintf(" and %d more", len(items)-max)
	}
	return strings.Join(items, ", ")
}
