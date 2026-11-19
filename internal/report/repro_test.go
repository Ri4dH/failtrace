package report

import (
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/repro"
)

func TestWriteRepro(t *testing.T) {
	one, two := 1, 2
	res := &repro.Result{
		Verdict: repro.VerdictReproduced,
		Reasons: []string{"the same tests failed: TestX"},
		CI:      &joblog.Failure{ExitCode: &one, Category: joblog.CategoryTest, FailedTests: []string{"TestX"}},
		Local:   &joblog.Failure{ExitCode: &one, Category: joblog.CategoryTest, FailedTests: []string{"TestX"}},
		Steps: []repro.StepResult{
			{Status: repro.StepPassed}, {Status: repro.StepPassed}, {Status: repro.StepSkipped}, {Status: repro.StepFailed, Target: true},
		},
		Image: "failtrace-env:abc", Platform: "linux/amd64", Seconds: 12,
		Differences: []string{"steps run as root", "uses actions/foo@0123456789012345678901234567890123456789"},
		Container:   "failtrace-7-abc", Workspace: "/tmp/ws",
	}
	var b strings.Builder
	if err := WriteRepro(&b, res, TextOptions{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"✓ Reproduced: the step fails locally the same way it failed in CI",
		"• the same tests failed: TestX",
		"Exit code      1                            1",
		"Failure        test failure                 test failure",
		"Steps      2 passed · 1 failed · 1 skipped (12s total)",
		"Image      failtrace-env:abc linux/amd64",
		"- steps run as root",
		"actions/foo@0123456",
		"docker exec -it failtrace-7-abc bash",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}

	res.Verdict, res.Container = repro.VerdictNotReproduced, ""
	res.Local = &joblog.Failure{Category: joblog.CategoryUnknown}
	b.Reset()
	_ = WriteRepro(&b, res, TextOptions{})
	if !strings.Contains(b.String(), "✗ Not reproduced") || !strings.Contains(b.String(), "may be flaky") || strings.Contains(b.String(), "docker exec") {
		t.Errorf("not reproduced output:\n%s", b.String())
	}

	res.Verdict, res.Local, res.CI = repro.VerdictDiverged, nil, &joblog.Failure{ExitCode: &two}
	b.Reset()
	_ = WriteRepro(&b, res, TextOptions{})
	if !strings.Contains(b.String(), "⚠ Diverged") {
		t.Errorf("diverged output:\n%s", b.String())
	}
}

func TestWriteReproSource(t *testing.T) {
	res := &repro.Result{
		Verdict: repro.VerdictNotReproduced, Reasons: []string{"the step that failed in CI passed locally"},
		Source: &repro.Source{Dir: "/home/me/widget", Head: "abcdef0123456789", Changed: 2},
	}
	var b strings.Builder
	_ = WriteRepro(&b, res, TextOptions{})
	if !strings.Contains(b.String(), "✓ Passes: the step that failed in CI passes with your working tree") || strings.Contains(b.String(), "may be flaky") {
		t.Errorf("passing output:\n%s", b.String())
	}
	res.Verdict = repro.VerdictReproduced
	b.Reset()
	_ = WriteRepro(&b, res, TextOptions{})
	if !strings.Contains(b.String(), "✗ Still fails") {
		t.Errorf("still failing output:\n%s", b.String())
	}
}
