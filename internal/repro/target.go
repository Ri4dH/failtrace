package repro

import (
	"context"
	"fmt"

	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/plan"
)

// MaxRuns bounds --runs.
const MaxRuns = 20

// targetOutputLines is how much of the failing step's output a Result keeps.
const targetOutputLines = 60

// Attempt is one run of the failing step.
type Attempt struct {
	Run      int     `json:"run"`
	ExitCode int     `json:"exit_code"`
	Verdict  Verdict `json:"verdict"`
	Seconds  float64 `json:"seconds"`
}

// runTarget runs the failing step opts.Runs times in the same container.
// Setup steps run once, so repeated attempts are cheap; a step that fails
// only some of the time is reported as flaky.
func (r *runner) runTarget(ctx context.Context, n int, s plan.Step, label string, sr StepResult, res *Result) error {
	runs := r.opts.Runs
	if runs < 1 {
		runs = 1
	}
	type outcome struct {
		code    int
		local   *joblog.Failure
		verdict Verdict
		reasons []string
		output  []string
	}
	var first, firstFail *outcome
	failures := 0
	total := 0.0
	for a := 1; a <= runs; a++ {
		switch {
		case runs == 1:
			r.logf(label, "%s  ← failed in CI", s.Name)
		case a == 1:
			r.logf(label, "%s  ← failed in CI (running it %d times)", s.Name, runs)
		}
		code, output, dur, err := r.runStep(ctx, n, s, a == 1 || r.opts.Verbose)
		if err != nil {
			return fmt.Errorf("step %d (%s): %w", n, s.Name, err)
		}
		prefix := ""
		if runs > 1 {
			prefix = fmt.Sprintf("run %d/%d ", a, runs)
		}
		r.logExit(prefix, code, dur)
		total += dur.Seconds()
		local := analyzeLocal(s.Name, output, code)
		v, reasons := compare(res.CI, local, code)
		o := &outcome{code: code, local: local, verdict: v, reasons: reasons, output: output}
		res.Attempts = append(res.Attempts, Attempt{Run: a, ExitCode: code, Verdict: v, Seconds: dur.Seconds()})
		if first == nil {
			first = o
		}
		if code != 0 {
			failures++
			if firstFail == nil {
				firstFail = o
			}
		}
	}

	// Report the first failing attempt when there is one: that is the
	// behaviour worth comparing with CI.
	chosen := first
	if firstFail != nil {
		chosen = firstFail
	}
	res.LocalExitCode, res.Local = &chosen.code, chosen.local
	res.Verdict, res.Reasons = chosen.verdict, chosen.reasons
	switch {
	case runs == 1:
	case failures == 0:
		res.Reasons = []string{fmt.Sprintf("the step passed in all %d runs", runs)}
	case failures == runs:
		res.Reasons = append([]string{fmt.Sprintf("the step failed in all %d runs", runs)}, res.Reasons...)
	default:
		res.Verdict = VerdictFlaky
		res.Reasons = append([]string{fmt.Sprintf("the step failed in %d of %d runs", failures, runs)}, chosen.reasons...)
	}

	sr.ExitCode, sr.Seconds = &chosen.code, total
	sr.Status = StepPassed
	if failures > 0 {
		sr.Status = StepFailed
	}
	sr.Output = tailLines(chosen.output, targetOutputLines)
	res.Steps = append(res.Steps, sr)
	return nil
}
