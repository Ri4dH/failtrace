package plan

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/github"
	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/workflow"
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var fixtureAPISteps = []github.Step{
	{Number: 1, Name: "Set up job", Conclusion: "success"},
	{Number: 2, Name: "Initialize containers", Conclusion: "success"},
	{Number: 3, Name: "Run actions/checkout@v4", Conclusion: "success"},
	{Number: 4, Name: "Run actions/setup-go@v5", Conclusion: "success"},
	{Number: 5, Name: "Run actions/cache@v4", Conclusion: "success"},
	{Number: 6, Name: "Run ./.github/actions/setup-tools", Conclusion: "success"},
	{Number: 7, Name: "Run actions/download-artifact@v4", Conclusion: "success"},
	{Number: 8, Name: "Unit tests", Conclusion: "skipped"},
	{Number: 9, Name: "Integration tests", Conclusion: "failure"},
}

// fixtureInput builds plan input from the testdata log and workflow, the way
// inspect would. Pass an empty workflow to simulate a log-only inspection.
func fixtureInput(t *testing.T, logText, wfText string, labels []string) Input {
	t.Helper()
	lg := joblog.Parse(logText)
	ev := &inspect.Evidence{Log: lg, Failed: lg.FailedSection(), API: github.Job{ID: 99, Steps: fixtureAPISteps}}
	job := inspect.JobReport{ID: 99, Name: "test (1.22)", RunsOn: labels, Evidence: ev}
	if wfText != "" {
		wf, err := workflow.Parse([]byte(wfText))
		if err != nil {
			t.Fatal(err)
		}
		m := wf.MatchJob(job.Name)
		if m == nil {
			t.Fatal("fixture job did not match")
		}
		ev.Workflow, ev.Job = wf, m.Job
		job.WorkflowJob, job.Matrix = m.Job.ID, m.Matrix
	}
	return Input{
		Owner: "acme", Repo: "widget", Job: job,
		Run: inspect.RunInfo{ID: 42, Attempt: 1, Workflow: "CI", Event: "pull_request", SHA: "2222222222222222222222222222222222222222"},
	}
}

func build(t *testing.T, in Input, files FileSource) *Plan {
	t.Helper()
	p, err := Build(context.Background(), in, files)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildFullPlan(t *testing.T) {
	p := build(t, fixtureInput(t, readFile(t, "job.log"), readFile(t, "workflow.yml"), []string{"ubuntu-24.04"}), nil)

	if p.Image != (Image{Ref: "ubuntu:24.04", Platform: "linux/amd64", Source: "matches the runner image ubuntu-24.04 20261111.1"}) {
		t.Errorf("image = %+v", p.Image)
	}
	if p.Workspace != "/home/runner/work/widget/widget" {
		t.Errorf("workspace = %q", p.Workspace)
	}
	if p.Commit != strings.Repeat("1", 40) {
		t.Errorf("commit = %q, want the merge commit CI checked out", p.Commit)
	}

	type row struct {
		kind   Kind
		number int
		parent int
		target bool
	}
	want := []row{
		{KindCheckout, 3, 0, false},
		{KindToolchain, 4, 0, false},
		{KindSkip, 5, 0, false},
		{KindComposite, 6, 0, false},
		{KindToolchain, 0, 4, false}, // setup-uv inside the composite action
		{KindRun, 0, 4, false},       // make tools inside the composite action
		{KindSkip, 7, 0, false},      // download-artifact
		{KindRun, 9, 0, true},        // not step 8, the skipped look-alike
	}
	var got []row
	for _, s := range p.Steps {
		got = append(got, row{s.Kind, s.Number, s.Parent, s.Target})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps =\n%+v\nwant\n%+v", got, want)
	}

	co := p.Steps[0].Checkout
	if co.Repository != "acme/widget" || co.Ref != "pull/7/merge" || co.MergeHead != strings.Repeat("2", 40) || co.FetchDepth != "1" || co.Submodules != "" {
		t.Errorf("checkout = %+v", co)
	}

	wantTC := []Toolchain{
		{Tool: "go", Version: "1.22.7", Spec: "1.22", Exact: true, Source: "actions/setup-go@v5"},
		{Tool: "uv", Version: "0.5.1", Exact: true, Source: "astral-sh/setup-uv@v5"},
	}
	if !reflect.DeepEqual(p.Toolchains, wantTC) {
		t.Errorf("toolchains = %+v", p.Toolchains)
	}

	target := p.Steps[7]
	if target.Name != "Integration tests" || target.Script != "go test -run Integration ./...\ngo vet ./..." ||
		target.Shell != "/usr/bin/bash -e {0}" || target.WorkingDirectory != "./service" {
		t.Errorf("target = %+v", target)
	}
	// Job-wide values and setup-action paths are not repeated per step.
	wantEnv := map[string]string{"DATABASE_URL": "postgres://postgres@localhost:5432/test", "EXTRA": "from-github-env"}
	if !reflect.DeepEqual(target.Env, wantEnv) {
		t.Errorf("target env = %v, want %v", target.Env, wantEnv)
	}

	env := map[string]EnvVar{}
	for _, e := range p.Env {
		env[e.Name] = e
	}
	checks := map[string]EnvVar{
		"GOFLAGS":    {Name: "GOFLAGS", Value: "-mod=readonly", Source: "workflow"},
		"API_TOKEN":  {Name: "API_TOKEN", Source: "job", Secret: true},
		"BUILD_ID":   {Name: "BUILD_ID", Value: "42-1.22", Source: "job"},
		"GITHUB_SHA": {Name: "GITHUB_SHA", Value: strings.Repeat("1", 40), Source: "github"},
		"CI":         {Name: "CI", Value: "true", Source: "github"},
	}
	for name, want := range checks {
		if env[name] != want {
			t.Errorf("env[%s] = %+v, want %+v", name, env[name], want)
		}
	}
	if !reflect.DeepEqual(p.Secrets, []string{"API_TOKEN"}) {
		t.Errorf("secrets = %q", p.Secrets)
	}
	if len(p.Services) != 1 || p.Services[0].Image != "postgres:16" {
		t.Errorf("services = %+v", p.Services)
	}

	if p.Verdict != VerdictPartial {
		t.Errorf("verdict = %q", p.Verdict)
	}
	msgs := issueText(p)
	for _, want := range []string{
		"warning: actions/download-artifact@v4 downloads artifacts",
		"warning: the job needs service containers: postgres (postgres:16)",
		"warning: needs secrets that are not available locally: API_TOKEN",
		"warning: could not evaluate expressions: `vars.REGION`",
		"info: CI tested the merge of PR head 2222222 into base 3333333 (merge commit 1111111)",
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("issues missing %q:\n%s", want, msgs)
		}
	}
	if strings.Contains(msgs, "without a setup step") {
		t.Errorf("go is set up, so it should not be reported as missing:\n%s", msgs)
	}
}

