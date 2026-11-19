package repro

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Ri4dH/failtrace/internal/docker"
	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/plan"
	"github.com/Ri4dH/failtrace/internal/toolchain"
)

// fakeEngine records container operations. Exec looks up the behaviour of a
// step by the script it runs (step-<n>.sh).
type fakeEngine struct {
	mu       sync.Mutex
	runs     []docker.RunOptions
	execs    []docker.ExecOptions
	removed  []string
	built    []string
	exists   bool
	arch     string
	health   []string // successive Health answers
	steps    map[int]func(o docker.ExecOptions, hostRoot string) int
	hostRoot string
}

func (f *fakeEngine) Available(context.Context) error   { return nil }
func (f *fakeEngine) ServerArch(context.Context) string { return f.arch }
func (f *fakeEngine) ImageExists(context.Context, string) bool {
	return f.exists
}
func (f *fakeEngine) Build(_ context.Context, dockerfile, tag, _ string, _ io.Writer) error {
	f.built = append(f.built, tag+"\n"+dockerfile)
	return nil
}
func (f *fakeEngine) Run(_ context.Context, o docker.RunOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, o)
	if len(o.Mounts) > 0 {
		f.hostRoot = o.Mounts[0].Host
	}
	return fmt.Sprintf("ctr%d", len(f.runs)), nil
}
func (f *fakeEngine) Exec(_ context.Context, o docker.ExecOptions) (int, error) {
	f.mu.Lock()
	f.execs = append(f.execs, o)
	f.mu.Unlock()
	if o.Cmd[0] == "printenv" {
		fmt.Fprintln(o.Stdout, "/usr/bin:/bin")
		return 0, nil
	}
	for n, fn := range f.steps {
		if strings.HasSuffix(o.Cmd[len(o.Cmd)-1], fmt.Sprintf("step-%d.sh", n)) {
			return fn(o, f.hostRoot), nil
		}
	}
	return 0, nil
}
func (f *fakeEngine) Health(context.Context, string) (string, error) {
	if len(f.health) == 0 {
		return "none", nil
	}
	h := f.health[0]
	f.health = f.health[1:]
	return h, nil
}
func (f *fakeEngine) Logs(context.Context, string, int) string { return "service log" }
func (f *fakeEngine) Remove(_ context.Context, ids ...string) error {
	f.removed = append(f.removed, ids...)
	return nil
}

// hostPath maps a container path under /home/runner/work to the host.
func hostPath(hostRoot, containerPath string) string {
	return filepath.Join(hostRoot, strings.TrimPrefix(containerPath, "/home/runner/work"))
}

type fakeGit struct {
	calls   [][]string
	failFor string // fail a fetch whose target is this commit
}

func (g *fakeGit) Run(_ context.Context, _ string, env map[string]string, args ...string) (string, error) {
	g.calls = append(g.calls, args)
	if g.failFor != "" && contains(args, "fetch") && args[len(args)-1] == g.failFor {
		return "", errors.New("not our ref")
	}
	return "", nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, tool, version, _, _ string) (*toolchain.Install, error) {
	if tool == "java" {
		return nil, toolchain.ErrUnsupported
	}
	return &toolchain.Install{Tool: tool, Version: version, Script: "install " + tool, Path: []string{"/opt/hostedtoolcache/" + tool + "/" + version + "/x64/bin"}, Source: "test"}, nil
}

