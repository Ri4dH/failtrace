package repro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Ri4dH/failtrace/internal/plan"
	"github.com/Ri4dH/failtrace/internal/toolchain"
)

// runnerPackages approximate the tools every GitHub-hosted Ubuntu runner
// ships with, so ordinary scripts (git, curl, make, python3, jq, sudo) work.
// git-lfs is left out: checkouts (and LFS pulls) happen on the host, and its
// post-install script fails under amd64 emulation.
var runnerPackages = []string{
	"build-essential", "ca-certificates", "curl", "file", "git", "gnupg", "jq", "lsb-release",
	"python3", "python3-pip", "python3-venv", "sudo", "unzip", "wget", "xz-utils", "zip", "zstd",
}

const aptProvision = "export DEBIAN_FRONTEND=noninteractive && apt-get update -qq && " +
	"apt-get install -y -qq --no-install-recommends %s && rm -rf /var/lib/apt/lists/*"

// ensureDownloaders installs curl and tar in arbitrary job container images,
// which toolchain installs need.
const ensureDownloaders = `if ! command -v curl >/dev/null 2>&1 || ! command -v tar >/dev/null 2>&1; then ` +
	`if command -v apt-get >/dev/null 2>&1; then export DEBIAN_FRONTEND=noninteractive && apt-get update -qq && apt-get install -y -qq --no-install-recommends ca-certificates curl tar xz-utils >/dev/null; ` +
	`elif command -v apk >/dev/null 2>&1; then apk add --no-cache ca-certificates curl tar xz bash >/dev/null; ` +
	`elif command -v dnf >/dev/null 2>&1; then dnf install -y -q ca-certificates curl tar xz; ` +
	`else echo "failtrace: cannot install curl in this image" >&2; exit 1; fi; fi`

var ubuntuRefRe = regexp.MustCompile(`^(?:docker\.io/)?(?:library/)?ubuntu:(\d{2}\.\d{2})$`)

// envImage is the image steps run in: the plan's base image plus toolchains.
type envImage struct {
	Tag        string
	Dockerfile string // empty when the base image is used as is
	Installs   []*toolchain.Install
	Missing    []string // toolchains that could not be installed, with reasons
}

// planImage resolves toolchains and renders the environment Dockerfile.
func planImage(ctx context.Context, res Resolver, p *plan.Plan, platform string, overrides map[string]string) (*envImage, error) {
	ubuntu := ""
	if m := ubuntuRefRe.FindStringSubmatch(p.Image.Ref); m != nil {
		ubuntu = m[1]
	}
	wanted := map[string]string{}
	for _, tc := range p.Toolchains {
		wanted[tc.Tool] = tc.Version
	}
	for tool, v := range overrides {
		wanted[tool] = v
	}
	tools := make([]string, 0, len(wanted))
	for tool := range wanted {
		tools = append(tools, tool)
	}
	sort.SliceStable(tools, func(i, j int) bool {
		if toolchain.Less(tools[i], tools[j]) != toolchain.Less(tools[j], tools[i]) {
			return toolchain.Less(tools[i], tools[j])
		}
		return tools[i] < tools[j]
	})

	img := &envImage{}
	for _, tool := range tools {
		version := wanted[tool]
		if version == "" {
			img.Missing = append(img.Missing, fmt.Sprintf("%s: the version CI used is unknown; pass --toolchain %s=<version>", tool, tool))
			continue
		}
		in, err := res.Resolve(ctx, tool, version, platform, ubuntu)
		if err != nil {
			if errors.Is(err, toolchain.ErrUnsupported) {
				err = fmt.Errorf("FailTrace cannot install %s yet", tool)
			}
			img.Missing = append(img.Missing, fmt.Sprintf("%s %s: %v", tool, version, err))
			continue
		}
		img.Installs = append(img.Installs, in)
	}

	if p.Image.Exact && len(img.Installs) == 0 {
		img.Tag = p.Image.Ref
		return img, nil
	}
	img.Dockerfile = renderDockerfile(p, img.Installs)
	sum := sha256.Sum256([]byte(platform + "\n" + img.Dockerfile))
	img.Tag = "failtrace-env:" + hex.EncodeToString(sum[:])[:16]
	return img, nil
}

func renderDockerfile(p *plan.Plan, installs []*toolchain.Install) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# FailTrace environment for %s (%s)\n", p.Repository, p.Job.Name)
	fmt.Fprintf(&b, "FROM %s\n", p.Image.Ref)
	if p.Image.Exact {
		fmt.Fprintf(&b, "RUN %s\n", ensureDownloaders)
	} else {
		fmt.Fprintf(&b, "RUN "+aptProvision+"\n", strings.Join(runnerPackages, " "))
	}
	var paths []string
	env := map[string]string{}
	for _, in := range installs {
		fmt.Fprintf(&b, "# %s %s from %s\n", in.Tool, in.Version, in.Source)
		fmt.Fprintf(&b, "RUN %s\n", in.Script)
		for k, v := range in.Env {
			env[k] = v
		}
		paths = append(append([]string{}, in.Path...), paths...) // later installs take precedence, like setup actions
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "ENV %s=%q\n", k, env[k])
	}
	if len(paths) > 0 {
		fmt.Fprintf(&b, "ENV PATH=%q\n", strings.Join(paths, ":")+":$PATH")
	}
	return b.String()
}