func issueText(p *Plan) string {
	var b strings.Builder
	for _, is := range p.Issues {
		b.WriteString(string(is.Severity) + ": " + is.Message + "\n")
	}
	return b.String()
}

func TestBuildLogOnly(t *testing.T) {
	p := build(t, fixtureInput(t, readFile(t, "job.log"), "", []string{"ubuntu-24.04"}), nil)
	if len(p.Steps) != 8 {
		t.Fatalf("got %d steps, want every executed section", len(p.Steps))
	}
	for _, s := range p.Steps {
		if s.Parent != 0 || s.Number != 0 {
			t.Errorf("without a workflow there is no nesting or numbering: %+v", s)
		}
	}
	if last := p.Steps[7]; !last.Target || last.Name != "Run go test -run Integration ./..." {
		t.Errorf("target = %+v", last)
	}
}

func TestBuildContainerJob(t *testing.T) {
	wf := strings.Replace(readFile(t, "workflow.yml"), "    runs-on: ubuntu-24.04\n",
		"    runs-on: ubuntu-24.04\n    container: golang:${{ matrix.go }}-bookworm\n", 1)
	p := build(t, fixtureInput(t, readFile(t, "job.log"), wf, []string{"ubuntu-24.04"}), nil)
	if p.Image.Ref != "golang:1.22-bookworm" || !p.Image.Exact || p.Workspace != "/__w/widget/widget" {
		t.Errorf("image = %+v workspace = %q", p.Image, p.Workspace)
	}
}

func TestBuildWindowsRunner(t *testing.T) {
	p := build(t, fixtureInput(t, readFile(t, "job.log"), readFile(t, "workflow.yml"), []string{"windows-latest"}), nil)
	if p.Image.Ref != "ubuntu:latest" || !strings.Contains(issueText(p), "only approximates it") {
		t.Errorf("image = %+v issues:\n%s", p.Image, issueText(p))
	}
}

func TestBuildBlockedWhenFailingStepIsAnAction(t *testing.T) {
	log := strings.Replace(readFile(t, "job.log"), "--- FAIL: TestIntegration (0.00s)\n", "", 1)
	// Make the download step fail instead of the tests.
	log = strings.Replace(log, "  name: dist\n2026-11-17T10:00:06.0000000Z ##[endgroup]\n",
		"  name: dist\n2026-11-17T10:00:06.0000000Z ##[endgroup]\n2026-11-17T10:00:06.0000000Z ##[error]Unable to find any artifacts\n", 1)
	log = strings.Replace(log, "##[error]Process completed with exit code 1.\n", "", 1)
	p := build(t, fixtureInput(t, log, readFile(t, "workflow.yml"), []string{"ubuntu-24.04"}), nil)
	if p.Verdict != VerdictBlocked || !p.Steps[len(p.Steps)-1].Target {
		t.Fatalf("verdict = %q, steps = %+v", p.Verdict, p.Steps)
	}
	if !strings.Contains(issueText(p), "blocker: the failing step runs the action actions/download-artifact@v4") {
		t.Errorf("issues:\n%s", issueText(p))
	}
}

