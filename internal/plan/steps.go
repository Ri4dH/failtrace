package plan

import (
	"strings"

	"github.com/Ri4dH/failtrace/internal/github"
	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/workflow"
)

// buildSteps walks the log sections up to the failing one. Sections that line
// up with a workflow step are top-level steps; sections in between belong to
// the preceding composite action.
func (b *builder) buildSteps() {
	secs := b.ev.Log.Sections
	failed := indexOf(secs, b.ev.Failed)

	var wfSteps []workflow.Step
	if b.ev.Job != nil {
		wfSteps = b.ev.Job.Steps
	}
	api := apiSteps(b.ev.API.Steps, wfSteps)
	aligned := align(wfSteps, api, secs[:failed+1])

	lastTop := -1
	for i := 1; i <= failed; i++ {
		sec := secs[i]
		if sec.Post {
			continue
		}
		var wf *workflow.Step
		var apiStep *github.Step
		wfIdx, isTop := aligned[i]
		if isTop {
			wf, apiStep = &wfSteps[wfIdx], api[wfIdx]
		}

		parent := 0
		if !isTop && wfSteps != nil && lastTop >= 0 {
			if ps := &b.plan.Steps[lastTop]; ps.Kind == KindUnsupported || ps.Kind == KindComposite {
				ps.Kind, ps.Note = KindComposite, "composite action; its steps follow"
				parent = lastTop + 1
			}
		}

		step := b.classify(sec, wf)
		step.Parent = parent
		if apiStep != nil {
			step.Number, step.Name = apiStep.Number, apiStep.Name
		}
		step.Target = i == failed
		b.plan.Steps = append(b.plan.Steps, step)
		b.sections[len(b.plan.Steps)-1] = sec
		if parent == 0 {
			lastTop = len(b.plan.Steps) - 1
		}
	}
}

func (b *builder) classify(sec *joblog.Section, wf *workflow.Step) Step {
	s := Step{Name: sec.Title}
	if sec.Header.Shell != "" {
		s.Kind = KindRun
		s.Script = strings.Join(sec.Header.Script, "\n")
		s.Shell = sec.Header.Shell
		s.Env = sec.Header.Env
		s.WorkingDirectory = b.workingDirectory(wf)
		return s
	}

	s.Uses = strings.TrimPrefix(sec.Title, "Run ")
	act := lookupAction(s.Uses)
	switch act.kind {
	case actionCheckout:
		s.Kind = KindCheckout
		s.Checkout = b.checkout(sec)
	case actionSetup:
		tc := b.toolchain(act.setup, s.Uses, sec)
		s.Kind, s.Toolchain = KindToolchain, &tc
		b.addToolchain(tc)
		if rt, ok := runtimeToolchain(sec, s.Uses); ok {
			b.addToolchain(rt)
		}
	case actionSkip:
		s.Kind, s.Note = KindSkip, act.reason
	case actionDownload:
		s.Kind, s.Note = KindSkip, act.reason
		b.issue(SeverityWarning, len(b.plan.Steps)+1,
			"%s downloads artifacts produced by another job or run; files it provides will be missing locally", s.Uses)
	default:
		s.Kind, s.Note = KindUnsupported, "action is not replayed"
		b.detectToolcache(sec, s.Uses)
	}
	return s
}

func (b *builder) workingDirectory(wf *workflow.Step) string {
	dir := ""
	if wf != nil {
		dir = wf.WorkingDirectory
	}
	if dir == "" && b.ev.Job != nil {
		dir = b.ev.Job.Defaults.WorkingDirectory
	}
	if dir == "" && b.ev.Workflow != nil {
		dir = b.ev.Workflow.Defaults.WorkingDirectory
	}
	return b.eval(dir)
}

