// Package inspect gathers everything known about a failed workflow run:
// run metadata, the failing job and step, the workflow definition, and the
// failure signature extracted from the job log.
package inspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ri4dH/failtrace/internal/github"
	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/runurl"
	"github.com/Ri4dH/failtrace/internal/workflow"
)

// DefaultExcerptLines is the default number of log lines kept per failure.
const DefaultExcerptLines = 25

// API is the subset of the GitHub client that inspection needs.
type API interface {
	GetRun(ctx context.Context, owner, repo string, runID int64) (*github.Run, error)
	ListJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]github.Job, error)
	GetJobLog(ctx context.Context, owner, repo string, jobID int64) (string, error)
	GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error)
}

// Options tunes an inspection.
type Options struct {
	ExcerptLines int
}

// Report is the result of inspecting a run.
type Report struct {
	Run      RunInfo     `json:"run"`
	Jobs     []JobReport `json:"failed_jobs"`
	Warnings []string    `json:"warnings,omitempty"`
}

// RunInfo describes the inspected run.
type RunInfo struct {
	Repository   string `json:"repository"`
	ID           int64  `json:"id"`
	Attempt      int    `json:"attempt"`
	Workflow     string `json:"workflow"`
	WorkflowPath string `json:"workflow_path,omitempty"`
	Title        string `json:"title,omitempty"`
	Event        string `json:"event"`
	Branch       string `json:"branch,omitempty"`
	SHA          string `json:"sha"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	URL          string `json:"url"`
}

// JobReport describes one failed job.
type JobReport struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Conclusion  string            `json:"conclusion"`
	WorkflowJob string            `json:"workflow_job,omitempty"`
	JobGuessed  bool              `json:"workflow_job_guessed,omitempty"`
	Matrix      map[string]string `json:"matrix,omitempty"`
	RunsOn      []string          `json:"runs_on,omitempty"`
	Runner      joblog.RunnerInfo `json:"runner"`
	Steps       []StepStatus      `json:"steps"`
	FailedStep  *FailedStep       `json:"failed_step,omitempty"`
	Failure     *joblog.Failure   `json:"failure,omitempty"`
	Notes       []string          `json:"notes,omitempty"`

	// Evidence is the raw material behind this report, for later stages such
	// as planning. It is not serialised.
	Evidence *Evidence `json:"-"`
}

// Evidence holds the parsed inputs a JobReport was built from. Any field may
// be nil when the corresponding source was unavailable.
type Evidence struct {
	Workflow *workflow.Workflow
	Job      *workflow.Job
	Log      *joblog.Log
	Failed   *joblog.Section // the failing step's log section
	API      github.Job
}

// StepStatus is one step's outcome as reported by the API.
type StepStatus struct {
	Number     int    `json:"number"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// FailedStep is the failing step, enriched with its workflow definition.
type FailedStep struct {
	Number           int               `json:"number"`
	Name             string            `json:"name"`
	ID               string            `json:"id,omitempty"`
	Uses             string            `json:"uses,omitempty"`
	Run              string            `json:"run,omitempty"`
	Shell            string            `json:"shell,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	With             map[string]string `json:"with,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
}

// Run inspects the run identified by ref.
func Run(ctx context.Context, api API, ref runurl.Ref, opts Options) (*Report, error) {
	if opts.ExcerptLines <= 0 {
		opts.ExcerptLines = DefaultExcerptLines
	}
	run, err := api.GetRun(ctx, ref.Owner, ref.Repo, ref.RunID)
	if err != nil {
		return nil, fmt.Errorf("fetch run: %w", err)
	}
	attempt := ref.Attempt
	if attempt == 0 {
		attempt = run.RunAttempt
	}
	rep := &Report{Jobs: []JobReport{}, Run: RunInfo{
		Repository: ref.Repository(), ID: run.ID, Attempt: attempt, Workflow: run.Name,
		WorkflowPath: workflowPath(run.Path), Title: run.DisplayTitle, Event: run.Event,
		Branch: run.HeadBranch, SHA: run.HeadSHA, Status: run.Status, Conclusion: run.Conclusion,
		URL: run.HTMLURL,
	}}

	jobs, err := api.ListJobs(ctx, ref.Owner, ref.Repo, ref.RunID, attempt)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	if len(jobs) == 0 && run.Conclusion == "failure" {
		rep.Warnings = append(rep.Warnings,
			"the run failed before any job started: usually an invalid workflow file, a startup failure, "+
				"or a run that needed approval. GitHub shows the reason on the run page")
	}
	targets, err := selectJobs(jobs, ref.JobID)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return rep, nil
	}

	wf := loadWorkflow(ctx, api, ref, run, rep)
	for _, j := range targets {
		rep.Jobs = append(rep.Jobs, inspectJob(ctx, api, ref, wf, j, opts, rep))
	}
	return rep, nil
}

