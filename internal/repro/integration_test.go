//go:build integration

package repro

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ri4dH/failtrace/internal/docker"
	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/plan"
	"github.com/Ri4dH/failtrace/internal/toolchain"
)

// TestIntegrationDocker runs a plan against a real Docker daemon using a
// local git remote and a tiny image. Run with: go test -tags integration ./internal/repro
func TestIntegrationDocker(t *testing.T) {
	eng := &docker.CLI{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := eng.Available(ctx); err != nil {
		t.Skip(err)
	}

	// A repository whose test script fails the way "CI" saw it fail.
	remotes := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Join(src, "service"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		`[ "$GREETING" = hello ] || { echo "GREETING not propagated: '$GREETING'"; exit 3; }` + "\n" +
		`command -v from-path >/dev/null || { echo "GITHUB_PATH not applied"; exit 4; }` + "\n" +
		`[ "$(pwd)" = /home/runner/work/widget/widget/service ] || { echo "wrong dir $(pwd)"; exit 5; }` + "\n" +
		"echo '--- FAIL: TestWidget (0.00s)'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(src, "service", "test.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(src, "init", "-q", "-b", "main")
	gitRun(src, "add", ".")
	gitRun(src, "commit", "-q", "-m", "widget")
	sha := gitRun(src, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(remotes, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(remotes, "clone", "-q", "--bare", src, filepath.Join(remotes, "acme", "widget.git"))

	p := &plan.Plan{
		Repository: "acme/widget",
		Job:        plan.JobRef{ID: 1, Name: "integration"},
		Verdict:    plan.VerdictReady,
		Image:      plan.Image{Ref: "alpine:3.20", Exact: true},
		Workspace:  "/home/runner/work/widget/widget",
		Commit:     sha,
		Env:        []plan.EnvVar{{Name: "CI", Value: "true", Source: "github"}},
		Steps: []plan.Step{
			{Name: "checkout", Kind: plan.KindCheckout, Checkout: &plan.Checkout{Repository: "acme/widget", Commit: sha}},
			{Name: "export", Kind: plan.KindRun, Shell: "sh -e {0}", Script: "echo GREETING=hello >> \"$GITHUB_ENV\"\n" +
				"mkdir -p /opt/bin && printf '#!/bin/sh\\n' > /opt/bin/from-path && chmod +x /opt/bin/from-path\n" +
				"echo /opt/bin >> \"$GITHUB_PATH\""},
			{Name: "test", Kind: plan.KindRun, Shell: "sh -e {0}", Script: "./test.sh", WorkingDirectory: "service", Target: true},
		},
	}
	var out bytes.Buffer
	res, err := Run(ctx, Deps{Engine: eng, Git: HostGit{}, Resolver: toolchain.NewResolver()}, p, ciFailure("TestWidget"),
		Options{GitBase: "file://" + remotes, WorkDir: t.TempDir(), Out: &out})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if res.Verdict != VerdictReproduced {
		t.Fatalf("verdict = %s %q\n%s", res.Verdict, res.Reasons, out.String())
	}
	if ps, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=failtrace-1-").Output(); len(bytes.TrimSpace(ps)) != 0 {
		t.Errorf("containers left behind: %s", ps)
	}
	t.Logf("output:\n%s", out.String())
}

// TestIntegrationToolchainAndService builds an environment image with Go from
// the actions manifest and checks a service container is reachable both as
// localhost and by name.
func TestIntegrationToolchainAndService(t *testing.T) {
	eng := &docker.CLI{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := eng.Available(ctx); err != nil {
		t.Skip(err)
	}
	platform, arch := "linux/amd64", "x64"
	if a := eng.ServerArch(ctx); a == "aarch64" || a == "arm64" {
		platform, arch = "linux/arm64", "arm64"
	}

	remotes := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	for _, args := range [][]string{{"init", "-q", "-b", "main", src}, {"-C", src, "commit", "-q", "--allow-empty", "-m", "empty"}} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sha, _ := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	_ = os.MkdirAll(filepath.Join(remotes, "acme"), 0o755)
	if out, err := exec.Command("git", "clone", "-q", "--bare", src, filepath.Join(remotes, "acme", "widget.git")).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	commit := strings.TrimSpace(string(sha))

	check := `go version | grep -q 'go1.27.4' || { echo "wrong go: $(go version)"; exit 3; }
[ -x /opt/hostedtoolcache/go/1.27.4/` + arch + `/bin/go ] || { echo "go not in the tool cache"; exit 4; }
python -c 'import ssl, sqlite3, sys; assert sys.version_info[:2] == (3, 12), sys.version' || { echo "python broken: $(command -v python)"; exit 7; }
case "$(command -v python)" in /opt/hostedtoolcache/Python/3.12.*) ;; *) echo "python not from the tool cache: $(command -v python)"; exit 8 ;; esac
pip --version >/dev/null || { echo "pip missing"; exit 9; }
for host in localhost redis; do
  exec 3<>/dev/tcp/$host/6379 || { echo "cannot reach redis at $host"; exit 5; }
  printf 'PING\r\n' >&3; read -r -t 5 reply <&3; exec 3>&-
  case "$reply" in +PONG*) ;; *) echo "bad reply from $host: $reply"; exit 6 ;; esac
done
echo 'FAIL	example.com/widget	0.01s'
exit 1`
	p := &plan.Plan{
		Repository: "acme/widget",
		Job:        plan.JobRef{ID: 2, Name: "integration-toolchain"},
		Verdict:    plan.VerdictReady,
		Image:      plan.Image{Ref: "ubuntu:24.04", Platform: platform},
		Workspace:  "/home/runner/work/widget/widget",
		Commit:     commit,
		Toolchains: []plan.Toolchain{{Tool: "go", Version: "1.27.4", Exact: true}, {Tool: "python", Version: "3.12"}},
		Services:   []plan.Service{{Name: "redis", Image: "redis:7-alpine", Options: `--health-cmd "redis-cli ping" --health-interval 1s --health-retries 30`}},
		Steps: []plan.Step{
			{Name: "checkout", Kind: plan.KindCheckout, Checkout: &plan.Checkout{Repository: "acme/widget", Commit: commit}},
			{Name: "setup-go", Kind: plan.KindToolchain},
			{Name: "test", Kind: plan.KindRun, Shell: "/usr/bin/bash -e {0}", Script: check, Target: true},
		},
	}
	code := 1
	var out bytes.Buffer
	res, err := Run(ctx, Deps{Engine: eng, Git: HostGit{}, Resolver: toolchain.NewResolver()}, p,
		&joblog.Failure{ExitCode: &code, Category: joblog.CategoryTest},
		Options{GitBase: "file://" + remotes, WorkDir: t.TempDir(), Out: &out})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if res.Verdict != VerdictReproduced || *res.LocalExitCode != 1 {
		t.Fatalf("verdict = %s %q exit=%v\n%s", res.Verdict, res.Reasons, res.LocalExitCode, out.String())
	}
	t.Logf("image %s\n%s", res.Image, out.String())
}

// TestIntegrationRunsAndShell checks flaky detection and the shell session
// against a real container. The "test" fails on every other attempt.
func TestIntegrationRunsAndShell(t *testing.T) {
	eng := &docker.CLI{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := eng.Available(ctx); err != nil {
		t.Skip(err)
	}
	remotes := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	for _, args := range [][]string{{"init", "-q", "-b", "main", src}, {"-C", src, "commit", "-q", "--allow-empty", "-m", "empty"}} {
		cmd := exec.Command("git", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sha, _ := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	_ = os.MkdirAll(filepath.Join(remotes, "acme"), 0o755)
	if out, err := exec.Command("git", "clone", "-q", "--bare", src, filepath.Join(remotes, "acme", "widget.git")).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	commit := strings.TrimSpace(string(sha))
	flaky := `n=$(cat /tmp/attempts 2>/dev/null || echo 0); n=$((n+1)); echo $n > /tmp/attempts
echo "attempt $n"
if [ $((n % 2)) -eq 0 ]; then echo '--- FAIL: TestFlaky (0.00s)'; exit 1; fi`
	p := &plan.Plan{
		Repository: "acme/widget", Job: plan.JobRef{ID: 3, Name: "integration-runs"}, Verdict: plan.VerdictReady,
		Image: plan.Image{Ref: "alpine:3.20", Exact: true}, Workspace: "/home/runner/work/widget/widget", Commit: commit,
		Steps: []plan.Step{
			{Name: "checkout", Kind: plan.KindCheckout, Checkout: &plan.Checkout{Repository: "acme/widget", Commit: commit}},
			{Name: "test", Kind: plan.KindRun, Shell: "sh -e {0}", Script: flaky, Env: map[string]string{"STEP_VAR": "from-step"}, Target: true},
		},
	}
	var out, term bytes.Buffer
	res, err := Run(ctx, Deps{Engine: eng, Git: HostGit{}, Resolver: toolchain.NewResolver()}, p, ciFailure("TestFlaky"),
		Options{
			GitBase: "file://" + remotes, WorkDir: t.TempDir(), Out: &out, Runs: 4, Shell: true,
			ShellIO: ShellIO{In: strings.NewReader("echo \"in:$PWD var:$STEP_VAR\"\nexit\n"), Out: &term},
		})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if res.Verdict != VerdictFlaky || res.Reasons[0] != "the step failed in 2 of 4 runs" {
		t.Fatalf("verdict = %s %q\n%s", res.Verdict, res.Reasons, out.String())
	}
	if !strings.Contains(term.String(), "in:/home/runner/work/widget/widget var:from-step") {
		t.Errorf("shell session output = %q", term.String())
	}
	t.Logf("output:\n%s\nshell:\n%s", out.String(), term.String())
}