func samplePlan() *plan.Plan {
	return &plan.Plan{
		Repository: "acme/widget",
		Job:        plan.JobRef{ID: 7, Name: "test"},
		Verdict:    plan.VerdictPartial,
		Image:      plan.Image{Ref: "ubuntu:24.04", Platform: "linux/amd64"},
		Workspace:  "/home/runner/work/widget/widget",
		Commit:     "1111111111111111111111111111111111111111",
		Toolchains: []plan.Toolchain{{Tool: "go", Version: "1.22.7", Exact: true}, {Tool: "java", Version: "21"}},
		Services:   []plan.Service{{Name: "postgres", Image: "postgres:16", Options: `--health-cmd "pg_isready" -p 5432:5432`}},
		Env: []plan.EnvVar{
			{Name: "CI", Value: "true", Source: "github"},
			{Name: "API_TOKEN", Source: "job", Secret: true},
		},
		Secrets: []string{"API_TOKEN"},
		Steps: []plan.Step{
			{Name: "Run actions/checkout@v4", Kind: plan.KindCheckout, Checkout: &plan.Checkout{Repository: "acme/widget", Commit: "1111111111111111111111111111111111111111"}},
			{Name: "Run actions/setup-go@v5", Kind: plan.KindToolchain},
			{Name: "Run actions/cache@v4", Kind: plan.KindSkip, Note: "cache only"},
			{Name: "Prepare", Kind: plan.KindRun, Script: "make prepare", Shell: "/usr/bin/bash -e {0}"},
			{Name: "Integration tests", Kind: plan.KindRun, Script: "go test ./...", Shell: "/usr/bin/bash --noprofile --norc -e -o pipefail {0}",
				WorkingDirectory: "./service", Env: map[string]string{"DATABASE_URL": "postgres://localhost/test", "API_TOKEN": "***"}, Target: true},
		},
		Issues: []plan.Issue{{Severity: plan.SeverityWarning, Message: "the job needs service containers: postgres (postgres:16)"}},
	}
}

func ciFailure(tests ...string) *joblog.Failure {
	code := 1
	return &joblog.Failure{ExitCode: &code, FailedTests: tests, Category: joblog.CategoryTest}
}

