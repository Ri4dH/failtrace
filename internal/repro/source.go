package repro

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Ri4dH/failtrace/internal/plan"
)

// Source describes the local working tree used instead of CI's commit
// (Options.Source).
type Source struct {
	Dir     string `json:"dir"`           // top level of the working tree
	Head    string `json:"head"`          // the commit checked out there
	Changed int    `json:"changed_files"` // files that differ from Head: modified, added, deleted or untracked
}

// replacesCheckout reports whether co is the checkout that Options.Source
// replaces: the first checkout of the job's own repository.
func (r *runner) replacesCheckout(co *plan.Checkout) bool {
	return r.opts.Source != "" && r.source == nil && strings.EqualFold(co.Repository, r.plan.Repository)
}

// checkoutSource fills dest with the working tree at Options.Source: its HEAD
// commit, plus a commit of its uncommitted and untracked changes (ignored
// files excluded), so the workspace is clean the way a CI checkout is. The
// working tree itself is only read.
func (r *runner) checkoutSource(ctx context.Context, dest string, depth []string) error {
	git := r.deps.Git
	out, err := git.Run(ctx, "", nil, "-C", r.opts.Source, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("--source %s is not inside a git repository: %w", r.opts.Source, err)
	}
	top := strings.TrimSpace(out)
	out, err = git.Run(ctx, "", nil, "-C", top, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("--source %s has no commits yet: %w", top, err)
	}
	src := &Source{Dir: top, Head: strings.TrimSpace(out)}

	fetch := append([]string{"-C", dest, "fetch", "-q", "--no-tags"}, depth...)
	if _, err := git.Run(ctx, "", nil, append(fetch, top, "HEAD")...); err != nil {
		return fmt.Errorf("copy %s: %w", top, err)
	}
	if _, err := git.Run(ctx, "", nil, "-C", dest, "checkout", "-q", "--force", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}

	paths, err := changedPaths(ctx, git, top)
	if err != nil {
		return err
	}
	for _, rel := range paths {
		changed, err := syncPath(top, dest, rel)
		if err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
		if changed {
			src.Changed++
		}
	}
	if src.Changed > 0 {
		commit := []string{"-C", dest, "-c", "user.name=FailTrace", "-c", "user.email=failtrace@users.noreply.github.com",
			"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}
		for _, args := range [][]string{
			append(commit, "add", "-A"),
			append(commit, "commit", "-q", "--no-verify", "--allow-empty", "-m", "failtrace: uncommitted changes from the working tree"),
		} {
			if _, err := git.Run(ctx, "", nil, args...); err != nil {
				return err
			}
		}
	}
	r.source = src
	r.noteSource(ctx, src)
	return nil
}

func (r *runner) noteSource(ctx context.Context, src *Source) {
	changes := "no uncommitted changes"
	if src.Changed > 0 {
		changes = fmt.Sprintf("plus %d changed file(s)", src.Changed)
	}
	r.logf("Workspace", "%s from %s (%s, %s)", r.plan.Repository, src.Dir, short(src.Head), changes)
	// No local paths here: differences end up in reports people share.
	if src.Head == r.plan.Commit {
		r.differences = append(r.differences, fmt.Sprintf(
			"the code is a local working tree at the commit CI tested (%s), %s", short(src.Head), changes))
	} else {
		r.differences = append(r.differences, fmt.Sprintf(
			"the code is a local working tree (%s, %s), not the commit CI tested (%s); GITHUB_SHA still names CI's commit",
			short(src.Head), changes, short(r.plan.Commit)))
	}
	remotes, _ := r.deps.Git.Run(ctx, "", nil, "-C", src.Dir, "remote", "-v")
	if !strings.Contains(strings.ToLower(remotes), strings.ToLower(r.plan.Repository)) {
		r.differences = append(r.differences, "the working tree has no remote for "+r.plan.Repository+"; check that it is the right repository")
	}
}

// changedPaths lists the files in the working tree at top that differ from
// HEAD (staged or not, including deletions) and the untracked files that are
// not ignored, sorted so a directory comes before the files in it.
func changedPaths(ctx context.Context, git Git, top string) ([]string, error) {
	diff, err := git.Run(ctx, "", nil, "-C", top, "diff", "--name-only", "-z", "--no-renames", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	untracked, err := git.Run(ctx, "", nil, "-C", top, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range strings.Split(diff+"\x00"+untracked, "\x00") {
		p = strings.TrimSuffix(p, "/") // nested repositories are listed as dir/
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// syncPath makes rel in dest match rel in top: copied, or removed if it was
// deleted. Directories (submodules and nested repositories) are left alone.
// It reports whether anything changed.
func syncPath(top, dest, rel string) (bool, error) {
	rel = filepath.FromSlash(rel)
	if !filepath.IsLocal(rel) {
		return false, fmt.Errorf("unexpected path from git")
	}
	if throughSymlink(top, rel) {
		return false, nil // the symlink itself is synced as its own entry
	}
	if throughSymlink(dest, rel) {
		return false, fmt.Errorf("refusing to write through a symbolic link")
	}
	from, to := filepath.Join(top, rel), filepath.Join(dest, rel)
	info, err := os.Lstat(from)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, os.RemoveAll(to)
	case err != nil:
		return false, err
	case info.IsDir():
		if l, err := os.Lstat(to); err == nil && l.Mode()&fs.ModeSymlink != 0 {
			return true, os.Remove(to) // a symlink in HEAD became a directory
		}
		return false, nil
	}
	if err := os.RemoveAll(to); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return false, err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(from)
		if err != nil {
			return false, err
		}
		return true, os.Symlink(target, to)
	case info.Mode().IsRegular():
		return true, copyFile(from, to, info.Mode().Perm())
	}
	return false, nil // sockets, devices: nothing git would track
}

// throughSymlink reports whether a parent directory of rel under root is a
// symbolic link.
func throughSymlink(root, rel string) bool {
	dir := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if err != nil {
			return false
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func copyFile(from, to string, perm fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
