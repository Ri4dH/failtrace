package repro

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ri4dH/failtrace/internal/docker"
	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/plan"
)

// ErrBlocked is returned for plans whose failing step cannot be replayed.
var ErrBlocked = errors.New("this failure cannot be reproduced yet")

// ErrShell is returned, together with a complete Result, when the
// reproduction finished but the --shell session could not be opened.
var ErrShell = errors.New("could not open the shell")

// Options configures a reproduction.
type Options struct {
	Host       string            // git host, default github.com
	GitBase    string            // clone URL prefix, default https://<Host>; tests use file:// remotes
	Token      string            // used to fetch private repositories
	Secrets    map[string]string // values for secrets the plan needs
	Toolchains map[string]string // tool → version overrides
	Platform   string            // override the plan's platform
	WorkDir    string            // host directory for workspaces
	Keep       bool              // keep containers and workspace afterwards
	Verbose    bool              // stream every step's output, not just the failing one
	Runs       int               // run the failing step this many times (flaky detection)
	Shell      bool              // open a shell in the container after the run
	ShellIO    ShellIO           // the terminal for Shell
	Out        io.Writer         // progress and step output
}

// StepStatus is what happened to a plan step locally.
type StepStatus string

const (
	StepPassed  StepStatus = "passed"
	StepFailed  StepStatus = "failed"
	StepSkipped StepStatus = "skipped"
	StepNotRun  StepStatus = "not-run"
)

// StepResult is one step's local outcome.
type StepResult struct {
	Index    int        `json:"index"`
	Name     string     `json:"name"`
	Kind     plan.Kind  `json:"kind"`
	Status   StepStatus `json:"status"`
	ExitCode *int       `json:"exit_code,omitempty"`
	Seconds  float64    `json:"seconds,omitempty"`
	Target   bool       `json:"target,omitempty"`
	Note     string     `json:"note,omitempty"`
	Output   []string   `json:"output_tail,omitempty"` // last lines, for a step that diverged
}

// Result is the outcome of a reproduction.
type Result struct {
	Verdict       Verdict         `json:"verdict"`
	Reasons       []string        `json:"reasons"`
	CI            *joblog.Failure `json:"ci,omitempty"`
	Local         *joblog.Failure `json:"local,omitempty"`
	LocalExitCode *int            `json:"local_exit_code,omitempty"`
	Steps         []StepResult    `json:"steps"`
	Image         string          `json:"image"`
	Platform      string          `json:"platform"`
	Differences   []string        `json:"differences,omitempty"`
	Container     string          `json:"container,omitempty"` // set when kept
	Workspace     string          `json:"workspace,omitempty"` // host path, set when kept
	Attempts      []Attempt       `json:"attempts,omitempty"`
	Seconds       float64         `json:"seconds"`
}

type runner struct {
	deps Deps
	plan *plan.Plan
	opts Options
	out  io.Writer

	name          string
	hostBase      string
	hostRoot      string // mounted at containerRoot
	hostWorkspace string
	hostTemp      string
	containerRoot string
	home          string

	container  string
	services   []string
	basePath   string
	githubEnv  map[string]string
	githubPath []string
	shellStep  plan.Step // the last run step, where --shell opens
	shellIndex int

	differences []string
}

