package repro

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ri4dH/failtrace/internal/plan"
)

// checkoutAll replays every checkout step on the host, the way
// actions/checkout does: init, fetch the exact commit, check it out detached.
func (r *runner) checkoutAll(ctx context.Context) error {
	for _, s := range r.plan.Steps {
		if s.Kind != plan.KindCheckout || s.Checkout == nil {
			continue
		}
		if err := r.checkout(ctx, s.Checkout); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) checkout(ctx context.Context, co *plan.Checkout) error {
	rel := filepath.FromSlash(co.Path)
	if co.Path != "" && !filepath.IsLocal(rel) {
		return fmt.Errorf("checkout path %q escapes the workspace", co.Path)
	}
	dest := filepath.Join(r.hostWorkspace, rel)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	env := r.gitEnv()
	url := fmt.Sprintf("%s/%s.git", strings.TrimRight(r.opts.GitBase, "/"), co.Repository)
	target := firstNonEmpty(co.Commit, co.Ref, "HEAD")
	depth := []string{"--depth=1"}
	switch co.FetchDepth {
	case "0":
		depth = nil
	case "", "1":
	default:
		depth = []string{"--depth=" + co.FetchDepth}
	}

	steps := [][]string{
		{"init", "-q", dest},
		{"-C", dest, "remote", "add", "origin", url},
	}
	for _, args := range steps {
		if _, err := r.deps.Git.Run(ctx, "", env, args...); err != nil {
			return err
		}
	}
	if r.replacesCheckout(co) {
		if err := r.checkoutSource(ctx, dest, depth); err != nil {
			return err
		}
	} else if err := r.fetchCommit(ctx, dest, env, co, target, depth); err != nil {
		return err
	}

	switch co.Submodules {
	case "true", "recursive":
		args := []string{"-C", dest, "submodule", "update", "--init", "--depth=1"}
		if co.Submodules == "recursive" {
			args = append(args, "--recursive")
		}
		if _, err := r.deps.Git.Run(ctx, "", env, args...); err != nil {
			return err
		}
	}
	if co.LFS {
		if _, err := r.deps.Git.Run(ctx, "", env, "-C", dest, "lfs", "pull"); err != nil {
			r.differences = append(r.differences, "Git LFS files could not be fetched ("+err.Error()+")")
		}
	}
	return nil
}

// fetchCommit fetches the commit CI tested and checks it out.
func (r *runner) fetchCommit(ctx context.Context, dest string, env map[string]string, co *plan.Checkout, target string, depth []string) error {
	fetch := append([]string{"-C", dest, "fetch", "-q", "--no-tags", "--prune"}, depth...)
	_, err := r.deps.Git.Run(ctx, "", env, append(fetch, "origin", target)...)
	switch {
	case err == nil:
		_, err = r.deps.Git.Run(ctx, "", env, "-C", dest, "checkout", "-q", "--force", "--detach", "FETCH_HEAD")
		return err
	case co.MergeHead != "" && co.MergeBase != "":
		// GitHub replaces PR merge commits when the base branch moves; rebuild it.
		if mergeErr := r.recreateMerge(ctx, dest, env, co); mergeErr != nil {
			return fmt.Errorf("fetch %s@%s: %v; recreating the merge also failed: %w", co.Repository, short(target), err, mergeErr)
		}
		return nil
	}
	return fmt.Errorf("fetch %s@%s: %w", co.Repository, short(target), err)
}

func (r *runner) recreateMerge(ctx context.Context, dest string, env map[string]string, co *plan.Checkout) error {
	cmds := [][]string{
		{"-C", dest, "fetch", "-q", "--no-tags", "--depth=200", "origin", co.MergeBase, co.MergeHead},
		{"-C", dest, "checkout", "-q", "--force", "--detach", co.MergeBase},
		{"-C", dest, "-c", "user.name=FailTrace", "-c", "user.email=failtrace@users.noreply.github.com",
			"merge", "-q", "--no-edit", "--no-ff", co.MergeHead},
	}
	for _, args := range cmds {
		if _, err := r.deps.Git.Run(ctx, "", env, args...); err != nil {
			return err
		}
	}
	r.differences = append(r.differences, fmt.Sprintf(
		"the merge commit CI tested (%s) is no longer on GitHub, so the merge of %s into %s was recreated locally",
		short(co.Commit), short(co.MergeHead), short(co.MergeBase)))
	return nil
}

// gitEnv authenticates fetches through the environment, so the token is
// neither written to .git/config nor visible in the process list.
func (r *runner) gitEnv() map[string]string {
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	if r.opts.Token != "" {
		cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.opts.Token))
		env["GIT_CONFIG_COUNT"] = "1"
		env["GIT_CONFIG_KEY_0"] = "http.https://" + r.opts.Host + "/.extraheader"
		env["GIT_CONFIG_VALUE_0"] = "AUTHORIZATION: basic " + cred
	}
	return env
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func short(s string) string {
	if len(s) > 7 && !strings.ContainsAny(s, "/") {
		return s[:7]
	}
	return s
}
