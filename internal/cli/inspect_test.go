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
