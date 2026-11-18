package inspect

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/github"
	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/runurl"
)

// fakeAPI is an in-memory stand-in for the GitHub client.
type fakeAPI struct {
	run      *github.Run
	jobs     []github.Job
	logs     map[int64]string
	files    map[string][]byte
	fileErr  error
	attempts []int
}

func (f *fakeAPI) GetRun(context.Context, string, string, int64) (*github.Run, error) {
	return f.run, nil
}

func (f *fakeAPI) ListJobs(_ context.Context, _, _ string, _ int64, attempt int) ([]github.Job, error) {
	f.attempts = append(f.attempts, attempt)
	return f.jobs, nil
}

func (f *fakeAPI) GetJobLog(_ context.Context, _, _ string, id int64) (string, error) {
	if l, ok := f.logs[id]; ok {
		return l, nil
	}
	return "", &github.APIError{StatusCode: 410, Message: "Gone", Path: "/logs"}
}

func (f *fakeAPI) GetFile(_ context.Context, _, _, path, ref string) ([]byte, error) {
	if f.fileErr != nil {
		return nil, f.fileErr
	}
	if d, ok := f.files[path+"@"+ref]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newFake(t *testing.T) *fakeAPI {
	steps := []github.Step{
		{Name: "Set up job", Number: 1, Conclusion: "success"},
		{Name: "Initialize containers", Number: 2, Conclusion: "success"},
		{Name: "Run actions/checkout@v4", Number: 3, Conclusion: "success"},
		{Name: "Set up Go", Number: 4, Conclusion: "success"},
		{Name: "Run go test ./...", Number: 5, Conclusion: "failure"},
		{Name: "Upload coverage", Number: 6, Conclusion: "skipped"},
	}
	return &fakeAPI{
		run: &github.Run{
			ID: 42, Name: "CI", HeadSHA: "abc1234567", Path: ".github/workflows/ci.yml@refs/heads/main",
			Event: "push", HeadBranch: "main", Conclusion: "failure", RunAttempt: 3,
		},
		jobs: []github.Job{
			{ID: 1, Name: "test (ubuntu-latest, 1.22)", Conclusion: "failure", Labels: []string{"ubuntu-latest"}, Steps: steps},
			{ID: 2, Name: "test (macos-latest, 1.22)", Conclusion: "cancelled"},
			{ID: 3, Name: "Lint golangci", Conclusion: "success"},
		},
		logs:  map[int64]string{1: read(t, "../joblog/testdata/go_test_failure.log")},
		files: map[string][]byte{".github/workflows/ci.yml@abc1234567": []byte(read(t, "../workflow/testdata/ci.yml"))},
	}
}

var ref = runurl.Ref{Host: "github.com", Owner: "acme", Repo: "widget", RunID: 42}

func TestRunFullReport(t *testing.T) {
	api := newFake(t)
	rep, err := Run(context.Background(), api, ref, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.attempts, []int{3}) {
		t.Errorf("listed attempts %v, want latest attempt 3", api.attempts)
	}
	if rep.Run.WorkflowPath != ".github/workflows/ci.yml" || rep.Run.Attempt != 3 {
		t.Errorf("run info = %+v", rep.Run)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %q", rep.Warnings)
	}
	if len(rep.Jobs) != 1 {
		t.Fatalf("got %d jobs, want only the failed one (cancelled siblings excluded)", len(rep.Jobs))
	}

	j := rep.Jobs[0]
	if j.WorkflowJob != "test" || !reflect.DeepEqual(j.Matrix, map[string]string{"os": "ubuntu-latest", "go": "1.22"}) {
		t.Errorf("job match = %s %v", j.WorkflowJob, j.Matrix)
	}
	fs := j.FailedStep
	if fs == nil || fs.Number != 5 || fs.Shell != "bash" || !strings.Contains(fs.Run, "go vet") || fs.Env["CGO_ENABLED"] != "0" {
		t.Errorf("failed step = %+v", fs)
	}
	if j.Runner.Image != "ubuntu-24.04" {
		t.Errorf("runner = %+v", j.Runner)
	}
	if j.Failure == nil || j.Failure.Category != joblog.CategoryTest || len(j.Failure.FailedTests) != 2 {
		t.Errorf("failure = %+v", j.Failure)
	}
	wantNotes := []string{
		"job uses service containers: postgres (postgres:16)",
		"failing step references secrets not available locally: CODECOV_TOKEN",
	}
	if !reflect.DeepEqual(j.Notes, wantNotes) {
		t.Errorf("notes = %q, want %q", j.Notes, wantNotes)
	}
}

func TestRunExplicitAttemptAndJob(t *testing.T) {
	api := newFake(t)
	r := ref
	r.Attempt, r.JobID = 1, 3
	rep, err := Run(context.Background(), api, r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.attempts, []int{1}) || len(rep.Jobs) != 1 || rep.Jobs[0].ID != 3 {
		t.Fatalf("attempts=%v jobs=%+v", api.attempts, rep.Jobs)
	}
	if !strings.Contains(strings.Join(rep.Jobs[0].Notes, "\n"), "job log unavailable") {
		t.Errorf("expected a note about the missing log, got %q", rep.Jobs[0].Notes)
	}

	r.JobID = 999
	if _, err := Run(context.Background(), api, r, Options{}); err == nil {
		t.Fatal("expected error for unknown job ID")
	}
}

func TestRunWithoutWorkflowFile(t *testing.T) {
	api := newFake(t)
	api.fileErr = errors.New("boom")
	rep, err := Run(context.Background(), api, ref, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "could not fetch") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
	j := rep.Jobs[0]
	if j.WorkflowJob != "" || j.FailedStep == nil || j.FailedStep.Name != "Run go test ./..." || j.Failure == nil {
		t.Errorf("log-only report should still identify the failure: %+v", j)
	}
}

func TestRunDynamicWorkflow(t *testing.T) {
	api := newFake(t)
	api.run.Path = "dynamic/pages/pages-build-deployment"
	rep, err := Run(context.Background(), api, ref, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "dynamic workflow") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestRunNoJobs(t *testing.T) {
	api := newFake(t)
	api.jobs = nil
	rep, err := Run(context.Background(), api, ref, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Jobs) != 0 || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "before any job started") {
		t.Errorf("report = %+v", rep)
	}
}

func TestRunOnlyCancelledJobs(t *testing.T) {
	api := newFake(t)
	api.jobs = api.jobs[1:2]
	rep, err := Run(context.Background(), api, ref, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Jobs) != 1 || rep.Jobs[0].Conclusion != "cancelled" {
		t.Errorf("expected the cancelled job when nothing failed, got %+v", rep.Jobs)
	}
}
