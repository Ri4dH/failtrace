package inspect

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Ri4dH/failtrace/internal/workflow"
)

var secretRefRe = regexp.MustCompile(`\bsecrets\.([A-Za-z_][A-Za-z0-9_]*)`)

// notes lists facts that affect whether the failure can be reproduced locally.
func notes(job *workflow.Job, step *workflow.Step, labels []string) []string {
	var out []string
	for _, l := range labels {
		ll := strings.ToLower(l)
		if strings.Contains(ll, "macos") || strings.Contains(ll, "windows") {
			out = append(out, fmt.Sprintf("runs on %s: a local Linux container can only approximate this runner", l))
			break
		}
	}
	if job == nil {
		return out
	}
	if job.Uses != "" {
		out = append(out, fmt.Sprintf("job calls reusable workflow %s; step details may be incomplete", job.Uses))
	}
	if job.Container != "" {
		out = append(out, fmt.Sprintf("job runs inside container %s", job.Container))
	}
	if len(job.Services) > 0 {
		svcs := make([]string, 0, len(job.Services))
		for _, s := range job.Services {
			if s.Image != "" {
				svcs = append(svcs, fmt.Sprintf("%s (%s)", s.Name, s.Image))
			} else {
				svcs = append(svcs, s.Name)
			}
		}
		out = append(out, "job uses service containers: "+strings.Join(svcs, ", "))
	}
	if step != nil {
		texts := []string{step.Run}
		texts = appendValues(texts, step.With)
		texts = appendValues(texts, step.Env)
		texts = appendValues(texts, job.Env)
		if s := secretRefs(texts); len(s) > 0 {
			out = append(out, "failing step references secrets not available locally: "+strings.Join(s, ", "))
		}
	}
	return out
}

func appendValues(dst []string, m map[string]string) []string {
	for _, v := range m {
		dst = append(dst, v)
	}
	return dst
}

func secretRefs(texts []string) []string {
	seen := map[string]bool{}
	for _, t := range texts {
		for _, m := range secretRefRe.FindAllStringSubmatch(t, -1) {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
