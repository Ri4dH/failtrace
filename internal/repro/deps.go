// Package repro executes a reproduction plan in local containers and compares
// the outcome with the failure CI reported.
package repro

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Ri4dH/failtrace/internal/docker"
	"github.com/Ri4dH/failtrace/internal/toolchain"
)

// Engine runs containers. docker.CLI implements it.
type Engine interface {
	Available(ctx context.Context) error
	ServerArch(ctx context.Context) string
	ImageExists(ctx context.Context, ref string) bool
	Build(ctx context.Context, dockerfile, tag, platform string, out io.Writer) error
	Run(ctx context.Context, o docker.RunOptions) (string, error)
	Exec(ctx context.Context, o docker.ExecOptions) (int, error)
	Health(ctx context.Context, id string) (string, error)
	Logs(ctx context.Context, id string, lines int) string
	Remove(ctx context.Context, ids ...string) error
}

// Git runs git commands on the host.
type Git interface {
	Run(ctx context.Context, dir string, env map[string]string, args ...string) (string, error)
}

// Resolver resolves toolchains. toolchain.Resolver implements it.
type Resolver interface {
	Resolve(ctx context.Context, tool, version, platform, ubuntu string) (*toolchain.Install, error)
}

// Deps are the side-effecting collaborators of Run.
type Deps struct {
	Engine   Engine
	Git      Git
	Resolver Resolver
}

// HostGit runs the git binary.
type HostGit struct{}

// Run executes git with args in dir. env is added to the inherited environment.
func (HostGit) Run(ctx context.Context, dir string, env map[string]string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if len(msg) > 600 {
			msg = "…" + msg[len(msg)-600:]
		}
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, msg)
	}
	return out.String(), nil
}
