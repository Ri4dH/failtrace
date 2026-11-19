package repro

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/docker"
)

// stepCounter returns a step behaviour that fails on the listed attempts.
func stepCounter(failOn map[int]bool) (func(docker.ExecOptions, string) int, *int) {
	calls := 0
	return func(o docker.ExecOptions, _ string) int {
		calls++
		fmt.Fprintf(o.Stdout, "attempt %d\n", calls)
		if failOn[calls] {
			fmt.Fprintln(o.Stdout, "--- FAIL: TestIntegration (0.01s)")
			return 1
		}
		return 0
	}, &calls
}

func countScriptExecs(eng *fakeEngine, n int) int {
	count := 0
	for _, e := range eng.execs {
		if strings.HasSuffix(e.Cmd[len(e.Cmd)-1], fmt.Sprintf("step-%d.sh", n)) {
			count++
		}
	}
	return count
}

func TestRunsDetectFlakiness(t *testing.T) {
	target, calls := stepCounter(map[int]bool{2: true, 3: true})
	eng := &fakeEngine{steps: map[int]func(docker.ExecOptions, string) int{5: target}}
	res, out := run(t, eng, &fakeGit{}, samplePlan(), ciFailure("TestIntegration"), Options{Runs: 4})

	if res.Verdict != VerdictFlaky || res.Reasons[0] != "the step failed in 2 of 4 runs" {
		t.Fatalf("verdict = %s %q", res.Verdict, res.Reasons)
	}
	if got := lastStepExec(eng, 5).Env["GOFLAGS"]; got != "-count=1" {
		t.Errorf("GOFLAGS = %q, want the test cache bypassed", got)
	}
	if got := lastStepExec(eng, 4).Env["GOFLAGS"]; got != "" {
		t.Errorf("setup steps keep CI's GOFLAGS, got %q", got)
	}
	if !strings.Contains(strings.Join(res.Differences, "\n"), "test cache is bypassed") {
		t.Errorf("differences = %q", res.Differences)
	}
	if *calls != 4 || countScriptExecs(eng, 4) != 1 {
		t.Errorf("target ran %d times, setup step %d times; want 4 and 1", *calls, countScriptExecs(eng, 4))
	}
	if len(res.Attempts) != 4 || res.Attempts[1].Verdict != VerdictReproduced || res.Attempts[0].Verdict != VerdictNotReproduced {
		t.Errorf("attempts = %+v", res.Attempts)
	}
	// The first failing attempt is the one compared with CI.
	if *res.LocalExitCode != 1 || len(res.Local.FailedTests) != 1 {
		t.Errorf("local = %+v exit %v", res.Local, *res.LocalExitCode)
	}
	last := res.Steps[len(res.Steps)-1]
	if last.Status != StepFailed || !strings.Contains(strings.Join(last.Output, "\n"), "attempt 2") {
		t.Errorf("target step = %+v", last)
	}
	// Only the first attempt streams its output.
	if !strings.Contains(out, "│ attempt 1") || strings.Contains(out, "│ attempt 2") || !strings.Contains(out, "(running it 4 times)") ||
		!strings.Contains(out, "  run 2/4 ✗ exit code 1") || !strings.Contains(out, "  run 4/4 ✓ exit code 0") {
		t.Errorf("output:\n%s", out)
	}
}

func TestRunsAllFailOrAllPass(t *testing.T) {
	target, _ := stepCounter(map[int]bool{1: true, 2: true, 3: true})
	eng := &fakeEngine{steps: map[int]func(docker.ExecOptions, string) int{5: target}}
	res, _ := run(t, eng, &fakeGit{}, samplePlan(), ciFailure("TestIntegration"), Options{Runs: 3})
	if res.Verdict != VerdictReproduced || res.Reasons[0] != "the step failed in all 3 runs" {
		t.Errorf("all fail: %s %q", res.Verdict, res.Reasons)
	}

	pass, _ := stepCounter(nil)
	eng = &fakeEngine{steps: map[int]func(docker.ExecOptions, string) int{5: pass}}
	res, _ = run(t, eng, &fakeGit{}, samplePlan(), ciFailure("TestIntegration"), Options{Runs: 3})
	if res.Verdict != VerdictNotReproduced || res.Reasons[0] != "the step passed in all 3 runs" {
		t.Errorf("all pass: %s %q", res.Verdict, res.Reasons)
	}

	if _, err := Run(context.Background(), Deps{Engine: &fakeEngine{}}, samplePlan(), nil, Options{Runs: MaxRuns + 1}); err == nil {
		t.Error("expected an error above MaxRuns")
	}
}

func TestShellOpensAtTheFailingStep(t *testing.T) {
	eng := &fakeEngine{}
	var term bytes.Buffer
	res, out := run(t, eng, &fakeGit{}, samplePlan(), ciFailure(), Options{
		Shell: true, ShellIO: ShellIO{In: strings.NewReader("exit\n"), Out: &term, TTY: true},
		Secrets: map[string]string{"API_TOKEN": "s3cret"},
	})
	if res == nil {
		t.Fatal("no result")
	}
	sh := eng.execs[len(eng.execs)-1]
	if sh.Stdin == nil || !sh.TTY || sh.Stdout != &term || sh.Cmd[0] != "sh" {
		t.Fatalf("shell exec = %+v", sh)
	}
	if sh.Workdir != "/home/runner/work/widget/widget/service" || sh.Env["DATABASE_URL"] == "" || sh.Env["API_TOKEN"] != "s3cret" || sh.Env["PS1"] == "" {
		t.Errorf("shell should get the failing step's directory and env: %s %v", sh.Workdir, sh.Env)
	}
	if !strings.Contains(out, "re-run the step with: bash --noprofile --norc -e -o pipefail /home/runner/work/_temp/failtrace/step-5.sh") {
		t.Errorf("output:\n%s", out)
	}
	if len(eng.removed) != 2 {
		t.Errorf("containers should be removed after the shell exits: %q", eng.removed)
	}
}

func TestShellAfterDivergedStep(t *testing.T) {
	eng := &fakeEngine{steps: map[int]func(docker.ExecOptions, string) int{
		4: func(docker.ExecOptions, string) int { return 2 },
	}}
	_, out := run(t, eng, &fakeGit{}, samplePlan(), ciFailure(), Options{
		Shell: true, ShellIO: ShellIO{In: strings.NewReader(""), Out: &bytes.Buffer{}},
	})
	if !strings.Contains(out, "at step 4 (Prepare)") {
		t.Errorf("the shell should open where the run stopped:\n%s", out)
	}
}

func TestShellWithoutTerminal(t *testing.T) {
	res, err := Run(context.Background(), Deps{Engine: &fakeEngine{}, Git: &fakeGit{}, Resolver: fakeResolver{}},
		samplePlan(), ciFailure(), Options{Shell: true, WorkDir: t.TempDir()})
	if !errors.Is(err, ErrShell) || res == nil || res.Verdict == "" {
		t.Fatalf("err = %v, res = %+v; want ErrShell with a complete result", err, res)
	}
}