func (b *builder) checkout(sec *joblog.Section) *Checkout {
	w := sec.Header.With
	facts := sec.CheckoutFacts()
	current := b.plan.Repository
	co := &Checkout{
		Repository: w["repository"], Ref: w["ref"], Path: w["path"], FetchDepth: w["fetch-depth"],
		LFS: w["lfs"] == "true", Commit: facts.Commit, MergeHead: facts.MergeHead, MergeBase: facts.MergeBase,
	}
	if sub := w["submodules"]; sub != "" && sub != "false" {
		co.Submodules = sub
	}
	if co.Repository == "" {
		co.Repository = current
	}
	same := strings.EqualFold(co.Repository, current)
	if co.Ref == "" {
		co.Ref = facts.Ref
	}
	if co.Commit == "" && same && w["ref"] == "" {
		co.Commit = b.in.Run.SHA
	}
	if same && b.plan.Commit == "" {
		b.plan.Commit = co.Commit
	}
	return co
}

// align maps log section indexes to workflow step indexes. It searches
// forward only, so composite inner sections (which match no workflow step)
// are passed over. Steps the API reports as skipped have no section and are
// left out, so a skipped step cannot claim the section of a later look-alike.
func align(steps []workflow.Step, api []*github.Step, secs []*joblog.Section) map[int]int {
	out := map[int]int{}
	cursor := 1
	for wi, st := range steps {
		if api[wi] != nil && api[wi].Conclusion == "skipped" {
			continue
		}
		for j := cursor; j < len(secs); j++ {
			if !secs[j].Post && sectionMatches(st, secs[j]) {
				out[j] = wi
				cursor = j + 1
				break
			}
		}
	}
	return out
}

func sectionMatches(st workflow.Step, sec *joblog.Section) bool {
	title := strings.TrimPrefix(sec.Title, "Run ")
	isRun := sec.Header.Shell != ""
	switch {
	case st.Uses != "":
		if isRun {
			return false
		}
		if title == st.Uses || workflow.MatchesTemplate(st.Uses, title) {
			return true
		}
		// Local actions are logged as "owner/repo/path@sha".
		if local, ok := strings.CutPrefix(st.Uses, "./"); ok {
			return strings.Contains(title, "/"+strings.TrimSuffix(local, "/")+"@")
		}
	case st.Run != "":
		if !isRun {
			return false
		}
		first := firstLine(st.Run)
		return first == title || workflow.MatchesTemplate(first, title)
	}
	return false
}

// apiSteps pairs each workflow step with its API step, matching names in order.
func apiSteps(api []github.Step, steps []workflow.Step) []*github.Step {
	out := make([]*github.Step, len(steps))
	cursor := 0
	for wi, st := range steps {
		name := st.DisplayName()
		for j := cursor; j < len(api); j++ {
			if api[j].Name == name || workflow.MatchesTemplate(name, api[j].Name) {
				out[wi] = &api[j]
				cursor = j + 1
				break
			}
		}
	}
	return out
}

// reviewSteps raises issues about individual steps once all are known.
func (b *builder) reviewSteps() {
	for i, s := range b.plan.Steps {
		n := i + 1
		switch {
		case s.Target && s.Kind != KindRun:
			b.issue(SeverityBlocker, n, "the failing step runs the action %s; FailTrace can only replay `run:` steps so far", s.Uses)
		case s.Kind == KindUnsupported:
			b.issue(SeverityWarning, n, "%s is not replayed; later steps may depend on what it did", s.Uses)
		}
		if s.Kind == KindRun && strings.Contains(s.Script, "***") {
			b.issue(SeverityWarning, n, "the script contains values GitHub masked as secrets (***)")
		}
		if co := s.Checkout; co != nil {
			if co.MergeHead != "" {
				b.issue(SeverityInfo, n, "CI tested the merge of PR head %s into base %s (merge commit %s), not the PR branch alone; the plan checks out that merge commit",
					short(co.MergeHead), short(co.MergeBase), short(co.Commit))
			}
			if !strings.EqualFold(co.Repository, b.plan.Repository) {
				b.issue(SeverityInfo, n, "also checks out another repository: %s", co.Repository)
			}
		}
		if tc := s.Toolchain; tc != nil && tc.Version == "" {
			b.issue(SeverityWarning, n, "could not determine which %s version %s installed; the default will be used", tc.Tool, s.Uses)
		}
	}
	b.reviewToolUsage()
}

func indexOf(secs []*joblog.Section, target *joblog.Section) int {
	for i, s := range secs {
		if s == target {
			return i
		}
	}
	return len(secs) - 1
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(first)
}
