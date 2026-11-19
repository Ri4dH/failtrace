package repro

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ri4dH/failtrace/internal/docker"
	"github.com/Ri4dH/failtrace/internal/plan"
)

const (
	noTestCache      = "-count=1"
	maskedSecret     = "***"
	targetLineLimit  = 50000
	setupOutputLines = 40
)

var fileCommands = []struct{ env, file string }{
	{"GITHUB_ENV", "set_env"},
	{"GITHUB_PATH", "add_path"},
	{"GITHUB_OUTPUT", "set_output"},
	{"GITHUB_STATE", "save_state"},
	{"GITHUB_STEP_SUMMARY", "step_summary"},
}

func (r *runner) runSteps(ctx context.Context, res *Result) error {
	total := len(r.plan.Steps)
	for i, s := range r.plan.Steps {
		n := i + 1
		sr := StepResult{Index: n, Name: s.Name, Kind: s.Kind, Target: s.Target}
		label := fmt.Sprintf("[%d/%d]", n, total)
		switch s.Kind {
		case plan.KindCheckout:
			sr.Status, sr.Note = StepPassed, "checked out on the host"
		case plan.KindToolchain:
			sr.Status, sr.Note = StepPassed, "installed in the environment image"
		case plan.KindRun:
			r.shellStep, r.shellIndex = s, n
			if s.Target {
				return r.runTarget(ctx, n, s, label, sr, res)
			}
			r.logf(label, "%s", s.Name)
			code, output, dur, err := r.runStep(ctx, n, s, r.opts.Verbose)
			if err != nil {
				return fmt.Errorf("step %d (%s): %w", n, s.Name, err)
			}
			sr.ExitCode, sr.Seconds = &code, dur.Seconds()
			sr.Status = StepPassed
			if code != 0 {
				sr.Status = StepFailed
			}
			r.logExit("", code, dur)
			if code != 0 {
				sr.Output = tailLines(output, setupOutputLines)
				for _, l := range sr.Output {
					fmt.Fprintf(r.out, "    │ %s\n", l)
				}
				res.Steps = append(res.Steps, sr)
				res.Verdict = VerdictDiverged
				res.Reasons = []string{fmt.Sprintf("step %d (%s) failed locally with exit code %d, but passed in CI, so the failing step never ran", n, s.Name, code)}
				r.markNotRun(res, i+1)
				return nil
			}
		default:
			sr.Status, sr.Note = StepSkipped, s.Note
		}
		res.Steps = append(res.Steps, sr)
	}
	return errors.New("the plan has no failing step to run")
}

func (r *runner) markNotRun(res *Result, from int) {
	for i := from; i < len(r.plan.Steps); i++ {
		s := r.plan.Steps[i]
		res.Steps = append(res.Steps, StepResult{Index: i + 1, Name: s.Name, Kind: s.Kind, Status: StepNotRun, Target: s.Target})
	}
}

// logExit prints a step's outcome; prefix labels repeated runs ("run 2/5 ").
func (r *runner) logExit(prefix string, code int, dur time.Duration) {
	fmt.Fprintf(r.out, "  %s%s exit code %d (%.1fs)\n", prefix, map[bool]string{true: "✓", false: "✗"}[code == 0], code, dur.Seconds())
}

