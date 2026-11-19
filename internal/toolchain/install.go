package toolchain

import (
	"fmt"
	"strings"
)

var cacheNames = map[string]string{"go": "go", "node": "node", "python": "Python"}

// manifestInstall installs an actions/<tool>-versions archive at the same
// tool cache path setup-<tool> uses.
func manifestInstall(tool, version, arch, url string) *Install {
	dir := fmt.Sprintf("%s/%s/%s/%s", ToolCache, cacheNames[tool], version, arch)
	in := &Install{Tool: tool, Version: version, URL: url, Dir: dir, Source: "actions/" + tool + "-versions (the build setup-" + tool + " uses)"}
	if tool == "python" {
		// Python archives ship a setup.sh that installs into the tool cache.
		in.Script = strings.Join([]string{
			"tmp=$(mktemp -d)",
			"curl -fsSL " + shellQuote(url) + " | tar -xz -C \"$tmp\"",
			"cd \"$tmp\" && RUNNER_TOOL_CACHE=" + ToolCache + " bash ./setup.sh >/dev/null",
			"rm -rf \"$tmp\"",
		}, " && ")
		in.Env = map[string]string{
			"pythonLocation":   dir,
			"PKG_CONFIG_PATH":  dir + "/lib/pkgconfig",
			"Python_ROOT_DIR":  dir,
			"Python2_ROOT_DIR": dir,
			"Python3_ROOT_DIR": dir,
			"LD_LIBRARY_PATH":  dir + "/lib",
		}
		in.Path = []string{dir + "/bin", dir}
		return in
	}
	in.Script = fmt.Sprintf("mkdir -p %s && curl -fsSL %s | tar -xz -C %s && touch %s.complete", dir, shellQuote(url), dir, dir)
	in.Path = []string{dir + "/bin"}
	return in
}

// officialInstall falls back to the upstream release when a version is
// missing from the actions manifest.
func officialInstall(tool, version, arch string) *Install {
	dir := fmt.Sprintf("%s/%s/%s/%s", ToolCache, cacheNames[tool], version, arch)
	var url string
	switch tool {
	case "go":
		goArch := map[string]string{"x64": "amd64", "arm64": "arm64"}[arch]
		url = fmt.Sprintf("https://go.dev/dl/go%s.linux-%s.tar.gz", version, goArch)
	case "node":
		url = fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-linux-%s.tar.gz", version, version, arch)
	}
	return &Install{
		Tool: tool, Version: version, URL: url, Dir: dir, Source: "official " + tool + " release",
		Script: fmt.Sprintf("mkdir -p %s && curl -fsSL %s | tar -xz -C %s --strip-components=1", dir, shellQuote(url), dir),
		Path:   []string{dir + "/bin"},
	}
}

func resolveUV(version, arch string) (*Install, error) {
	if !isConcrete(version) {
		return nil, fmt.Errorf("uv %q: an exact version is needed; pass --toolchain uv=<version>", version)
	}
	triple := map[string]string{"x64": "x86_64", "arm64": "aarch64"}[arch]
	dir := fmt.Sprintf("%s/uv/%s/%s", ToolCache, version, triple)
	url := fmt.Sprintf("https://github.com/astral-sh/uv/releases/download/%s/uv-%s-unknown-linux-gnu.tar.gz", version, triple)
	return &Install{
		Tool: "uv", Version: version, URL: url, Dir: dir, Source: "astral-sh/uv release",
		Script: fmt.Sprintf("mkdir -p %s && curl -fsSL %s | tar -xz -C %s --strip-components=1", dir, shellQuote(url), dir),
		Path:   []string{dir},
	}, nil
}

// resolvePnpm installs pnpm with npm, so it must come after node.
func resolvePnpm(version string) (*Install, error) {
	if version == "" {
		return nil, fmt.Errorf("pnpm: version unknown; pass --toolchain pnpm=<version>")
	}
	return &Install{
		Tool: "pnpm", Version: version, Source: "npm",
		Script: "npm install --global --silent " + shellQuote("pnpm@"+version),
	}, nil
}

func resolveRust(version string) (*Install, error) {
	if version == "" {
		version = "stable"
	}
	return &Install{
		Tool: "rust", Version: version, Source: "rustup",
		Script: "curl --proto '=https' -fsSL https://sh.rustup.rs | sh -s -- -y -q --profile minimal --default-toolchain " + shellQuote(version),
		Env:    map[string]string{"RUSTUP_HOME": "/usr/local/rustup", "CARGO_HOME": "/usr/local/cargo"},
		Path:   []string{"/usr/local/cargo/bin"},
	}, nil
}

// installOrder makes sure runtimes come before tools installed with them.
var installOrder = map[string]int{"go": 0, "node": 1, "python": 2, "rust": 3, "uv": 4, "pnpm": 5}

// Less orders installs so dependencies (node before pnpm) come first.
func Less(a, b string) bool {
	ra, oka := installOrder[a]
	rb, okb := installOrder[b]
	if !oka {
		ra = 100
	}
	if !okb {
		rb = 100
	}
	return ra < rb
}

// shellQuote quotes s for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
