package report

import (
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/plan"
	"github.com/Ri4dH/failtrace/internal/repro"
)

func TestWriteMarkdown(t *testing.T) {
	p := samplePlan()
	p.RunURL = "https://github.com/acme/widget/actions/runs/42"
	p.Job.URL = "https://github.com/acme/widget/actions/runs/42/job/7"
	one := 1
	res := &repro.Result{
		Verdict:       repro.VerdictFlaky,
		Reasons:       []string{"the step failed in 2 of 3 runs", "the same tests failed: Test|Pipe"},
		CI:            &joblog.Failure{ExitCode: &one, Category: joblog.CategoryTest, FailedTests: []string{"Test|Pipe"}, Excerpt: []string{"--- FAIL: Test|Pipe"}},
		Local:         &joblog.Failure{ExitCode: &one, Category: joblog.CategoryTest, FailedTests: []string{"Test|Pipe"}},
		LocalExitCode: &one,
		Attempts:      []repro.Attempt{{Run: 1}, {Run: 2, ExitCode: 1}, {Run: 3, ExitCode: 1}},
		Steps: []repro.StepResult{
			{Index: 1, Name: "Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", Status: repro.StepPassed},
			{Index: 2, Name: "Run actions/cache@v4", Status: repro.StepSkipped, Note: "cache only"},
			{Index: 4, Name: "Integration tests", Status: repro.StepFailed, ExitCode: &one, Seconds: 3.25, Target: true,
				Output: []string{"```go", "--- FAIL: Test|Pipe", "```"}},
		},
		Platform:    "linux/arm64",
		Differences: []string{"steps run as root; on GitHub's runners they run as the `runner` user"},
		Seconds:     42,
	}
	var b strings.Builder
	if err := WriteMarkdown(&b, p, res); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	for _, want := range []string{
		"## FailTrace: 🎲 Flaky",
		"The step **Integration tests** of job [`test (1.22)`](https://github.com/acme/widget/actions/runs/42/job/7) in `acme/widget` fails only some of the time ([CI run](https://github.com/acme/widget/actions/runs/42)).",
		"- the same tests failed: Test\\|Pipe",
		"| Exit code | 1 | 1 |",
		"| Failed tests | `Test\\|Pipe` | `Test\\|Pipe` |",
		"| Runs | failed 1 of 1 | failed 2 of 3 |",
		"- **Image:** `ubuntu:24.04` on `linux/arm64` (CI: `linux/amd64`)",
		"- **Commit:** `1111111`, the merge of PR head `2222222` into `3333333` that CI tested",
		"- **Toolchains:** go `1.22.7`",
		"- **Services:** postgres (`postgres:16`)",
		"- steps run as root; on GitHub's runners they run as the `runner` user",
		"| 1 | Run actions/checkout@3d3c42e | passed |  |",
		"| 2 | Run actions/cache@v4 | skipped: cache only |  |",
		"| 4 | **Integration tests** ← failed in CI | failed (exit 1) | 3.2s |",
		"<summary>Local output of the failing step (last 3 lines)</summary>",
		"````text\n```go\n--- FAIL: Test|Pipe\n```\n````",
		"<summary>CI output of the failing step (excerpt)</summary>",
		"`failtrace repro https://github.com/acme/widget/actions/runs/42`",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

func TestMarkdownEscaping(t *testing.T) {
	cases := map[string]string{
		"a *b* _c_ <script>":         `a \*b\* \_c\_ \<script\>`,
		"keep `code_with_*` as is":   "keep `code_with_*` as is",
		"unbalanced ` tick_here":     "unbalanced \\` tick\\_here",
		"[link](x) # heading | pipe": `\[link\](x) \# heading \| pipe`,
	}
	for in, want := range cases {
		if got := mdText(in); got != want {
			t.Errorf("mdText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mdCode("a`b"); got != "``a`b``" {
		t.Errorf("mdCode = %q", got)
	}
	if got := mdCell("a|b\nc"); got != `a\|b c` {
		t.Errorf("mdCell = %q", got)
	}
}

func TestWriteMarkdownWithoutDetails(t *testing.T) {
	res := &repro.Result{Verdict: repro.VerdictNotReproduced, Reasons: []string{"the step that failed in CI passed locally"}}
	var b strings.Builder
	if err := WriteMarkdown(&b, &plan.Plan{Repository: "acme/widget", Job: plan.JobRef{Name: "build"}}, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "## FailTrace: ❌ Not reproduced") || strings.Contains(b.String(), "<details>") {
		t.Errorf("markdown:\n%s", b.String())
	}
}

func TestWriteMarkdownSource(t *testing.T) {
	res := &repro.Result{
		Verdict: repro.VerdictNotReproduced,
		Source:  &repro.Source{Dir: "/home/me/widget", Head: "abcdef0123456789", Changed: 2},
	}
	var b strings.Builder
	if err := WriteMarkdown(&b, samplePlan(), res); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	for _, want := range []string{
		"## FailTrace: ✅ Passes with local changes",
		"passes with a local working tree",
		"- **Code:** a local working tree at `abcdef0` plus 2 changed file(s), not the commit above",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
	if strings.Contains(md, "/home/me") {
		t.Errorf("the report should not contain local paths:\n%s", md)
	}
}