// scriptPath writes step n's script to the shared temp directory and returns
// its path inside the container.
func (r *runner) scriptPath(n int, s plan.Step) (string, error) {
	rel := fmt.Sprintf("failtrace/step-%d%s", n, scriptExt(s.Shell))
	hostScript := filepath.Join(r.hostTemp, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(hostScript), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(hostScript, []byte(s.Script+"\n"), 0o644); err != nil {
		return "", err
	}
	return path.Join(r.containerRoot, "_temp", rel), nil
}

// workdir resolves a step's working directory inside the container.
func (r *runner) workdir(s plan.Step) string {
	if s.WorkingDirectory == "" {
		return r.plan.Workspace
	}
	if path.IsAbs(s.WorkingDirectory) {
		return s.WorkingDirectory
	}
	return path.Join(r.plan.Workspace, s.WorkingDirectory)
}

// runStep runs a run step; stream copies its output to the terminal live.
func (r *runner) runStep(ctx context.Context, n int, s plan.Step, stream bool) (int, []string, time.Duration, error) {
	script, err := r.scriptPath(n, s)
	if err != nil {
		return 0, nil, 0, err
	}
	argv, err := shellCommand(s.Shell, script)
	if err != nil {
		return 0, nil, 0, err
	}
	env, err := r.stepEnv(s, n)
	if err != nil {
		return 0, nil, 0, err
	}
	wd := r.workdir(s)

	limit := setupOutputLines * 4
	if s.Target {
		limit = targetLineLimit
	}
	collector := newLineCollector(limit)
	var w io.Writer = collector
	var pw *prefixWriter
	if stream {
		pw = newPrefixWriter(r.out, "    │ ")
		w = io.MultiWriter(collector, pw)
	}
	start := time.Now()
	code, err := r.deps.Engine.Exec(ctx, docker.ExecOptions{
		Container: r.container, Workdir: wd, Env: env, Cmd: argv, Stdout: w, Stderr: w,
	})
	if pw != nil {
		pw.Flush()
	}
	if err != nil {
		return 0, nil, 0, err
	}
	r.applyFileCommands(n)
	return code, collector.Lines(), time.Since(start), nil
}

// stepEnv layers the job env, the step's env from CI's log, and values
// earlier local steps wrote to $GITHUB_ENV.
func (r *runner) stepEnv(s plan.Step, n int) (map[string]string, error) {
	env := map[string]string{}
	for _, e := range r.plan.Env {
		if e.Secret {
			if v, ok := r.opts.Secrets[e.Name]; ok {
				env[e.Name] = v
			}
			continue
		}
		env[e.Name] = e.Value
	}
	for k, v := range s.Env {
		if v == maskedSecret {
			if sv, ok := r.opts.Secrets[k]; ok {
				env[k] = sv
			}
			continue
		}
		env[k] = v
	}
	for k, v := range r.opts.Secrets {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	for k, v := range r.githubEnv {
		env[k] = v
	}
	env["HOME"] = r.home
	env["RUNNER_TOOL_CACHE"] = "/opt/hostedtoolcache"
	env["FAILTRACE"] = "true"
	for _, fc := range fileCommands {
		name := fmt.Sprintf("%s_%d", fc.file, n)
		if err := os.WriteFile(filepath.Join(r.hostTemp, "_runner_file_commands", name), nil, 0o644); err != nil {
			return nil, err
		}
		env[fc.env] = path.Join(r.containerRoot, "_temp", "_runner_file_commands", name)
	}
	env["PATH"] = strings.Join(append(append([]string{}, r.githubPath...), r.basePath), ":")
	if s.Target && r.opts.Runs > 1 {
		// go test replays cached passing results, which would hide flakiness.
		env["GOFLAGS"] = strings.TrimSpace(env["GOFLAGS"] + " " + noTestCache)
	}
	return env, nil
}

// applyFileCommands carries $GITHUB_ENV and $GITHUB_PATH into later steps.
func (r *runner) applyFileCommands(n int) {
	dir := filepath.Join(r.hostTemp, "_runner_file_commands")
	if data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("set_env_%d", n))); err == nil {
		vars, err := parseEnvFile(string(data))
		if err != nil {
			r.differences = append(r.differences, fmt.Sprintf("step %d: %v", n, err))
		}
		for k, v := range vars {
			r.githubEnv[k] = v
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("add_path_%d", n))); err == nil {
		for _, p := range parsePathFile(string(data)) {
			r.githubPath = append([]string{p}, r.githubPath...)
		}
	}
}

func scriptExt(shell string) string {
	fields, _ := docker.SplitArgs(shell)
	if len(fields) > 0 && strings.HasPrefix(path.Base(fields[0]), "python") {
		return ".py"
	}
	return ".sh"
}

// shellCommand turns a step shell such as "/usr/bin/bash -e {0}" into argv.
func shellCommand(shell, script string) ([]string, error) {
	if strings.TrimSpace(shell) == "" {
		shell = "bash -e {0}"
	}
	lower := strings.ToLower(shell)
	if strings.Contains(shell, `\`) || strings.Contains(lower, "pwsh") || strings.Contains(lower, "powershell") || strings.HasPrefix(lower, "cmd") {
		return nil, fmt.Errorf("the step uses a Windows or PowerShell shell (%s), which the Linux container cannot run", shell)
	}
	fields, err := docker.SplitArgs(shell)
	if err != nil || len(fields) == 0 {
		return nil, fmt.Errorf("cannot parse shell %q", shell)
	}
	if path.IsAbs(fields[0]) {
		fields[0] = path.Base(fields[0]) // /usr/bin/bash may live elsewhere in the image
	}
	replaced := false
	for i, f := range fields {
		if strings.Contains(f, "{0}") {
			fields[i] = strings.ReplaceAll(f, "{0}", script)
			replaced = true
		}
	}
	if !replaced {
		fields = append(fields, script)
	}
	return fields, nil
}