func TestBuildRequiresLog(t *testing.T) {
	_, err := Build(context.Background(), Input{Job: inspect.JobReport{Evidence: &inspect.Evidence{}}}, nil)
	if !errors.Is(err, ErrNoLog) {
		t.Fatalf("err = %v, want ErrNoLog", err)
	}
	_, err = Build(context.Background(), Input{}, nil)
	if !errors.Is(err, ErrNoLog) {
		t.Fatalf("err = %v, want ErrNoLog", err)
	}
}

type fakeFiles map[string]string

func (f fakeFiles) GetFile(_ context.Context, _, _, path, _ string) ([]byte, error) {
	if s, ok := f[path]; ok {
		return []byte(s), nil
	}
	return nil, errors.New("not found")
}

func TestToolchainFromVersionFile(t *testing.T) {
	log := strings.Replace(readFile(t, "job.log"), "  go-version: 1.22\n", "  go-version-file: ./go.mod\n", 1)
	log = strings.Replace(log, "Successfully cached go to /opt/hostedtoolcache/go/1.22.7/x64\n", "\n", 1)
	files := fakeFiles{"go.mod": "module x\n\ngo 1.23.0\n\ntoolchain go1.23.4\n"}
	p := build(t, fixtureInput(t, log, readFile(t, "workflow.yml"), []string{"ubuntu-24.04"}), files)
	want := Toolchain{Tool: "go", Version: "1.23.4", Spec: "./go.mod", Source: "actions/setup-go@v5"}
	if p.Toolchains[0] != want {
		t.Errorf("toolchain = %+v, want %+v", p.Toolchains[0], want)
	}

	p = build(t, fixtureInput(t, log, readFile(t, "workflow.yml"), []string{"ubuntu-24.04"}), nil)
	if p.Toolchains[0].Version != "" || !strings.Contains(issueText(p), "could not determine which go version") {
		t.Errorf("without file access the version is unknown: %+v\n%s", p.Toolchains[0], issueText(p))
	}
}

func TestParseVersionFile(t *testing.T) {
	cases := []struct {
		file, content, tool, want string
	}{
		{"go.mod", "module x\ngo 1.22.5\n", "go", "1.22.5"},
		{"go.mod", "module x\ngo 1.22\ntoolchain go1.23.1\n", "go", "1.23.1"},
		{".nvmrc", "v20.11.1\n", "node", "20.11.1"},
		{".python-version", "# pinned\n3.12\n", "python", "3.12"},
		{".tool-versions", "golang 1.22.1\nnodejs 20.1.0\n", "node", "20.1.0"},
		{"package.json", `{"packageManager": "pnpm@9.12.0+sha512.abc"}`, "pnpm", "9.12.0"},
		{"package.json", `{"name": "x"}`, "pnpm", ""},
	}
	for _, c := range cases {
		if got := parseVersionFile(c.file, []byte(c.content), c.tool); got != c.want {
			t.Errorf("parseVersionFile(%s, %q) = %q, want %q", c.file, c.content, got, c.want)
		}
	}
}

func TestLookupAction(t *testing.T) {
	cases := map[string]actionKind{
		"actions/checkout@v4":                 actionCheckout,
		"Actions/Setup-Go@v5":                 actionSetup,
		"actions/cache/restore@v4":            actionSkip,
		"actions/download-artifact@v4":        actionDownload,
		"golangci/golangci-lint-action@v6":    actionUnknown,
		"actions/cache-something-else@v1":     actionUnknown,
		"dtolnay/rust-toolchain@1.80":         actionSetup,
		"step-security/harden-runner@abc1234": actionSkip,
	}
	for uses, want := range cases {
		if got := lookupAction(uses).kind; got != want {
			t.Errorf("lookupAction(%q) = %v, want %v", uses, got, want)
		}
	}
}

func TestRustToolchainFromRef(t *testing.T) {
	sec := &joblog.Section{Title: "Run dtolnay/rust-toolchain@1.80"}
	b := &builder{plan: &Plan{}}
	tc := b.toolchain(lookupAction("dtolnay/rust-toolchain@1.80").setup, "dtolnay/rust-toolchain@1.80", sec)
	if tc.Version != "1.80" || tc.Spec != "1.80" {
		t.Errorf("toolchain = %+v", tc)
	}
}