func run(t *testing.T, eng *fakeEngine, git *fakeGit, p *plan.Plan, ci *joblog.Failure, opts Options) (*Result, string) {
	t.Helper()
	var out bytes.Buffer
	opts.Out, opts.WorkDir = &out, t.TempDir()
	res, err := Run(context.Background(), Deps{Engine: eng, Git: git, Resolver: fakeResolver{}}, p, ci, opts)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func TestRunReproduced(t *testing.T) {
	eng := &fakeEngine{arch: "aarch64", health: []string{"starting", "healthy"}, steps: map[int]func(docker.ExecOptions, string) int{
		4: func(o docker.ExecOptions, root string) int {
			// An earlier step exports env and PATH for later steps.
			_ = os.WriteFile(hostPath(root, o.Env["GITHUB_ENV"]), []byte("FOO=bar\nMULTI<<EOF\na\nb\nEOF\n"), 0o644)
			_ = os.WriteFile(hostPath(root, o.Env["GITHUB_PATH"]), []byte("/opt/extra/bin\n"), 0o644)
			return 0
		},
		5: func(o docker.ExecOptions, _ string) int {
			fmt.Fprintln(o.Stdout, "--- FAIL: TestIntegration (0.01s)")
			fmt.Fprintln(o.Stdout, "FAIL")
			return 1
		},
	}}
	git := &fakeGit{}
	t.Setenv("UNRELATED", "x")
	res, out := run(t, eng, git, samplePlan(), ciFailure("TestIntegration"), Options{Token: "tok", Secrets: map[string]string{"API_TOKEN": "s3cret"}})

	if res.Verdict != VerdictReproduced || !strings.Contains(res.Reasons[0], "TestIntegration") {
		t.Fatalf("verdict = %s %q\n%s", res.Verdict, res.Reasons, out)
	}
	if *res.LocalExitCode != 1 || res.Local.Category != joblog.CategoryTest {
		t.Errorf("local = %+v", res.Local)
	}

	// The image: built once with the resolvable toolchains.
	if len(eng.built) != 1 || !strings.Contains(eng.built[0], "install go") || !strings.Contains(eng.built[0], "FROM ubuntu:24.04") {
		t.Errorf("built = %q", eng.built)
	}
	if !strings.HasPrefix(res.Image, "failtrace-env:") {
		t.Errorf("image tag = %q", res.Image)
	}

	// The job container and the service sharing its network namespace.
	job, svc := eng.runs[0], eng.runs[1]
	if job.Workdir != "/home/runner/work/widget/widget" || job.Mounts[0].Container != "/home/runner/work" || job.Platform != "linux/amd64" ||
		!reflect.DeepEqual(job.AddHosts, []string{"postgres:127.0.0.1"}) {
		t.Errorf("job container = %+v", job)
	}
	if svc.Network != "container:ctr1" || !reflect.DeepEqual(svc.Extra, []string{"--health-cmd", "pg_isready"}) {
		t.Errorf("service container = %+v", svc)
	}

	// The checkout mirrors actions/checkout, authenticated via env, not argv.
	wantGit := [][]string{
		{"init", "-q"}, {"remote", "add", "origin", "https://github.com/acme/widget.git"},
		{"fetch", "-q", "--no-tags", "--prune", "--depth=1", "origin", "1111111111111111111111111111111111111111"},
		{"checkout", "-q", "--force", "--detach", "FETCH_HEAD"},
	}
	for i, want := range wantGit {
		if got := strings.Join(git.calls[i], " "); !strings.Contains(got, strings.Join(want, " ")) || strings.Contains(got, "tok") {
			t.Errorf("git call %d = %q, want it to contain %q", i, got, want)
		}
	}

	target := lastStepExec(eng, 5)
	wantArgv := []string{"bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "/home/runner/work/_temp/failtrace/step-5.sh"}
	if !reflect.DeepEqual(target.Cmd, wantArgv) || target.Workdir != "/home/runner/work/widget/widget/service" {
		t.Errorf("target exec = %q in %s", target.Cmd, target.Workdir)
	}
	env := target.Env
	checks := map[string]string{
		"FOO": "bar", "MULTI": "a\nb", "CI": "true", "API_TOKEN": "s3cret", "DATABASE_URL": "postgres://localhost/test",
		"HOME": "/home/runner", "PATH": "/opt/extra/bin:/usr/bin:/bin",
	}
	for k, want := range checks {
		if env[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, env[k], want)
		}
	}
	if _, leaked := env["UNRELATED"]; leaked {
		t.Error("host environment leaked into the step")
	}
	if !strings.Contains(out, "│ --- FAIL: TestIntegration") {
		t.Errorf("target output should be streamed:\n%s", out)
	}

	statuses := map[string]StepStatus{}
	for _, s := range res.Steps {
		statuses[s.Name] = s.Status
	}
	if statuses["Run actions/cache@v4"] != StepSkipped || statuses["Prepare"] != StepPassed || statuses["Integration tests"] != StepFailed {
		t.Errorf("steps = %+v", res.Steps)
	}

	diffs := strings.Join(res.Differences, "\n")
	for _, want := range []string{"service containers", "not installed: java 21", "run as root", "linux/amd64 is emulated on this aarch64 machine"} {
		if !strings.Contains(diffs, want) {
			t.Errorf("differences missing %q:\n%s", want, diffs)
		}
	}
	if strings.Contains(diffs, "API_TOKEN was not provided") {
		t.Error("the secret was provided")
	}
	if len(eng.removed) != 2 {
		t.Errorf("removed = %q, want job and service containers", eng.removed)
	}
	if entries, _ := os.ReadDir(filepath.Dir(eng.hostRoot)); len(entries) != 0 {
		t.Errorf("workspace left behind: %v", entries)
	}
}

func lastStepExec(eng *fakeEngine, n int) docker.ExecOptions {
	for i := len(eng.execs) - 1; i >= 0; i-- {
		if strings.HasSuffix(eng.execs[i].Cmd[len(eng.execs[i].Cmd)-1], fmt.Sprintf("step-%d.sh", n)) {
			return eng.execs[i]
		}
	}
	return docker.ExecOptions{}
}

func TestRunNotReproduced(t *testing.T) {
	eng := &fakeEngine{exists: true}
	res, out := run(t, eng, &fakeGit{}, samplePlan(), ciFailure("TestIntegration"), Options{})
	if res.Verdict != VerdictNotReproduced {
		t.Fatalf("verdict = %s", res.Verdict)
	}
	if len(eng.built) != 0 || !strings.Contains(out, "(cached)") {
		t.Errorf("a cached image should not be rebuilt: %q\n%s", eng.built, out)
	}
	if !strings.Contains(strings.Join(res.Differences, "\n"), "secret API_TOKEN was not provided") {
		t.Errorf("differences = %q", res.Differences)
	}
}

func TestRunDiverged(t *testing.T) {
	eng := &fakeEngine{steps: map[int]func(docker.ExecOptions, string) int{
		4: func(o docker.ExecOptions, _ string) int {
			fmt.Fprintln(o.Stdout, "make: *** No rule to make target")
			return 2
		},
	}}
	res, out := run(t, eng, &fakeGit{}, samplePlan(), ciFailure(), Options{})
	if res.Verdict != VerdictDiverged || !strings.Contains(res.Reasons[0], "step 4 (Prepare) failed locally with exit code 2") {
		t.Fatalf("verdict = %s %q", res.Verdict, res.Reasons)
	}
	last := res.Steps[len(res.Steps)-1]
	if last.Status != StepNotRun || !last.Target {
		t.Errorf("the target should be marked not-run: %+v", last)
	}
	if !strings.Contains(out, "│ make: *** No rule") {
		t.Errorf("a diverging step's output should be shown:\n%s", out)
	}
}

func TestRunPlatformOverrideIsADifference(t *testing.T) {
	res, _ := run(t, &fakeEngine{arch: "aarch64"}, &fakeGit{}, samplePlan(), ciFailure(), Options{Platform: "linux/arm64"})
	diffs := strings.Join(res.Differences, "\n")
	if !strings.Contains(diffs, "running linux/arm64, but CI ran linux/amd64") || strings.Contains(diffs, "emulated") {
		t.Errorf("differences = %s", diffs)
	}
	if res.Platform != "linux/arm64" {
		t.Errorf("platform = %s", res.Platform)
	}
}

func TestRunKeep(t *testing.T) {
	eng := &fakeEngine{}
	res, _ := run(t, eng, &fakeGit{}, samplePlan(), ciFailure(), Options{Keep: true})
	if len(eng.removed) != 0 || res.Container == "" || res.Workspace == "" {
		t.Fatalf("keep: removed=%q result=%+v", eng.removed, res)
	}
	if _, err := os.Stat(res.Workspace); err != nil {
		t.Errorf("kept workspace missing: %v", err)
	}
}

func TestRunRecreatesMergeCommit(t *testing.T) {
	p := samplePlan()
	p.Steps[0].Checkout.MergeHead = strings.Repeat("2", 40)
	p.Steps[0].Checkout.MergeBase = strings.Repeat("3", 40)
	git := &fakeGit{failFor: p.Steps[0].Checkout.Commit}
	res, _ := run(t, &fakeEngine{}, git, p, ciFailure(), Options{})
	var merged bool
	for _, c := range git.calls {
		if contains(c, "merge") && c[len(c)-1] == strings.Repeat("2", 40) {
			merged = true
		}
	}
	if !merged || !strings.Contains(strings.Join(res.Differences, "\n"), "was recreated locally") {
		t.Errorf("merge not recreated: calls=%q differences=%q", git.calls, res.Differences)
	}
}

func TestRunBlocked(t *testing.T) {
	p := samplePlan()
	p.Verdict = plan.VerdictBlocked
	p.Issues = append(p.Issues, plan.Issue{Severity: plan.SeverityBlocker, Message: "the failing step runs the action x/y"})
	_, err := Run(context.Background(), Deps{Engine: &fakeEngine{}}, p, nil, Options{})
	if !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "x/y") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunUnhealthyService(t *testing.T) {
	eng := &fakeEngine{health: []string{"unhealthy"}}
	_, err := Run(context.Background(), Deps{Engine: eng, Git: &fakeGit{}, Resolver: fakeResolver{}}, samplePlan(), nil, Options{WorkDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "service postgres is unhealthy") || !strings.Contains(err.Error(), "service log") {
		t.Fatalf("err = %v", err)
	}
	if len(eng.removed) != 2 {
		t.Errorf("containers should be cleaned up after an error: %q", eng.removed)
	}
}

func TestRunRejectsCheckoutOutsideWorkspace(t *testing.T) {
	p := samplePlan()
	p.Steps[0].Checkout.Path = "../../etc"
	_, err := Run(context.Background(), Deps{Engine: &fakeEngine{}, Git: &fakeGit{}, Resolver: fakeResolver{}}, p, nil, Options{WorkDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "escapes the workspace") {
		t.Fatalf("err = %v", err)
	}
}
