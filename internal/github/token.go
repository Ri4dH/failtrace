package github

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ErrNoToken is returned when no credentials can be found.
var ErrNoToken = errors.New("no GitHub token found: set GITHUB_TOKEN, or install the GitHub CLI and run `gh auth login`")

// ResolveToken finds a token from GITHUB_TOKEN, GH_TOKEN, or `gh auth token`.
func ResolveToken(ctx context.Context, host string) (string, error) {
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v, nil
		}
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return "", ErrNoToken
	}
	args := []string{"auth", "token"}
	if host != "" && host != "github.com" {
		args = append(args, "--hostname", host)
	}
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		return "", ErrNoToken
	}
	if tok := strings.TrimSpace(string(out)); tok != "" {
		return tok, nil
	}
	return "", ErrNoToken
}