func selectJobs(jobs []github.Job, jobID int64) ([]github.Job, error) {
	if jobID != 0 {
		for _, j := range jobs {
			if j.ID == jobID {
				return []github.Job{j}, nil
			}
		}
		return nil, fmt.Errorf("job %d is not part of this run attempt", jobID)
	}
	var failed, cancelled []github.Job
	for _, j := range jobs {
		switch j.Conclusion {
		case "failure", "timed_out":
			failed = append(failed, j)
		case "cancelled":
			cancelled = append(cancelled, j)
		}
	}
	if len(failed) > 0 {
		return failed, nil
	}
	return cancelled, nil
}

func workflowPath(p string) string {
	if i := strings.IndexByte(p, '@'); i >= 0 {
		p = p[:i]
	}
	return p
}

func loadWorkflow(ctx context.Context, api API, ref runurl.Ref, run *github.Run, rep *Report) *workflow.Workflow {
	path := rep.Run.WorkflowPath
	if !strings.HasPrefix(path, ".github/workflows/") {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"workflow %q is not a file in the repository (dynamic workflow); step details come from logs only", path))
		return nil
	}
	data, err := api.GetFile(ctx, ref.Owner, ref.Repo, path, run.HeadSHA)
	if err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("could not fetch %s at %s: %v", path, shortSHA(run.HeadSHA), err))
		return nil
	}
	wf, err := workflow.Parse(data)
	if err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("could not parse %s: %v", path, err))
		return nil
	}
	return wf
}

func inspectJob(ctx context.Context, api API, ref runurl.Ref, wf *workflow.Workflow, j github.Job, opts Options, rep *Report) JobReport {
	jr := JobReport{ID: j.ID, Name: j.Name, URL: j.HTMLURL, Conclusion: j.Conclusion, RunsOn: j.Labels}
	ev := &Evidence{Workflow: wf, API: j}
	jr.Evidence = ev

	var failed *github.Step
	names := make([]string, 0, len(j.Steps))
	for i, s := range j.Steps {
		jr.Steps = append(jr.Steps, StepStatus{Number: s.Number, Name: s.Name, Conclusion: s.Conclusion})
		names = append(names, s.Name)
		if failed == nil && (s.Conclusion == "failure" || s.Conclusion == "timed_out") {
			failed = &j.Steps[i]
		}
	}
	if failed == nil {
		for i, s := range j.Steps {
			if s.Conclusion == "cancelled" {
				failed = &j.Steps[i]
				break
			}
		}
	}
	if failed != nil {
		jr.FailedStep = &FailedStep{Number: failed.Number, Name: failed.Name}
	}

	var wfJob *workflow.Job
	var wfStep *workflow.Step
	if wf != nil {
		if m := wf.MatchJob(j.Name); m != nil {
			wfJob = m.Job
			ev.Job = m.Job
			jr.WorkflowJob, jr.JobGuessed, jr.Matrix = m.Job.ID, m.Guessed, m.Matrix
			if failed != nil {
				if idx := m.Job.MatchStep(failed.Name, failed.Number, names); idx >= 0 {
					wfStep = &m.Job.Steps[idx]
					fillFromWorkflow(jr.FailedStep, wf, m.Job, wfStep)
				}
			}
		} else {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("could not match job %q to a job in %s", j.Name, rep.Run.WorkflowPath))
		}
	}
	jr.Notes = notes(wfJob, wfStep, j.Labels)

	text, err := api.GetJobLog(ctx, ref.Owner, ref.Repo, j.ID)
	if err != nil {
		jr.Notes = append(jr.Notes, fmt.Sprintf("job log unavailable: %v", err))
		return jr
	}
	lg := joblog.Parse(text)
	jr.Runner = lg.Runner
	ev.Log = lg
	if sec := lg.FailedSection(); sec != nil {
		ev.Failed = sec
		jr.Failure = joblog.Analyze(sec, opts.ExcerptLines)
	}
	return jr
}

func fillFromWorkflow(fs *FailedStep, wf *workflow.Workflow, job *workflow.Job, s *workflow.Step) {
	fs.ID, fs.Uses, fs.Run, fs.With, fs.Env = s.ID, s.Uses, s.Run, s.With, s.Env
	fs.Shell = firstNonEmpty(s.Shell, job.Defaults.Shell, wf.Defaults.Shell)
	fs.WorkingDirectory = firstNonEmpty(s.WorkingDirectory, job.Defaults.WorkingDirectory, wf.Defaults.WorkingDirectory)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
