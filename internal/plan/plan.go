// Package plan turns a failed job into a reproduction plan: which image to
// start, which commit to check out, which toolchains to install, which
// environment to set, and which steps to replay up to and including the one
// that failed. Nothing is executed here.
//
// The job log is the source of truth for what actually ran: it contains the
// evaluated scripts, shells, environment, resolved toolchain versions, and the
// inner steps of composite actions. The workflow file adds structure such as
// step names and working directories.
package plan

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/joblog"
)

// Verdict summarises how faithfully the plan can reproduce the failure.
type Verdict string

const (
	VerdictReady   Verdict = "ready"   // nothing known to be missing
	VerdictPartial Verdict = "partial" // reproducible, but some parts are approximated or missing
	VerdictBlocked Verdict = "blocked" // the failing step itself cannot be replayed
)

// Severity ranks an Issue.
type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Kind says how a step is handled when the plan is executed.
type Kind string

const (
	KindCheckout    Kind = "checkout"    // check out a commit into the workspace
	KindToolchain   Kind = "toolchain"   // install a toolchain instead of running the setup action
	KindRun         Kind = "run"         // replay the script
	KindComposite   Kind = "composite"   // composite action; its inner steps follow with Parent set
	KindSkip        Kind = "skip"        // not needed locally (caches, uploads, ...)
	KindUnsupported Kind = "unsupported" // JavaScript or Docker action that is not replayed
)

// FileSource reads repository files, used to resolve version files such as go.mod.
type FileSource interface {
	GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error)
}

// Input is everything Build needs about one failed job.
type Input struct {
	Owner string
	Repo  string
	Run   inspect.RunInfo
	Job   inspect.JobReport // must carry Evidence
}

// Plan is a reproduction plan for one failed job.
type Plan struct {
	Repository string      `json:"repository"`
	RunURL     string      `json:"run_url,omitempty"`
	Job        JobRef      `json:"job"`
	Verdict    Verdict     `json:"verdict"`
	Image      Image       `json:"image"`
	Workspace  string      `json:"workspace"`
	Commit     string      `json:"commit"`
	Toolchains []Toolchain `json:"toolchains,omitempty"`
	Services   []Service   `json:"services,omitempty"`
	Env        []EnvVar    `json:"env"`
	Secrets    []string    `json:"secrets,omitempty"`
	Steps      []Step      `json:"steps"`
	Issues     []Issue     `json:"issues,omitempty"`
}

// JobRef identifies the job being reproduced.
type JobRef struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	URL         string            `json:"url,omitempty"`
	WorkflowJob string            `json:"workflow_job,omitempty"`
	Matrix      map[string]string `json:"matrix,omitempty"`
}

// Image is the container image the reproduction starts from.
type Image struct {
	Ref      string `json:"ref"`
	Platform string `json:"platform"`
	Exact    bool   `json:"exact"` // the same image CI used (job container)
	Source   string `json:"source"`
}

// Toolchain is a language toolchain to install.
type Toolchain struct {
	Tool    string `json:"tool"`
	Version string `json:"version,omitempty"`
	Spec    string `json:"spec,omitempty"` // what the workflow asked for, e.g. "1.22" or "go.mod"
	Exact   bool   `json:"exact"`          // Version is the exact version CI resolved
	Source  string `json:"source"`
}

// Service is a service container the job depends on.
type Service struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Ports   []string          `json:"ports,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Options string            `json:"options,omitempty"`
}

// EnvVar is a job-wide environment variable.
type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"` // github, workflow, job
	Secret bool   `json:"secret,omitempty"`
}

// Checkout describes one actions/checkout step.
type Checkout struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Path       string `json:"path,omitempty"`
	FetchDepth string `json:"fetch_depth,omitempty"`
	Submodules string `json:"submodules,omitempty"`
	LFS        bool   `json:"lfs,omitempty"`
	MergeHead  string `json:"merge_head,omitempty"`
	MergeBase  string `json:"merge_base,omitempty"`
}

