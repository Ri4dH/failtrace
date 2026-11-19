package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
	}
	for _, c := range cases {
		cmd := NewRootCmd("test")
		cmd.SetArgs(c.args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.ExecuteContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want containing %q", c.args, err, c.want)
		}
	}
}
