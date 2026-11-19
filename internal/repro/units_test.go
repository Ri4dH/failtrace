package repro

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/joblog"
	"github.com/Ri4dH/failtrace/internal/plan"
)

func TestShellCommand(t *testing.T) {
	cases := []struct {
		shell string
		want  []string
	}{
		{"/usr/bin/bash -e {0}", []string{"bash", "-e", "/s"}},
		{"", []string{"bash", "-e", "/s"}},
		{"sh -e {0}", []string{"sh", "-e", "/s"}},
		{"python {0}", []string{"python", "/s"}},
		{"perl", []string{"perl", "/s"}},
	}
	for _, c := range cases {
		got, err := shellCommand(c.shell, "/s")
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("shellCommand(%q) = %q, %v; want %q", c.shell, got, err, c.want)
		}
	}
	for _, bad := range []string{`C:\Program Files\PowerShell\7\pwsh.EXE -command ". '{0}'"`, "pwsh -command {0}", "cmd /D /E:ON {0}"} {
		if _, err := shellCommand(bad, "/s"); err == nil {
			t.Errorf("shellCommand(%q) should fail", bad)
		}
	}
	if scriptExt("python {0}") != ".py" || scriptExt("/usr/bin/bash -e {0}") != ".sh" {
		t.Error("scriptExt")
	}
}

func TestParseEnvFile(t *testing.T) {
	got, err := parseEnvFile("A=1\nB=x=y\r\n\nC<<ghadelim_1\nline1\nline2\nghadelim_1\nD=\n")
	want := map[string]string{"A": "1", "B": "x=y", "C": "line1\nline2", "D": ""}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("parseEnvFile = %v, %v", got, err)
	}
	if _, err := parseEnvFile("C<<EOF\nnever closed\n"); err == nil {
		t.Error("expected an error for an unterminated heredoc")
	}
	if _, err := parseEnvFile("not a valid line\n"); err == nil {
		t.Error("expected an error for a line without =")
	}
	if got := parsePathFile(" /a/bin \n\n/b\n"); !reflect.DeepEqual(got, []string{"/a/bin", "/b"}) {
		t.Errorf("parsePathFile = %q", got)
	}
}

func TestDropNetworkFlags(t *testing.T) {
	in := []string{"--health-cmd", "pg_isready", "-p", "5432:5432", "--publish=80:80", "--network", "x", "--name=y", "--health-retries", "5"}
	want := []string{"--health-cmd", "pg_isready", "--health-retries", "5"}
	if got := dropNetworkFlags(in); !reflect.DeepEqual(got, want) {
		t.Errorf("dropNetworkFlags = %q", got)
	}
}

func TestCompare(t *testing.T) {
	code := func(n int) *int { return &n }
	cases := []struct {
		name  string
		ci    *joblog.Failure
		local *joblog.Failure
		exit  int
		want  Verdict
	}{
		{"passed", &joblog.Failure{ExitCode: code(1)}, &joblog.Failure{}, 0, VerdictNotReproduced},
		{"same tests", &joblog.Failure{FailedTests: []string{"A", "B"}}, &joblog.Failure{FailedTests: []string{"B", "A"}}, 1, VerdictReproduced},
		{"overlapping tests", &joblog.Failure{FailedTests: []string{"A", "B"}}, &joblog.Failure{FailedTests: []string{"A"}}, 1, VerdictReproduced},
		{"other tests", &joblog.Failure{FailedTests: []string{"A"}}, &joblog.Failure{FailedTests: []string{"C"}}, 1, VerdictDifferent},
		{"same signature", &joblog.Failure{ExitCode: code(2), Signature: "make: *** [x] Error <n>"}, &joblog.Failure{Signature: "make: *** [x] Error <n>"}, 1, VerdictReproduced},
		{"same exit and category", &joblog.Failure{ExitCode: code(1), Category: joblog.CategoryBuild}, &joblog.Failure{Category: joblog.CategoryBuild}, 1, VerdictReproduced},
		{"same exit only", &joblog.Failure{ExitCode: code(1), Category: joblog.CategoryUnknown}, &joblog.Failure{Category: joblog.CategoryUnknown}, 1, VerdictReproduced},
		{"different exit", &joblog.Failure{ExitCode: code(1), Category: joblog.CategoryTest}, &joblog.Failure{Category: joblog.CategoryBuild}, 2, VerdictDifferent},
		{"no CI details", nil, &joblog.Failure{}, 3, VerdictReproduced},
	}
	for _, c := range cases {
		if got, reasons := compare(c.ci, c.local, c.exit); got != c.want || len(reasons) == 0 {
			t.Errorf("%s: compare = %s %q, want %s", c.name, got, reasons, c.want)
		}
	}
}