// Run reproduces p. ci is the failure CI reported for the target step.
func Run(ctx context.Context, deps Deps, p *plan.Plan, ci *joblog.Failure, opts Options) (*Result, error) {
	if p.Verdict == plan.VerdictBlocked {
		for _, is := range p.Issues {
			if is.Severity == plan.SeverityBlocker {
				return nil, fmt.Errorf("%w: %s", ErrBlocked, is.Message)
			}
		}
		return nil, ErrBlocked
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Runs > MaxRuns {
		return nil, fmt.Errorf("at most %d runs are supported", MaxRuns)
	}
	if opts.Host == "" {
		opts.Host = "github.com"
	}
	if opts.GitBase == "" {
		opts.GitBase = "https://" + opts.Host
	}
	if err := deps.Engine.Available(ctx); err != nil {
		return nil, err
	}
	start := time.Now()
	r, err := newRunner(deps, p, opts)
	if err != nil {
		return nil, err
	}
	platform := firstNonEmpty(opts.Platform, p.Image.Platform)
	res := &Result{CI: ci, Platform: platform}
	r.noteDifferences(ctx, platform)

	img, err := planImage(ctx, deps.Resolver, p, platform, opts.Toolchains)
	if err != nil {
		return nil, err
	}
	res.Image = img.Tag
	for _, m := range img.Missing {
		r.differences = append(r.differences, "not installed: "+m)
	}
	if err := r.ensureImage(ctx, img, platform); err != nil {
		return nil, err
	}

	if err := r.makeDirs(); err != nil {
		return nil, err
	}
	defer r.cleanup(ctx, res)
	r.logf("Workspace", "%s @ %s", p.Repository, short(p.Commit))
	if err := r.checkoutAll(ctx); err != nil {
		return nil, err
	}
	if err := r.startContainers(ctx, img.Tag, platform); err != nil {
		return nil, err
	}
	if err := r.runSteps(ctx, res); err != nil {
		return nil, err
	}
	res.Differences = r.differences
	res.Seconds = time.Since(start).Seconds()
	if opts.Shell {
		if err := r.openShell(ctx); err != nil {
			return res, fmt.Errorf("%w: %v", ErrShell, err)
		}
	}
	return res, nil
}

func newRunner(deps Deps, p *plan.Plan, opts Options) (*runner, error) {
	root := path.Dir(path.Dir(p.Workspace))
	rel := strings.TrimPrefix(p.Workspace, root+"/")
	if root == "." || root == "/" || rel == p.Workspace || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return nil, fmt.Errorf("unexpected workspace path %q", p.Workspace)
	}
	if opts.WorkDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		opts.WorkDir = filepath.Join(dir, "failtrace", "work")
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	name := fmt.Sprintf("failtrace-%d-%s", p.Job.ID, hex.EncodeToString(suffix))
	r := &runner{
		deps: deps, plan: p, opts: opts, out: opts.Out, name: name,
		hostBase: filepath.Join(opts.WorkDir, name), containerRoot: root,
		githubEnv: map[string]string{}, home: "/home/runner",
	}
	if root == "/__w" {
		r.home = "/github/home"
	}
	r.hostRoot = filepath.Join(r.hostBase, "work")
	r.hostWorkspace = filepath.Join(r.hostRoot, filepath.FromSlash(rel))
	r.hostTemp = filepath.Join(r.hostRoot, "_temp")
	return r, nil
}

func (r *runner) logf(label, format string, args ...any) {
	fmt.Fprintf(r.out, "▶ %-11s %s\n", label, fmt.Sprintf(format, args...))
}

func (r *runner) noteDifferences(ctx context.Context, platform string) {
	p := r.plan
	for _, is := range p.Issues {
		if is.Severity == plan.SeverityWarning && !strings.HasPrefix(is.Message, "needs secrets") {
			r.differences = append(r.differences, is.Message)
		}
	}
	for _, name := range p.Secrets {
		if _, ok := r.opts.Secrets[name]; !ok {
			r.differences = append(r.differences, fmt.Sprintf("secret %s was not provided (pass --secret %s)", name, name))
		}
	}
	if !p.Image.Exact {
		r.differences = append(r.differences,
			fmt.Sprintf("the image is plain %s with common runner packages; GitHub's runner image has far more preinstalled", p.Image.Ref),
			"steps run as root; on GitHub's runners they run as the `runner` user with passwordless sudo")
	}
	if r.opts.Runs > 1 {
		r.differences = append(r.differences, "with --runs, Go's test cache is bypassed for the failing step (GOFLAGS="+noTestCache+") so every run really executes the tests")
	}
	if platform != p.Image.Platform && p.Image.Platform != "" {
		r.differences = append(r.differences, fmt.Sprintf("running %s, but CI ran %s", platform, p.Image.Platform))
	}
	arch := r.deps.Engine.ServerArch(ctx)
	native := map[string]string{"aarch64": "linux/arm64", "arm64": "linux/arm64", "x86_64": "linux/amd64", "amd64": "linux/amd64"}[arch]
	if native != "" && native != platform {
		r.differences = append(r.differences, fmt.Sprintf("%s is emulated on this %s machine, which is slower and occasionally behaves differently", platform, arch))
	}
}

func (r *runner) ensureImage(ctx context.Context, img *envImage, platform string) error {
	if img.Dockerfile == "" {
		r.logf("Image", "%s (the job's container)", img.Tag)
		return nil
	}
	if r.deps.Engine.ImageExists(ctx, img.Tag) {
		r.logf("Image", "%s (cached)", img.Tag)
		return nil
	}
	var tools []string
	for _, in := range img.Installs {
		tools = append(tools, in.Tool+" "+in.Version)
	}
	what := "common runner packages"
	if len(tools) > 0 {
		what += ", " + strings.Join(tools, ", ")
	}
	r.logf("Image", "building %s from %s with %s (first run only, this can take a few minutes)", img.Tag, r.plan.Image.Ref, what)
	var buf bytes.Buffer
	var w io.Writer = &buf
	if r.opts.Verbose {
		w = io.MultiWriter(&buf, newPrefixWriter(r.out, "    │ "))
	}
	if err := r.deps.Engine.Build(ctx, img.Dockerfile, img.Tag, platform, w); err != nil {
		return fmt.Errorf("%w\n%s", err, tailText(buf.String(), 30))
	}
	return nil
}

