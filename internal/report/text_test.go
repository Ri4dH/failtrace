package report

import (
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/joblog"
)

func sampleReport() *inspect.Report {
	code := 1
	return &inspect.Report{
		Run: inspect.RunInfo{
			Repository: "acme/widget", Workflow: "CI", WorkflowPath: ".github/workflows/ci.yml",
			Title: "Fix parser", Event: "push", Branch: "main", SHA: "abc1234567", Attempt: 1,
			Conclusion: "failure", URL: "https://github.com/acme/widget/actions/runs/42",
		},
		Jobs: []inspect.JobReport{{
			Name: "test (ubuntu-latest, 1.22)", Conclusion: "failure", WorkflowJob: "test",
			Matrix: map[string]string{"os": "ubuntu-latest", "go": "1.22"},
			RunsOn: []string{"ubuntu-latest"},
			Runner: joblog.RunnerInfo{Image: "ubuntu-24.04", ImageVersion: "20261111.1", Version: "2.338.2"},
			Steps: []inspect.StepStatus{
				{Number: 1, Name: "Set up job", Conclusion: "success"},
				{Number: 2, Name: "Run go test ./...", Conclusion: "failure"},
				{Number: 3, Name: "Upload", Conclusion: "skipped"},
			},
			FailedStep: &inspect.FailedStep{Number: 2, Name: "Run go test ./...", Run: "go test ${{ matrix.flags }}", Shell: "bash"},
			Failure: &joblog.Failure{
				Section: "Run go test ./...", Command: []string{"go test ./..."}, Shell: "/usr/bin/bash -e {0}",
				ExitCode: &code, FailedTests: []string{"TestParseConfig"}, Excerpt: []string{"--- FAIL: TestParseConfig"},
				Category: joblog.CategoryTest,
			},
			Notes: []string{"failing step references secrets not available locally: TOKEN"},
		}},
		Warnings: []string{"something odd"},
	}
}

func TestWriteText(t *testing.T) {
	var b strings.Builder
	if err := WriteText(&b, sampleReport(), TextOptions{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"CI · acme/widget",
		"push · on main · @ abc1234 · attempt 1 · failure",
		"ubuntu-latest → ubuntu-24.04 (20261111.1) · runner 2.338.2",
		".github/workflows/ci.yml → jobs.test",
		"go=1.22, os=ubuntu-latest",
		"Run go test ./...  ← failed here",
		"Upload (skipped)",
		"shell: /usr/bin/bash -e {0}",
		"$ go test ./...", // evaluated command from the log wins over the raw template
		"test failure · exit code 1",
		"• TestParseConfig",
		"│ --- FAIL: TestParseConfig",
		"! failing step references secrets",
		"! something odd",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("output contains ANSI escapes with color disabled")
	}
}

func TestWriteTextColor(t *testing.T) {
	var b strings.Builder
	if err := WriteText(&b, sampleReport(), TextOptions{Color: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\x1b[31m") {
		t.Error("expected ANSI color codes")
	}
}

func TestWriteTextNoFailedJobs(t *testing.T) {
	rep := sampleReport()
	rep.Jobs, rep.Warnings = nil, nil
	rep.Run.Conclusion = "success"
	var b strings.Builder
	if err := WriteText(&b, rep, TextOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "No failed jobs found in this run (success)") {
		t.Errorf("output = %s", b.String())
	}
}

func TestWriteTextCapsErrors(t *testing.T) {
	rep := sampleReport()
	for i := 0; i < maxErrors+3; i++ {
		rep.Jobs[0].Failure.Errors = append(rep.Jobs[0].Failure.Errors, "err")
	}
	var b strings.Builder
	_ = WriteText(&b, rep, TextOptions{})
	if !strings.Contains(b.String(), "… and 3 more") {
		t.Errorf("expected error list to be capped:\n%s", b.String())
	}
}
