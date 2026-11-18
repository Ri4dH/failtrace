package plan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	exprRe        = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	ubuntuLabelRe = regexp.MustCompile(`^ubuntu-(\d{2}\.\d{2})(?:-arm)?$`)
)

const maskedSecret = "***"

// exprContext holds the values FailTrace can substitute into ${{ }} expressions.
type exprContext map[string]string

func (b *builder) newExprContext() exprContext {
	r := b.in.Run
	c := exprContext{
		"github.sha":              r.SHA,
		"github.repository":       b.in.Owner + "/" + b.in.Repo,
		"github.repository_owner": b.in.Owner,
		"github.event_name":       r.Event,
		"github.run_id":           strconv.FormatInt(r.ID, 10),
		"github.run_attempt":      strconv.Itoa(r.Attempt),
		"github.workflow":         r.Workflow,
		"github.job":              b.in.Job.WorkflowJob,
		"runner.os":               "Linux",
	}
	for k, v := range b.in.Job.Matrix {
		c["matrix."+k] = v
	}
	return c
}

// eval substitutes known expressions and records the ones it cannot resolve.
// Secret references are left for buildEnv to report.
func (b *builder) eval(s string) string {
	return exprRe.ReplaceAllStringFunc(s, func(m string) string {
		key := exprRe.FindStringSubmatch(m)[1]
		if v, ok := b.expr[key]; ok {
			return v
		}
		if !strings.HasPrefix(key, "secrets.") {
			b.unresolved[key] = true
		}
		return m
	})
}

func (b *builder) chooseImage() {
	p := b.plan
	labels := b.in.Job.RunsOn
	lower := strings.ToLower(strings.Join(labels, " "))
	runner := b.ev.Log.Runner
	p.Image.Platform = "linux/amd64"
	if strings.Contains(lower, "arm") || strings.Contains(strings.ToLower(runner.Image), "arm") {
		p.Image.Platform = "linux/arm64"
	}
	b.expr["runner.arch"] = map[string]string{"linux/amd64": "X64", "linux/arm64": "ARM64"}[p.Image.Platform]

	repoName := b.in.Repo
	if job := b.ev.Job; job != nil && job.Container != "" {
		p.Image.Ref, p.Image.Exact = b.eval(job.Container), true
		p.Image.Source = "the job's container, the same image CI used"
		p.Workspace = "/__w/" + repoName + "/" + repoName
		b.expr["runner.temp"] = "/__w/_temp"
	} else {
		p.Workspace = "/home/runner/work/" + repoName + "/" + repoName
		b.expr["runner.temp"] = "/home/runner/work/_temp"
		b.chooseRunnerImage(labels, lower)
	}
	b.expr["github.workspace"] = p.Workspace
}

func (b *builder) chooseRunnerImage(labels []string, lower string) {
	img := &b.plan.Image
	runner := b.ev.Log.Runner
	switch {
	case strings.Contains(lower, "windows") || strings.Contains(lower, "macos"):
		img.Ref = "ubuntu:latest"
		img.Source = "a Linux stand-in for " + strings.Join(labels, ", ")
		b.issue(SeverityWarning, 0, "the job ran on %s; a Linux container only approximates it, so OS-specific failures will not reproduce", strings.Join(labels, ", "))
		return
	case strings.HasPrefix(runner.Image, "ubuntu"):
		if m := ubuntuLabelRe.FindStringSubmatch(runner.Image); m != nil {
			img.Ref = "ubuntu:" + m[1]
			img.Source = fmt.Sprintf("matches the runner image %s %s", runner.Image, runner.ImageVersion)
			return
		}
	}
	for _, l := range labels {
		if m := ubuntuLabelRe.FindStringSubmatch(strings.ToLower(l)); m != nil {
			img.Ref, img.Source = "ubuntu:"+m[1], "matches the runs-on label "+l
			return
		}
	}
	img.Ref = "ubuntu:latest"
	if strings.Contains(lower, "self-hosted") {
		img.Source = "a stand-in for a self-hosted runner"
		b.issue(SeverityWarning, 0, "the job ran on a self-hosted runner (%s) whose setup is unknown", strings.Join(labels, ", "))
		return
	}
	img.Source = "the runner's Ubuntu version could not be determined"
	b.issue(SeverityInfo, 0, "could not tell which Ubuntu version %s used; the plan uses ubuntu:latest", strings.Join(labels, ", "))
}