func (r *runner) makeDirs() error {
	for _, dir := range []string{r.hostWorkspace, filepath.Join(r.hostTemp, "_runner_file_commands")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) cleanup(ctx context.Context, res *Result) {
	if r.opts.Keep {
		res.Container, res.Workspace = r.name, r.hostWorkspace
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	ids := append([]string{}, r.services...)
	if r.container != "" {
		ids = append(ids, r.container)
	}
	_ = r.deps.Engine.Remove(cctx, ids...)
	_ = os.RemoveAll(r.hostBase)
}

func (r *runner) startContainers(ctx context.Context, image, platform string) error {
	var hosts []string
	for _, svc := range r.plan.Services {
		hosts = append(hosts, svc.Name+":127.0.0.1")
	}
	id, err := r.deps.Engine.Run(ctx, docker.RunOptions{
		Name: r.name, Image: image, Platform: platform, Init: true,
		Mounts:  []docker.Mount{{Host: r.hostRoot, Container: r.containerRoot}},
		Workdir: r.plan.Workspace, Entrypoint: "tail", Cmd: []string{"-f", "/dev/null"}, AddHosts: hosts,
	})
	if err != nil {
		return err
	}
	r.container = id

	prep := fmt.Sprintf("mkdir -p %s && (command -v git >/dev/null 2>&1 && git config --system --add safe.directory '*' || true)", r.home)
	if code, err := r.deps.Engine.Exec(ctx, docker.ExecOptions{Container: id, Cmd: []string{"sh", "-c", prep}, Stdout: io.Discard, Stderr: io.Discard}); err != nil || code != 0 {
		return fmt.Errorf("prepare container: exit %d %v (the image needs a POSIX shell)", code, err)
	}
	var pathOut bytes.Buffer
	if code, _ := r.deps.Engine.Exec(ctx, docker.ExecOptions{Container: id, Cmd: []string{"printenv", "PATH"}, Stdout: &pathOut, Stderr: io.Discard}); code == 0 {
		r.basePath = strings.TrimSpace(pathOut.String())
	}
	if r.basePath == "" {
		r.basePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	for _, svc := range r.plan.Services {
		if err := r.startService(ctx, svc); err != nil {
			return err
		}
	}
	return nil
}

// startService runs a service container in the job container's network
// namespace, so it is reachable both as localhost (as on a runner) and by
// its service name (as in container jobs).
func (r *runner) startService(ctx context.Context, svc plan.Service) error {
	extra, err := docker.SplitArgs(svc.Options)
	if err != nil {
		return fmt.Errorf("service %s options: %w", svc.Name, err)
	}
	id, err := r.deps.Engine.Run(ctx, docker.RunOptions{
		Name: r.name + "-" + svc.Name, Image: svc.Image, Network: "container:" + r.container,
		Env: svc.Env, Extra: dropNetworkFlags(extra),
	})
	if err != nil {
		return fmt.Errorf("start service %s: %w", svc.Name, err)
	}
	r.services = append(r.services, id)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		status, err := r.deps.Engine.Health(ctx, id)
		if err != nil {
			return fmt.Errorf("service %s: %w", svc.Name, err)
		}
		switch status {
		case "healthy", "none":
			r.logf("Service", "%s (%s) is up", svc.Name, svc.Image)
			return nil
		case "exited", "unhealthy":
			return fmt.Errorf("service %s is %s:\n%s", svc.Name, status, tailText(r.deps.Engine.Logs(ctx, id, 20), 20))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service %s did not become healthy within 3 minutes", svc.Name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// dropNetworkFlags removes options that conflict with sharing a network
// namespace (port publishing, network selection, names).
func dropNetworkFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, hasValue := strings.Cut(a, "=")
		switch name {
		case "-p", "--publish", "--network", "--net", "--name", "--hostname", "-h", "--network-alias":
			if !hasValue {
				i++ // skip the separate value
			}
			continue
		}
		out = append(out, a)
	}
	return out
}
