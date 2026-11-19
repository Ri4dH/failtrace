package report

import (
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/joblog"
)

func llmReport() *inspect.Report {
	one := 1
	vet := func(name, goVersion string) inspect.JobReport {
		return inspect.JobReport{
			Name: name, WorkflowJob: "build", Matrix: map[string]string{"go": goVersion},
			Runner:     joblog.RunnerInfo{Image: "ubuntu-24.04", Version: "2.337.0"},
			Steps:      []inspect.StepStatus{{Number: 1, Name: "Set up job", Conclusion: "success"}},
			FailedStep: &inspect.FailedStep{Number: 6, Name: "Run ./vet.sh", Run: "./vet.sh"},
			Failure: &joblog.Failure{
				Section: "Run ./vet.sh", Command: []string{"./vet.sh"}, Shell: "/usr/bin/bash -e {0}", ExitCode: &one,
				Env: map[string]string{
					"GOTOOLCHAIN": "local", "pythonLocation": "/opt/hostedtoolcache/Python/3.12.1/x64",
					"UV_CACHE_DIR": "/home/runner/work/_temp/setup-uv-cache", "BIG": strings.Repeat("x", 200),
				},
				Errors: []string{"a_test.go:85:13: non-constant format string", "Process completed with exit code 1."},
				Excerpt: []string{
					"", "Error: a_test.go:85:13: non-constant format string",
					"Error: Process completed with exit code 1.",
				},
				Category: joblog.CategoryLint,
			},
		}
	}
	test := inspect.JobReport{
		Name: "test", FailedStep: &inspect.FailedStep{Number: 4, Name: "Run go test -v ./..."},
		Failure: &joblog.Failure{
			Section: "Run go test -v ./...", Command: []string{"go test -v ./..."}, ExitCode: &one,
			FailedTests: []string{"TestA"}, ExcerptAnchored: true, Category: joblog.CategoryTest,
			Excerpt: []string{
				"=== RUN   TestA", "=== PAUSE TestA", "=== CONT  TestA", "    a_test.go:9: boom",
				"··········-···-··········", "", "", "--- FAIL: TestA (0.01s)", "--- PASS: TestB (0.00s)", "FAIL",
			},
		},
	}
	return &inspect.Report{
		Run: inspect.RunInfo{
			Repository: "acme/widget", ID: 42, Attempt: 1, Workflow: "CI", WorkflowPath: ".github/workflows/ci.yml",
			Event: "pull_request", Branch: "fix", SHA: "0123456789abcdef0123456789abcdef01234567", Title: "Fix it",
			URL: "https://github.com/acme/widget/actions/runs/42",
		},
		Jobs: []inspect.JobReport{vet("build (1.22)", "1.22"), vet("build (1.23)", "1.23"), test,
			{Name: "lost", Notes: []string{"job log unavailable: 404"}}},
	}
}

func TestWriteLLM(t *testing.T) {
	var b strings.Builder
	if err := WriteLLM(&b, llmReport()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	want := "# CI failure: acme/widget, workflow \"CI\" (.github/workflows/ci.yml)\n" +
		"Run: https://github.com/acme/widget/actions/runs/42 (attempt 1, pull_request)\n" +
		"Commit: 0123456789abcdef0123456789abcdef01234567 on fix (\"Fix it\")\n" +
		"\n" +
		"## Job: build (1.22)\n" +
		"workflow job: build; matrix: go=1.22; runner: ubuntu-24.04\n" +
		"Failed step #6: Run ./vet.sh\n" +
		"Command (shell: /usr/bin/bash -e {0}):\n```\n./vet.sh\n```\n" +
		"Env: BIG=" + strings.Repeat("x", 120) + "…, GOTOOLCHAIN=local\n" +
		"Result: lint / format check, exit code 1\n" +
		"Errors:\n- a_test.go:85:13: non-constant format string\n" +
		"\n" +
		"## Job: build (1.23)\n" +
		"Same failure as job \"build (1.22)\".\n" +
		"\n" +
		"## Job: test\n" +
		"Failed step #4: Run go test -v ./...\n" +
		"Command:\n```\ngo test -v ./...\n```\n" +
		"Result: test failure, exit code 1\n" +
		"Failed tests: TestA\n" +
		"Log (from the first error):\n```\n    a_test.go:9: boom\n\n--- FAIL: TestA (0.01s)\nFAIL\n```\n" +
		"\n" +
		"## Job: lost\n" +
		"Note: job log unavailable: 404\n" +
		"Failed step: unknown (no step failed in the API's view; see the run page)\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	for _, noise := range []string{"Set up job", "2.337.0", "hostedtoolcache", "_temp", "=== RUN", "--- PASS", "···", "\x1b["} {
		if strings.Contains(out, noise) {
			t.Errorf("output contains %q", noise)
		}
	}
}

func TestWriteLLMNoJobs(t *testing.T) {
	rep := &inspect.Report{
		Run:      inspect.RunInfo{Repository: "acme/widget", Workflow: "CI", Attempt: 1, Event: "push", SHA: "abc", Conclusion: "failure"},
		Warnings: []string{"the run failed before any job started"},
	}
	var b strings.Builder
	_ = WriteLLM(&b, rep)
	if !strings.Contains(b.String(), "Warning: the run failed before any job started\n\nNo failed jobs (run conclusion: failure).\n") {
		t.Errorf("output:\n%s", b.String())
	}
}

func TestLLMFenceAvoidsBackticks(t *testing.T) {
	var b strings.Builder
	writeBlock(&b, "Log", []string{"```go", "x", "```"})
	if !strings.HasPrefix(b.String(), "Log:\n````\n```go") {
		t.Errorf("fence = %q", b.String())
	}
}
