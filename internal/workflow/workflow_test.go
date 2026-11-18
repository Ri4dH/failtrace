package workflow

import (
	"os"
	"reflect"
	"testing"
)

func loadCI(t *testing.T) *Workflow {
	t.Helper()
	data, err := os.ReadFile("testdata/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

func TestParse(t *testing.T) {
	wf := loadCI(t)
	if wf.Name != "CI" || wf.Env["GOFLAGS"] != "-mod=readonly" || wf.Defaults.Shell != "bash" {
		t.Fatalf("workflow header = %+v", wf)
	}
	var ids []string
	for _, j := range wf.Jobs {
		ids = append(ids, j.ID)
	}
	if !reflect.DeepEqual(ids, []string{"test", "lint", "build", "deploy"}) {
		t.Fatalf("job order = %q", ids)
	}

	test := wf.Jobs[0]
	if !reflect.DeepEqual(test.Matrix.Keys, []string{"os", "go"}) {
		t.Errorf("matrix keys = %q", test.Matrix.Keys)
	}
	if !reflect.DeepEqual(test.Matrix.Values["go"], []string{"1.22", "1.23"}) {
		t.Errorf("matrix go = %q", test.Matrix.Values["go"])
	}
	if len(test.Services) != 1 || test.Services[0] != (Service{Name: "postgres", Image: "postgres:16"}) {
		t.Errorf("services = %+v", test.Services)
	}
	if got := test.Steps[3].With["fail_ci_if_error"]; got != "true" {
		t.Errorf("boolean with value = %q, want \"true\"", got)
	}

	lint := wf.Jobs[1]
	if lint.Container != "golang:1.23" || !lint.Matrix.HasInclude {
		t.Errorf("lint = %+v", lint)
	}
	if !reflect.DeepEqual(lint.Matrix.Values["tool"], []string{"golangci", "gofmt"}) {
		t.Errorf("include-only matrix values = %q", lint.Matrix.Values["tool"])
	}

	build := wf.Jobs[2]
	if !reflect.DeepEqual(build.RunsOn, []string{"self-hosted", "linux"}) || build.Env != nil {
		t.Errorf("build = %+v", build)
	}
	if wf.Jobs[3].Uses != "./.github/workflows/deploy.yml" {
		t.Errorf("deploy uses = %q", wf.Jobs[3].Uses)
	}
}

func TestParseRejectsMissingJobs(t *testing.T) {
	if _, err := Parse([]byte("name: x\non: push\n")); err == nil {
		t.Fatal("expected error for workflow without jobs")
	}
	if _, err := Parse([]byte("jobs: [")); err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestMatchJob(t *testing.T) {
	wf := loadCI(t)
	cases := []struct {
		name    string
		wantJob string
		matrix  map[string]string
	}{
		{"test (ubuntu-latest, 1.22)", "test", map[string]string{"os": "ubuntu-latest", "go": "1.22"}},
		{"Lint golangci", "lint", map[string]string{"tool": "golangci"}},
		{"build", "build", nil},
		{"deploy / publish", "deploy", nil},
	}
	for _, c := range cases {
		m := wf.MatchJob(c.name)
		if m == nil {
			t.Errorf("MatchJob(%q) = nil", c.name)
			continue
		}
		if m.Job.ID != c.wantJob || !reflect.DeepEqual(m.Matrix, c.matrix) || m.Guessed {
			t.Errorf("MatchJob(%q) = %s %v guessed=%v, want %s %v", c.name, m.Job.ID, m.Matrix, m.Guessed, c.wantJob, c.matrix)
		}
	}
	if m := wf.MatchJob("does-not-exist"); m != nil {
		t.Errorf("expected no match, got %s", m.Job.ID)
	}
}

func TestMatchJobUnmappedMatrixValues(t *testing.T) {
	wf := loadCI(t)
	m := wf.MatchJob("test (ubuntu-latest, 1.22, extra)")
	if m == nil || m.Job.ID != "test" || m.Matrix != nil {
		t.Fatalf("got %+v", m)
	}
	if !reflect.DeepEqual(m.MatrixRaw, []string{"ubuntu-latest", "1.22", "extra"}) {
		t.Errorf("matrix raw = %q", m.MatrixRaw)
	}
}

func TestMatchJobSingleJobFallback(t *testing.T) {
	wf, err := Parse([]byte("jobs:\n  only:\n    name: Something ${{ inputs.x }}\n    runs-on: ubuntu-latest\n    steps: [{run: echo}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := wf.MatchJob("totally different")
	if m == nil || m.Job.ID != "only" || !m.Guessed {
		t.Fatalf("got %+v", m)
	}
}

func TestStepDisplayName(t *testing.T) {
	cases := map[string]Step{
		"Set up Go":               {Name: "Set up Go", Uses: "actions/setup-go@v5"},
		"Run actions/checkout@v4": {Uses: "actions/checkout@v4"},
		"Run go test ./...":       {Run: "  go test ./...\ngo vet ./...\n"},
		"Run ${{ matrix.tool }}":  {Name: "Run ${{ matrix.tool }}"},
	}
	for want, s := range cases {
		if got := s.DisplayName(); got != want {
			t.Errorf("DisplayName(%+v) = %q, want %q", s, got, want)
		}
	}
}

func TestMatchStep(t *testing.T) {
	wf := loadCI(t)
	test, lint := wf.Jobs[0], wf.Jobs[1]
	apiSteps := []string{"Set up job", "Initialize containers", "Run actions/checkout@v4", "Set up Go", "Run go test ./...", "Upload coverage"}

	cases := []struct {
		job    *Job
		name   string
		number int
		want   int
	}{
		{test, "Set up Go", 4, 1},
		{test, "Run go test ./...", 5, 2},
		{test, "Renamed in a later commit", 6, 3}, // falls back to position
		{lint, "Run golangci", 3, 1},              // templated name
		{test, "Post Run actions/checkout@v4", 20, -1},
	}
	for _, c := range cases {
		if got := c.job.MatchStep(c.name, c.number, apiSteps); got != c.want {
			t.Errorf("MatchStep(%q, %d) = %d, want %d", c.name, c.number, got, c.want)
		}
	}
}
