package joblog

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) *Log {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return Parse(string(data))
}

func TestParseSectionsAndRunner(t *testing.T) {
	lg := loadFixture(t, "go_test_failure.log")

	var titles []string
	for _, s := range lg.Sections {
		titles = append(titles, s.Title)
	}
	want := []string{"Set up job", "Run actions/checkout@v4", "Run actions/setup-go@v5", "Run go test ./...", "Post job cleanup"}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles = %q, want %q", titles, want)
	}

	wantRunner := RunnerInfo{Version: "2.338.2", Image: "ubuntu-24.04", ImageVersion: "20261111.196.1"}
	if lg.Runner != wantRunner {
		t.Errorf("runner = %+v, want %+v", lg.Runner, wantRunner)
	}

	checkout := lg.Sections[1]
	if checkout.Header.With["repository"] != "acme/widget" {
		t.Errorf("checkout with = %v", checkout.Header.With)
	}
	if !lg.Sections[4].Post {
		t.Error("last section should be a post section")
	}
}

func TestParseRunStepHeader(t *testing.T) {
	sec := loadFixture(t, "go_test_failure.log").FailedSection()
	if sec == nil || sec.Title != "Run go test ./..." {
		t.Fatalf("failed section = %+v", sec)
	}
	h := sec.Header
	if !reflect.DeepEqual(h.Script, []string{"go test ./..."}) {
		t.Errorf("script = %q", h.Script)
	}
	if h.Shell != "/usr/bin/bash -e {0}" {
		t.Errorf("shell = %q", h.Shell)
	}
	if h.Env["CGO_ENABLED"] != "0" {
		t.Errorf("env = %v", h.Env)
	}
}

func TestFailedSectionPrefersExitCode(t *testing.T) {
	raw := "##[group]Run a\na\n##[endgroup]\n##[error]some annotation\n" +
		"##[group]Run b\nb\n##[endgroup]\n##[error]Process completed with exit code 2.\n"
	sec := Parse(raw).FailedSection()
	if sec == nil || sec.Title != "Run b" {
		t.Fatalf("got %+v, want section 'Run b'", sec)
	}
}

func TestFailedSectionNone(t *testing.T) {
	if sec := Parse("##[group]Run ok\nok\n##[endgroup]\nall good\n").FailedSection(); sec != nil {
		t.Fatalf("expected no failed section, got %q", sec.Title)
	}
}

func TestClean(t *testing.T) {
	cases := map[string]string{
		"2026-11-17T10:00:03.1234567Z \x1b[36;1mgo test ./...\x1b[0m": "go test ./...",
		"\x1b[31mred\x1b[0m\r": "red",
		"no timestamp":         "no timestamp",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseStripsBOM(t *testing.T) {
	lg := Parse("\uFEFF2026-11-17T10:00:00.0000000Z Current runner version: '2.1.0'\n")
	if lg.Runner.Version != "2.1.0" {
		t.Fatalf("runner version = %q", lg.Runner.Version)
	}
}

func TestParseMultiLineEnv(t *testing.T) {
	raw := strings.Join([]string{
		"2026-11-19T07:34:41.2096450Z ##[group]Run python -m check",
		"2026-11-19T07:34:41.2096450Z \x1b[36;1mpython -m check\x1b[0m",
		"2026-11-19T07:34:41.2152441Z shell: /usr/bin/bash -e {0}",
		"2026-11-19T07:34:41.2153144Z env:",
		"2026-11-19T07:34:41.2153577Z   PYTEST_ADDOPTS: --color=yes",
		"2026-11-19T07:34:41.2155275Z   INPUT_JOBS: {",
		`  "build": {`,
		`    "result": "failure"`,
		"  }",
		"}",
		"2026-11-19T07:34:41.2156357Z   EMPTY:",
		"2026-11-19T07:34:41.2157262Z ##[endgroup]",
		"2026-11-19T07:34:41.2657820Z output",
	}, "\n")
	env := Parse(raw).Sections[1].Header.Env
	want := map[string]string{
		"PYTEST_ADDOPTS": "--color=yes",
		"INPUT_JOBS":     "{\n  \"build\": {\n    \"result\": \"failure\"\n  }\n}",
		"EMPTY":          "",
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env = %q\nwant %q", env, want)
	}
}
