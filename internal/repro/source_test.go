package repro

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ri4dH/failtrace/internal/docker"
	"github.com/Ri4dH/failtrace/internal/plan"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeT(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// sourceRepo creates a working tree with every kind of local change.
func sourceRepo(t *testing.T) (dir, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir = t.TempDir()
	gitT(t, dir, "init", "-q")
	writeT(t, filepath.Join(dir, ".gitignore"), ".env\nnode_modules/\n", 0o644)
	writeT(t, filepath.Join(dir, "main.go"), "package main // v1\n", 0o644)
	writeT(t, filepath.Join(dir, "old.txt"), "to be deleted\n", 0o644)
	writeT(t, filepath.Join(dir, "service", "db.go"), "package service\n", 0o644)
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	head = gitT(t, dir, "rev-parse", "HEAD")

	writeT(t, filepath.Join(dir, "main.go"), "package main // fixed\n", 0o644) // modified
	if err := os.Remove(filepath.Join(dir, "old.txt")); err != nil {           // deleted
		t.Fatal(err)
	}
	writeT(t, filepath.Join(dir, "staged.go"), "package main // staged\n", 0o644) // added to the index
	gitT(t, dir, "add", "staged.go")
	writeT(t, filepath.Join(dir, "service", "new_test.go"), "package service\n", 0o644) // untracked
	writeT(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\n", 0o755)                       // untracked, executable
	writeT(t, filepath.Join(dir, ".env"), "SECRET=1\n", 0o644)                          // ignored
	writeT(t, filepath.Join(dir, "node_modules", "x.js"), "x\n", 0o644)                 // ignored
	return dir, head
}

func TestRunSource(t *testing.T) {
	dir, head := sourceRepo(t)
	before := gitT(t, dir, "status", "--porcelain")
	eng := &fakeEngine{steps: map[int]func(docker.ExecOptions, string) int{5: func(docker.ExecOptions, string) int { return 0 }}}
	var out bytes.Buffer
	res, err := Run(context.Background(), Deps{Engine: eng, Git: HostGit{}, Resolver: fakeResolver{}}, samplePlan(), ciFailure("TestIntegration"),
		Options{GitBase: "file:///nonexistent", Source: filepath.Join(dir, "service"), Keep: true, Out: &out, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}

	if res.Verdict != VerdictNotReproduced {
		t.Errorf("verdict = %s", res.Verdict)
	}
	top, _ := filepath.EvalSymlinks(dir)
	if res.Source == nil || res.Source.Head != head || res.Source.Changed != 5 {
		t.Fatalf("source = %+v, want head %s and 5 changed files", res.Source, head)
	}
	if got, _ := filepath.EvalSymlinks(res.Source.Dir); got != top {
		t.Errorf("source dir = %s, want the top level %s", res.Source.Dir, top)
	}

	ws := res.Workspace
	want := map[string]string{
		"main.go": "package main // fixed\n", "staged.go": "package main // staged\n",
		"service/db.go": "package service\n", "service/new_test.go": "package service\n", "run.sh": "#!/bin/sh\n",
	}
	for rel, content := range want {
		got, err := os.ReadFile(filepath.Join(ws, rel))
		if err != nil || string(got) != content {
			t.Errorf("%s = %q, %v; want %q", rel, got, err, content)
		}
	}
	for _, rel := range []string{"old.txt", ".env", "node_modules"} {
		if _, err := os.Lstat(filepath.Join(ws, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should not be in the workspace (err %v)", rel, err)
		}
	}
	if info, err := os.Stat(filepath.Join(ws, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh lost its executable bit: %v %v", info, err)
	}

	// The workspace is a clean checkout: the changes are a commit on top of HEAD.
	if st := gitT(t, ws, "status", "--porcelain"); st != "" {
		t.Errorf("workspace is not clean:\n%s", st)
	}
	if parent := gitT(t, ws, "rev-parse", "HEAD^"); parent != head {
		t.Errorf("HEAD^ = %s, want %s", parent, head)
	}
	if origin := gitT(t, ws, "remote", "get-url", "origin"); origin != "file:///nonexistent/acme/widget.git" {
		t.Errorf("origin = %s; it should stay the repository's remote, as in CI", origin)
	}

	// The working tree is untouched.
	if after := gitT(t, dir, "status", "--porcelain"); after != before {
		t.Errorf("working tree changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if refs := gitT(t, dir, "for-each-ref"); strings.Count(refs, "\n") != 0 {
		t.Errorf("refs were added to the working tree's repository:\n%s", refs)
	}

	diffs := strings.Join(res.Differences, "\n")
	if !strings.Contains(diffs, "local working tree ("+head[:7]+", plus 5 changed file(s))") ||
		!strings.Contains(diffs, "no remote for acme/widget") || strings.Contains(diffs, dir) {
		t.Errorf("differences = %s", diffs)
	}
	if !strings.Contains(out.String(), "Workspace   acme/widget from ") {
		t.Errorf("progress:\n%s", out.String())
	}
}

func TestRunSourceClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	writeT(t, filepath.Join(dir, "a.txt"), "a\n", 0o644)
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	gitT(t, dir, "remote", "add", "origin", "git@github.com:Acme/Widget.git")
	head := gitT(t, dir, "rev-parse", "HEAD")
	p := samplePlan()
	p.Commit = head

	eng := &fakeEngine{}
	var out bytes.Buffer
	res, err := Run(context.Background(), Deps{Engine: eng, Git: HostGit{}, Resolver: fakeResolver{}}, p, nil,
		Options{GitBase: "file:///nonexistent", Source: dir, Keep: true, Out: &out, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if res.Source.Changed != 0 || gitT(t, res.Workspace, "rev-parse", "HEAD") != head {
		t.Errorf("a clean tree should be checked out as its HEAD: %+v", res.Source)
	}
	if diffs := strings.Join(res.Differences, "\n"); strings.Contains(diffs, "no remote") ||
		!strings.Contains(diffs, "at the commit CI tested ("+head[:7]+"), no uncommitted changes") {
		t.Errorf("differences = %s", diffs)
	}
}

func TestRunSourceErrors(t *testing.T) {
	dir, _ := sourceRepo(t)
	other := samplePlan()
	other.Steps[0] = plan.Step{Name: "Run actions/checkout@v4", Kind: plan.KindSkip, Note: "checks out something else"}
	notRepo := t.TempDir()

	for name, tc := range map[string]struct {
		plan   *plan.Plan
		source string
		want   string
	}{
		"no checkout of the repository": {other, dir, "does not check out acme/widget"},
		"not a git repository":          {samplePlan(), notRepo, "is not inside a git repository"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Run(context.Background(), Deps{Engine: &fakeEngine{}, Git: HostGit{}, Resolver: fakeResolver{}}, tc.plan, nil,
				Options{GitBase: "file:///nonexistent", Source: tc.source, Out: &bytes.Buffer{}, WorkDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSyncPathSymlinks(t *testing.T) {
	top, dest, outside := t.TempDir(), t.TempDir(), t.TempDir()
	// In the working tree "lib" became a symlink; git also lists lib/a.go as deleted.
	if err := os.Symlink(outside, filepath.Join(top, "lib")); err != nil {
		t.Fatal(err)
	}
	writeT(t, filepath.Join(outside, "a.go"), "outside\n", 0o644)
	writeT(t, filepath.Join(dest, "lib", "a.go"), "tracked\n", 0o644)

	for _, rel := range []string{"lib", "lib/a.go"} {
		if _, err := syncPath(top, dest, rel); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(dest, "lib")); err != nil || target != outside {
		t.Errorf("lib = %q, %v; want a symlink to %s", target, err, outside)
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "a.go")); string(got) != "outside\n" {
		t.Errorf("a file outside the workspace was changed: %q", got)
	}
	// Never write through a symlink in the workspace.
	writeT(t, filepath.Join(top, "real", "b.go"), "b\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(dest, "real")); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPath(top, dest, "real/b.go"); err == nil {
		t.Error("expected an error writing through a symlink")
	}
	if _, err := syncPath(top, dest, "../escape"); err == nil {
		t.Error("expected an error for a path outside the tree")
	}
}
