// Package docker drives containers through the docker CLI. Using the CLI
// instead of the Engine API keeps FailTrace dependency-free and works with
// any docker-compatible setup (Docker Desktop, colima, OrbStack, ...).
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// CLI runs docker commands.
type CLI struct {
	Bin string // defaults to "docker"
}

// Mount is a bind mount.
type Mount struct {
	Host, Container string
}

// RunOptions configures a detached container.
type RunOptions struct {
	Name       string
	Image      string
	Platform   string
	Mounts     []Mount
	Env        map[string]string // passed via the process environment, not argv
	Network    string            // e.g. "container:<id>"
	Workdir    string
	Entrypoint string
	Cmd        []string
	AddHosts   []string // "name:ip"
	Extra      []string // extra flags, e.g. service health checks
	Init       bool
}

// ExecOptions configures a command run inside a container.
type ExecOptions struct {
	Container string
	Workdir   string
	Env       map[string]string // passed via the process environment, not argv
	Cmd       []string
	Stdout    io.Writer
	Stderr    io.Writer
}

func (c *CLI) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "docker"
}

// Available checks that the docker CLI exists and the daemon responds.
func (c *CLI) Available(ctx context.Context) error {
	if _, err := exec.LookPath(c.bin()); err != nil {
		return errors.New("docker not found: install Docker Desktop, colima or another docker-compatible runtime")
	}
	out, err := c.output(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil || strings.TrimSpace(out) == "" {
		return errors.New("the docker daemon is not running (start Docker Desktop or your docker runtime and retry)")
	}
	return nil
}

// ServerArch returns the daemon's architecture, e.g. "aarch64" or "x86_64".
func (c *CLI) ServerArch(ctx context.Context) string {
	out, _ := c.output(ctx, "info", "--format", "{{.Architecture}}")
	return strings.TrimSpace(out)
}

// ImageExists reports whether ref is present locally.
func (c *CLI) ImageExists(ctx context.Context, ref string) bool {
	_, err := c.output(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil
}

// Build builds an image from a Dockerfile with an empty build context.
func (c *CLI) Build(ctx context.Context, dockerfile, tag, platform string, out io.Writer) error {
	args := []string{"build", "--progress=plain", "-t", tag}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Stdin = strings.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	return nil
}

// Run starts a detached container and returns its ID.
func (c *CLI) Run(ctx context.Context, o RunOptions) (string, error) {
	args := []string{"run", "--detach"}
	if o.Name != "" {
		args = append(args, "--name", o.Name)
	}
	if o.Platform != "" {
		args = append(args, "--platform", o.Platform)
	}
	if o.Init {
		args = append(args, "--init")
	}
	for _, m := range o.Mounts {
		args = append(args, "--volume", m.Host+":"+m.Container)
	}
	if o.Network != "" {
		args = append(args, "--network", o.Network)
	}
	if o.Workdir != "" {
		args = append(args, "--workdir", o.Workdir)
	}
	if o.Entrypoint != "" {
		args = append(args, "--entrypoint", o.Entrypoint)
	}
	for _, h := range o.AddHosts {
		args = append(args, "--add-host", h)
	}
	args = append(args, envFlags(o.Env)...)
	args = append(args, o.Extra...)
	args = append(args, o.Image)
	args = append(args, o.Cmd...)

	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = withEnv(o.Env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker run %s: %v: %s", o.Image, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Exec runs a command in a container and returns its exit code. A non-nil
// error means the command could not be run at all.
func (c *CLI) Exec(ctx context.Context, o ExecOptions) (int, error) {
	args := []string{"exec"}
	if o.Workdir != "" {
		args = append(args, "--workdir", o.Workdir)
	}
	args = append(args, envFlags(o.Env)...)
	args = append(args, o.Container)
	args = append(args, o.Cmd...)
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = withEnv(o.Env)
	cmd.Stdout, cmd.Stderr = o.Stdout, o.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr) && ctx.Err() == nil:
		return exitErr.ExitCode(), nil
	case ctx.Err() != nil:
		return -1, ctx.Err()
	}
	return -1, fmt.Errorf("docker exec: %w", err)
}

// Health returns a container's health status ("healthy", "starting",
// "unhealthy"), "none" when it has no health check, or "exited".
func (c *CLI) Health(ctx context.Context, id string) (string, error) {
	out, err := c.output(ctx, "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", id)
	if err != nil {
		return "", err
	}
	state, health, _ := strings.Cut(strings.TrimSpace(out), " ")
	if state != "running" {
		return "exited", nil
	}
	return health, nil
}

// Logs returns the tail of a container's output.
func (c *CLI) Logs(ctx context.Context, id string, lines int) string {
	out, _ := c.combined(ctx, "logs", "--tail", fmt.Sprint(lines), id)
	return out
}

// Remove force-removes containers, ignoring ones that are already gone.
func (c *CLI) Remove(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := c.combined(ctx, append([]string{"rm", "--force", "--volumes"}, ids...)...)
	return err
}

func (c *CLI) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (c *CLI) combined(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, c.bin(), args...).CombinedOutput()
	return string(out), err
}

// envFlags names variables without values ("-e KEY"); docker reads the values
// from its own environment, which keeps secrets out of the process list.
func envFlags(env map[string]string) []string {
	flags := make([]string, 0, 2*len(env))
	for k := range env {
		flags = append(flags, "--env", k)
	}
	return flags
}

func withEnv(env map[string]string) []string {
	out := os.Environ()
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// SplitArgs splits a command line like `--health-cmd "pg_isready -U x"` into
// arguments, honouring single and double quotes and backslash escapes.
func SplitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inArg = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in %q", s)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
