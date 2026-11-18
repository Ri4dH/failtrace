package report

import (
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/plan"
)

func samplePlan() *plan.Plan {
	return &plan.Plan{
		Repository: "acme/widget",
		Job:        plan.JobRef{Name: "test (1.22)", WorkflowJob: "test", Matrix: map[string]string{"go": "1.22"}},
		Verdict:    plan.VerdictPartial,
		Image:      plan.Image{Ref: "ubuntu:24.04", Platform: "linux/amd64", Source: "matches the runner image ubuntu-24.04"},
		Workspace:  "/home/runner/work/widget/widget",
		Commit:     "1111111111111111111111111111111111111111",
		Toolchains: []plan.Toolchain{{Tool: "go", Version: "1.22.7", Spec: "1.22", Exact: true, Source: "actions/setup-go@0aaccfd150d50ccaeb58ebd88d36e91967a5f35b"}},
		Services:   []plan.Service{{Name: "postgres", Image: "postgres:16", Ports: []string{"5432:5432"}}},
		Env: []plan.EnvVar{
			{Name: "CI", Value: "true", Source: "github"},
			{Name: "GOFLAGS", Value: "-mod=readonly", Source: "workflow"},
			{Name: "API_TOKEN", Source: "job", Secret: true},
		},
		Steps: []plan.Step{
			{Name: "Run actions/checkout@v4", Kind: plan.KindCheckout, Checkout: &plan.Checkout{
				Repository: "acme/widget", Commit: "1111111111111111111111111111111111111111",
				MergeHead: "2222222222222222222222222222222222222222", MergeBase: "3333333333333333333333333333333333333333"}},
			{Name: "Run ./.github/actions/setup", Kind: plan.KindComposite, Uses: "./.github/actions/setup", Note: "composite action; its steps follow"},
			{Name: "Run make tools", Kind: plan.KindRun, Script: "make tools\nmake more", Parent: 2},
			{Name: "Integration tests", Kind: plan.KindRun, Script: "go test ./...\ngo vet ./...", Shell: "bash -e {0}",
				WorkingDirectory: "./service", Env: map[string]string{"DATABASE_URL": "postgres://x"}, Target: true},
		},
		Issues: []plan.Issue{
			{Severity: plan.SeverityWarning, Message: "needs secrets: API_TOKEN"},
			{Severity: plan.SeverityInfo, Step: 1, Message: "CI tested a merge commit"},
		},
	}
}

func TestWritePlan(t *testing.T) {
	var b strings.Builder
	if err := WritePlan(&b, samplePlan(), TextOptions{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"Reproduction plan · acme/widget",
		"test (1.22)  (jobs.test · go=1.22)",
		"PARTIAL",
		"ubuntu:24.04  linux/amd64",
		"Commit     1111111",
		"go       1.22.7     exact · asked for \"1.22\" · from actions/setup-go@0aaccfd",
		"postgres   postgres:16  ports 5432:5432",
		"GOFLAGS=-mod=readonly  workflow",
		"API_TOKEN=<secret>  job",
		"+ 1 GitHub defaults (CI, …; see --json)",
		"acme/widget @ 1111111 (merge of 2222222 into 3333333)",
		"    3    run         $ make tools  (+1 lines)",
		"Integration tests  ← failed in CI",
		"$ go test ./...",
		"$ go vet ./...",
		"working-directory: ./service",
		"env: DATABASE_URL=postgres://x",
		"! needs secrets: API_TOKEN",
		"i [step 1] CI tested a merge commit",
		"Dry run: nothing was executed.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("output contains ANSI escapes with color disabled")
	}
}

func TestShortenRefs(t *testing.T) {
	in := "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 and actions/cache@v4"
	if got := shortenRefs(in); got != "actions/checkout@3d3c42e and actions/cache@v4" {
		t.Errorf("shortenRefs = %q", got)
	}
}
