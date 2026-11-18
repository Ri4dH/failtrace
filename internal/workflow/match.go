package workflow

import (
	"regexp"
	"strings"
)

// JobMatch is the workflow job that produced an API job, plus the matrix
// combination recovered from the job's display name.
type JobMatch struct {
	Job       *Job              `json:"-"`
	Matrix    map[string]string `json:"matrix,omitempty"`
	MatrixRaw []string          `json:"matrix_raw,omitempty"` // values we could not map to keys
	Guessed   bool              `json:"guessed,omitempty"`    // fell back to the only job in the file
}

var (
	exprRe      = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	matrixRefRe = regexp.MustCompile(`^matrix\.([A-Za-z0-9_-]+)$`)
	parensRe    = regexp.MustCompile(`^(.+?) \((.+)\)$`)
)

// setupStepNames are runner-generated steps that precede the workflow's own steps.
var setupStepNames = map[string]bool{"Set up job": true, "Initialize containers": true}

// MatchJob finds the job definition for an API job display name such as
// "test (ubuntu-latest, 1.22)", "Lint golangci" or "deploy / publish".
func (w *Workflow) MatchJob(displayName string) *JobMatch {
	candidates := []string{displayName}
	if caller, _, ok := strings.Cut(displayName, " / "); ok {
		candidates = append(candidates, caller) // reusable workflow: "caller / callee"
	}
	for _, c := range candidates {
		if m := w.matchCandidate(c); m != nil {
			return m
		}
	}
	if len(w.Jobs) == 1 {
		return &JobMatch{Job: w.Jobs[0], Guessed: true}
	}
	return nil
}

func (w *Workflow) matchCandidate(name string) *JobMatch {
	for _, j := range w.Jobs {
		if j.ID == name || (j.Name != "" && j.Name == name) {
			return &JobMatch{Job: j}
		}
	}
	for _, j := range w.Jobs {
		if vals, ok := matchTemplate(j.Name, name); ok {
			return &JobMatch{Job: j, Matrix: vals}
		}
	}
	sub := parensRe.FindStringSubmatch(name)
	if sub == nil {
		return nil
	}
	base, values := sub[1], strings.Split(sub[2], ", ")
	for _, j := range w.Jobs {
		named := j.ID == base || (j.Name != "" && j.Name == base)
		tplVals, tplOK := matchTemplate(j.Name, base)
		if !named && !tplOK {
			continue
		}
		m := &JobMatch{Job: j}
		if j.Matrix != nil && len(j.Matrix.Keys) == len(values) {
			m.Matrix = make(map[string]string, len(values))
			for i, k := range j.Matrix.Keys {
				m.Matrix[k] = values[i]
			}
		} else {
			m.MatrixRaw = values
		}
		for k, v := range tplVals {
			if m.Matrix == nil {
				m.Matrix = map[string]string{}
			}
			m.Matrix[k] = v
		}
		return m
	}
	return nil
}

// MatchesTemplate reports whether name is an evaluation of tpl, a string
// containing ${{ }} expressions (e.g. "Lint ${{ matrix.tool }}").
func MatchesTemplate(tpl, name string) bool {
	_, ok := matchTemplate(tpl, name)
	return ok
}

// matchTemplate matches a name containing ${{ }} expressions against an
// evaluated name and returns the values bound to ${{ matrix.<key> }}.
func matchTemplate(tpl, name string) (map[string]string, bool) {
	if !strings.Contains(tpl, "${{") {
		return nil, false
	}
	var b strings.Builder
	b.WriteString("^")
	var keys []string
	last := 0
	for _, loc := range exprRe.FindAllStringSubmatchIndex(tpl, -1) {
		b.WriteString(regexp.QuoteMeta(tpl[last:loc[0]]))
		b.WriteString("(.*?)")
		key := ""
		if mm := matrixRefRe.FindStringSubmatch(tpl[loc[2]:loc[3]]); mm != nil {
			key = mm[1]
		}
		keys = append(keys, key)
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(tpl[last:]))
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, false
	}
	sub := re.FindStringSubmatch(name)
	if sub == nil {
		return nil, false
	}
	vals := map[string]string{}
	for i, k := range keys {
		if k != "" {
			vals[k] = sub[i+1]
		}
	}
	return vals, true
}

// MatchStep returns the index in j.Steps of the API step with the given name
// and 1-based number, or -1. apiSteps are all API step names in order.
func (j *Job) MatchStep(apiName string, apiNumber int, apiSteps []string) int {
	for i, s := range j.Steps {
		if s.DisplayName() == apiName {
			return i
		}
	}
	for i, s := range j.Steps {
		if _, ok := matchTemplate(s.Name, apiName); ok {
			return i
		}
	}
	leading := 0
	for _, n := range apiSteps {
		if !setupStepNames[n] {
			break
		}
		leading++
	}
	if idx := apiNumber - 1 - leading; idx >= 0 && idx < len(j.Steps) {
		return idx
	}
	return -1
}