// buildEnv assembles job-wide environment: GitHub's defaults, then workflow
// and job `env:`. Values GitHub evaluated in the step headers win over our
// own evaluation. Each run step keeps only what differs from that.
func (b *builder) buildEnv() {
	p := b.plan
	header := b.headerEnv()
	seen := map[string]int{}
	set := func(name, value, source string) {
		v := EnvVar{Name: name, Value: value, Source: source}
		if hv, ok := header[name]; ok {
			v.Value = hv
		} else {
			v.Value = b.eval(value)
		}
		if v.Value == maskedSecret || (exprRe.MatchString(v.Value) && strings.Contains(v.Value, "secrets.")) {
			v.Value, v.Secret = "", true
			b.addSecret(name)
		}
		if i, ok := seen[name]; ok {
			p.Env[i] = v
			return
		}
		seen[name] = len(p.Env)
		p.Env = append(p.Env, v)
	}

	for _, kv := range b.githubDefaults() {
		set(kv[0], kv[1], "github")
	}
	if wf := b.ev.Workflow; wf != nil {
		for _, k := range sortedKeys(wf.Env) {
			set(k, wf.Env[k], "workflow")
		}
	}
	if job := b.ev.Job; job != nil {
		for _, k := range sortedKeys(job.Env) {
			set(k, job.Env[k], "job")
		}
	}

	for i := range p.Steps {
		s := &p.Steps[i]
		if s.Kind != KindRun || len(s.Env) == 0 {
			continue
		}
		own := map[string]string{}
		for k, v := range s.Env {
			if strings.Contains(v, "/opt/hostedtoolcache/") {
				continue // set by a setup action; the local toolchain install provides its own
			}
			if j, ok := seen[k]; ok && (p.Env[j].Value == v || p.Env[j].Secret) {
				continue
			}
			if v == maskedSecret {
				b.addSecret(k)
			}
			own[k] = v
		}
		s.Env = nil
		if len(own) > 0 {
			s.Env = own
		}
	}
	if len(p.Secrets) > 0 {
		b.issue(SeverityWarning, 0, "needs secrets that are not available locally: %s; you will have to supply them", strings.Join(p.Secrets, ", "))
	}
}

// headerEnv merges the evaluated env printed in run step headers, preferring
// the failing step's values.
func (b *builder) headerEnv() map[string]string {
	out := map[string]string{}
	for i := len(b.plan.Steps) - 1; i >= 0; i-- {
		s := b.plan.Steps[i]
		if s.Kind != KindRun {
			continue
		}
		for k, v := range s.Env {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
	}
	return out
}

func (b *builder) addSecret(name string) {
	for _, s := range b.plan.Secrets {
		if s == name {
			return
		}
	}
	b.plan.Secrets = append(b.plan.Secrets, name)
}

func (b *builder) githubDefaults() [][2]string {
	r := b.in.Run
	sha := b.plan.Commit
	if sha == "" {
		sha = r.SHA
	}
	vars := [][2]string{
		{"CI", "true"},
		{"GITHUB_ACTIONS", "true"},
		{"GITHUB_REPOSITORY", b.in.Owner + "/" + b.in.Repo},
		{"GITHUB_REPOSITORY_OWNER", b.in.Owner},
		{"GITHUB_SHA", sha},
		{"GITHUB_EVENT_NAME", r.Event},
		{"GITHUB_RUN_ID", strconv.FormatInt(r.ID, 10)},
		{"GITHUB_RUN_ATTEMPT", strconv.Itoa(r.Attempt)},
		{"GITHUB_WORKFLOW", r.Workflow},
		{"GITHUB_JOB", b.in.Job.WorkflowJob},
		{"GITHUB_WORKSPACE", b.plan.Workspace},
		{"RUNNER_OS", "Linux"},
		{"RUNNER_ARCH", b.expr["runner.arch"]},
		{"RUNNER_TEMP", b.expr["runner.temp"]},
	}
	if r.Event == "push" && r.Branch != "" {
		vars = append(vars, [2]string{"GITHUB_REF", "refs/heads/" + r.Branch}, [2]string{"GITHUB_REF_NAME", r.Branch})
	}
	return vars
}

func (b *builder) buildServices() {
	job := b.ev.Job
	if job == nil || len(job.Services) == 0 {
		return
	}
	names := make([]string, 0, len(job.Services))
	for _, s := range job.Services {
		svc := Service{Name: s.Name, Image: b.eval(s.Image), Env: evalMap(b, s.Env)}
		for _, port := range s.Ports {
			svc.Ports = append(svc.Ports, b.eval(port))
		}
		b.plan.Services = append(b.plan.Services, svc)
		names = append(names, fmt.Sprintf("%s (%s)", svc.Name, svc.Image))
	}
	b.issue(SeverityWarning, 0, "the job needs service containers: %s", strings.Join(names, ", "))
}

func evalMap(b *builder, m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = b.eval(v)
	}
	return out
}
