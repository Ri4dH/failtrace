package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/repro"
)

func TestInspectRejectsBadInputBeforeNetwork(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"inspect"}, "accepts 1 arg"},
		{[]string{"inspect", "https://github.com/acme/widget/pull/1"}, "not a GitHub Actions run URL"},
		{[]string{"inspect", "--lines", "0", "https://github.com/acme/widget/actions/runs/1"}, "--lines must be between"},
		{[]string{"inspect", "--job", "-5", "https://github.com/acme/widget/actions/runs/1"}, "--job must be a positive"},
		{[]string{"plan"}, "accepts 1 arg"},
		{[]string{"plan", "https://gitlab.com/acme/widget/-/pipelines/1"}, "not a GitHub Actions run URL"},
		{[]string{"plan", "--job", "-1", "https://github.com/acme/widget/actions/runs/1"}, "--job must be a positive"},
		{[]string{"repro", "https://github.com/acme/widget/pull/1"}, "not a GitHub Actions run URL"},
		{[]string{"repro", "--secret", "TOKEN=abc", "https://github.com/acme/widget/actions/runs/1"}, "expected a variable name"},
		{[]string{"repro", "--secret", "FAILTRACE_TEST_UNSET_SECRET", "https://github.com/acme/widget/actions/runs/1"}, "is not set in your environment"},
		{[]string{"repro", "--toolchain", "node", "https://github.com/acme/widget/actions/runs/1"}, "expected tool=version"},
		{[]string{"repro", "--toolchain", "node=20;rm -rf /", "https://github.com/acme/widget/actions/runs/1"}, "expected tool=version"},
		{[]string{"repro", "--platform", "windows/amd64", "https://github.com/acme/widget/actions/runs/1"}, "--platform must be"},
		{[]string{"repro", "--timeout", "-1s", "https://github.com/acme/widget/actions/runs/1"}, "--timeout must be positive"},
		{[]string{"repro", "--runs", "0", "https://github.com/acme/widget/actions/runs/1"}, "--runs must be between 1 and 20"},
		{[]string{"repro", "--runs", "21", "https://github.com/acme/widget/actions/runs/1"}, "--runs must be between 1 and 20"},
		{[]string{"repro", "--shell", "--json", "https://github.com/acme/widget/actions/runs/1"}, "--shell cannot be combined"},
		{[]string{"repro", "--json", "--report", "-", "https://github.com/acme/widget/actions/runs/1"}, "pick one"},
		{[]string{"repro", "--report", "/nonexistent-dir-failtrace/r.md", "https://github.com/acme/widget/actions/runs/1"}, "does not exist"},
		{[]string{"repro", "--report", ".", "https://github.com/acme/widget/actions/runs/1"}, "is a directory"},
		{[]string{"repro", "--source", "/nonexistent-dir-failtrace", "https://github.com/acme/widget/actions/runs/1"}, "is not a directory"},
		{[]string{"repro", "--source", "inspect_test.go", "https://github.com/acme/widget/actions/runs/1"}, "is not a directory"},
	}
	for _, c := range cases {
		cmd := NewRootCmd(BuildInfo{Version: "test"})
		cmd.SetArgs(c.args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.ExecuteContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want containing %q", c.args, err, c.want)
		}
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := NewRootCmd(BuildInfo{Version: "v1.2.3", Commit: "0123456789abcdef0123", Date: "2026-11-18T00:00:00Z"})
	cmd.SetArgs([]string{"version"})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "failtrace v1.2.3 (0123456789ab, 2026-11-18T00:00:00Z) ") {
		t.Errorf("version = %q", out.String())
	}
	out.Reset()
	cmd = NewRootCmd(BuildInfo{Version: "v1.2.3"})
	cmd.SetArgs([]string{"--version"})
	cmd.SetOut(&out)
	_ = cmd.Execute()
	if !strings.HasPrefix(out.String(), "failtrace v1.2.3") {
		t.Errorf("--version = %q", out.String())
	}
}

func TestVerdictExitCodes(t *testing.T) {
	want := map[repro.Verdict]int{
		repro.VerdictNotReproduced: 0, repro.VerdictReproduced: 2, repro.VerdictDifferent: 3,
		repro.VerdictDiverged: 4, repro.VerdictFlaky: 5,
	}
	seen := map[int]bool{}
	for v, code := range want {
		var stderr bytes.Buffer
		if got := exitCode(verdictExit(v), &stderr); got != code || stderr.Len() != 0 {
			t.Errorf("%s: exit %d (stderr %q), want %d and nothing printed", v, got, stderr.String(), code)
		}
		if seen[code] {
			t.Errorf("exit code %d is used twice", code)
		}
		seen[code] = true
	}
	if len(verdictExitCodes) != len(want) {
		t.Errorf("verdictExitCodes has %d entries, want %d", len(verdictExitCodes), len(want))
	}
	if err := verdictExit("bogus"); err == nil {
		t.Error("an unknown verdict should be an error")
	}

	var stderr bytes.Buffer
	if got := exitCode(errors.New("boom"), &stderr); got != 1 || stderr.String() != "failtrace: boom\n" {
		t.Errorf("error: exit %d, stderr %q", got, stderr.String())
	}
	if got := exitCode(nil, &stderr); got != 0 {
		t.Errorf("success: exit %d", got)
	}
}