// Step is one step of the plan, in execution order.
type Step struct {
	Number           int               `json:"number,omitempty"` // step number in the GitHub UI, if known
	Name             string            `json:"name"`
	Kind             Kind              `json:"kind"`
	Uses             string            `json:"uses,omitempty"`
	Script           string            `json:"script,omitempty"`
	Shell            string            `json:"shell,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Env              map[string]string `json:"env,omitempty"` // in addition to Plan.Env
	Checkout         *Checkout         `json:"checkout,omitempty"`
	Toolchain        *Toolchain        `json:"toolchain,omitempty"`
	Parent           int               `json:"parent,omitempty"` // 1-based index of the enclosing composite step
	Target           bool              `json:"target,omitempty"` // the step that failed in CI
	Note             string            `json:"note,omitempty"`
}

// Issue is something that may make the reproduction differ from CI.
type Issue struct {
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Step     int      `json:"step,omitempty"` // 1-based index into Plan.Steps
}

// ErrNoLog is returned when the job log, the plan's main input, is missing.
var ErrNoLog = errors.New("the job log is unavailable (expired or inaccessible), so there is nothing to plan from")

type builder struct {
	ctx        context.Context
	in         Input
	ev         *inspect.Evidence
	files      FileSource
	expr       exprContext
	plan       *Plan
	sections   map[int]*joblog.Section // plan step index → log section
	unresolved map[string]bool
}

// Build creates a reproduction plan. files may be nil, in which case version
// files (go.mod, .nvmrc, ...) are not consulted.
func Build(ctx context.Context, in Input, files FileSource) (*Plan, error) {
	ev := in.Job.Evidence
	if ev == nil || ev.Log == nil {
		return nil, ErrNoLog
	}
	if ev.Failed == nil {
		return nil, errors.New("could not find the failing step in the job log")
	}
	b := &builder{
		ctx: ctx, in: in, ev: ev, files: files,
		sections:   map[int]*joblog.Section{},
		unresolved: map[string]bool{},
		plan: &Plan{
			Repository: in.Owner + "/" + in.Repo,
			RunURL:     in.Run.URL,
			Job: JobRef{
				ID: in.Job.ID, Name: in.Job.Name, URL: in.Job.URL,
				WorkflowJob: in.Job.WorkflowJob, Matrix: in.Job.Matrix,
			},
		},
	}
	b.expr = b.newExprContext()
	b.chooseImage()
	b.buildSteps()
	if b.plan.Commit == "" {
		b.plan.Commit = in.Run.SHA
	}
	b.buildEnv()
	b.buildServices()
	b.reviewSteps()
	b.finish()
	return b.plan, nil
}

func (b *builder) issue(sev Severity, step int, format string, args ...any) {
	b.plan.Issues = append(b.plan.Issues, Issue{Severity: sev, Message: fmt.Sprintf(format, args...), Step: step})
}

func (b *builder) finish() {
	if len(b.unresolved) > 0 {
		keys := sortedKeys(b.unresolved)
		b.issue(SeverityWarning, 0, "could not evaluate expressions: %s", joinQuoted(keys))
	}
	rank := map[Severity]int{SeverityBlocker: 0, SeverityWarning: 1, SeverityInfo: 2}
	sort.SliceStable(b.plan.Issues, func(i, j int) bool {
		return rank[b.plan.Issues[i].Severity] < rank[b.plan.Issues[j].Severity]
	})
	b.plan.Verdict = VerdictReady
	for _, is := range b.plan.Issues {
		switch is.Severity {
		case SeverityBlocker:
			b.plan.Verdict = VerdictBlocked
			return
		case SeverityWarning:
			b.plan.Verdict = VerdictPartial
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinQuoted(items []string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += ", "
		}
		out += "`" + it + "`"
	}
	return out
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
