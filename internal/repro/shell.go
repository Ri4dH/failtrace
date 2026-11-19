package repro

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Ri4dH/failtrace/internal/docker"
)

// ShellIO connects --shell to the user's terminal.
type ShellIO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	TTY bool // allocate a pseudo-terminal (stdin and stdout are terminals)
}

// openShell starts an interactive shell where the failing step ran (or where
// a setup step diverged), with that step's environment.
func (r *runner) openShell(ctx context.Context) error {
	term := r.opts.ShellIO
	if term.In == nil || term.Out == nil {
		return errors.New("--shell needs a terminal")
	}
	if r.shellIndex == 0 {
		return errors.New("no step ran, so there is nowhere to open a shell")
	}
	s, n := r.shellStep, r.shellIndex
	script, err := r.scriptPath(n, s)
	if err != nil {
		return err
	}
	argv, err := shellCommand(s.Shell, script)
	if err != nil {
		return err
	}
	env, err := r.stepEnv(s, n)
	if err != nil {
		return err
	}
	env["PS1"] = `(failtrace) \w \$ `

	fmt.Fprintln(r.out)
	r.logf("Shell", "inside the container at step %d (%s); type exit to finish", n, s.Name)
	fmt.Fprintf(r.out, "%14sre-run the step with: %s\n", "", strings.Join(argv, " "))
	errOut := term.Err
	if errOut == nil {
		errOut = term.Out
	}
	_, err = r.deps.Engine.Exec(ctx, docker.ExecOptions{
		Container: r.container, Workdir: r.workdir(s), Env: env,
		Cmd:   []string{"sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash --norc; else exec sh; fi"},
		Stdin: term.In, TTY: term.TTY, Stdout: term.Out, Stderr: errOut,
	})
	return err
}