func TestAnalyzeLocalSignatureMatchesCI(t *testing.T) {
	ci := joblog.Analyze(&joblog.Section{
		Body:   []string{"go vet ./...", "./a.go:12:3: printf call has arguments but no formatting directives"},
		Errors: []string{"Process completed with exit code 1."},
	}, 10)
	local := analyzeLocal("vet", []string{"go vet ./...", "./a.go:12:3: printf call has arguments but no formatting directives"}, 1)
	if ci.Signature == "" || ci.Signature != local.Signature {
		t.Errorf("signatures differ: %q vs %q", ci.Signature, local.Signature)
	}
}

func TestPlanImage(t *testing.T) {
	p := samplePlan()
	img, err := planImage(context.Background(), fakeResolver{}, p, "linux/amd64", map[string]string{"node": "20.11.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(img.Installs) != 2 || img.Installs[0].Tool != "go" || img.Installs[1].Tool != "node" {
		t.Errorf("installs = %+v", img.Installs)
	}
	if len(img.Missing) != 1 || !strings.Contains(img.Missing[0], "java") {
		t.Errorf("missing = %q", img.Missing)
	}
	df := img.Dockerfile
	for _, want := range []string{"FROM ubuntu:24.04", "apt-get install", "build-essential", "RUN install go", "RUN install node",
		`ENV PATH="/opt/hostedtoolcache/node/20.11.1/x64/bin:/opt/hostedtoolcache/go/1.22.7/x64/bin:$PATH"`} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile missing %q:\n%s", want, df)
		}
	}
	again, _ := planImage(context.Background(), fakeResolver{}, p, "linux/amd64", map[string]string{"node": "20.11.1"})
	other, _ := planImage(context.Background(), fakeResolver{}, p, "linux/arm64", map[string]string{"node": "20.11.1"})
	if again.Tag != img.Tag || other.Tag == img.Tag {
		t.Errorf("tags should be stable per content and platform: %s %s %s", img.Tag, again.Tag, other.Tag)
	}

	// A job container without toolchains is used as is.
	p.Image = plan.Image{Ref: "golang:1.22", Exact: true}
	p.Toolchains = nil
	img, _ = planImage(context.Background(), fakeResolver{}, p, "linux/amd64", nil)
	if img.Tag != "golang:1.22" || img.Dockerfile != "" {
		t.Errorf("exact image = %+v", img)
	}
	// With toolchains, it only gets curl/tar if missing, not runner packages.
	p.Toolchains = []plan.Toolchain{{Tool: "go", Version: "1.23.1"}}
	img, _ = planImage(context.Background(), fakeResolver{}, p, "linux/amd64", nil)
	if !strings.Contains(img.Dockerfile, "command -v curl") || strings.Contains(img.Dockerfile, "build-essential") {
		t.Errorf("exact image with toolchain:\n%s", img.Dockerfile)
	}
	// Unknown versions are reported, not guessed.
	p.Toolchains = []plan.Toolchain{{Tool: "uv"}}
	img, _ = planImage(context.Background(), fakeResolver{}, p, "linux/amd64", nil)
	if len(img.Missing) != 1 || !strings.Contains(img.Missing[0], "--toolchain uv=") {
		t.Errorf("missing = %q", img.Missing)
	}
}

func TestLineCollectorAndPrefixWriter(t *testing.T) {
	c := newLineCollector(2)
	_, _ = c.Write([]byte("a\nb\x1b[31m!\x1b[0m\nc"))
	if got := c.Lines(); !reflect.DeepEqual(got, []string{"b!", "c"}) {
		t.Errorf("lines = %q", got)
	}
	var b strings.Builder
	w := newPrefixWriter(&b, "> ")
	_, _ = w.Write([]byte("one\ntw"))
	_, _ = w.Write([]byte("o\nthree"))
	w.Flush()
	if b.String() != "> one\n> two\n> three\n" {
		t.Errorf("prefixed = %q", b.String())
	}
}
