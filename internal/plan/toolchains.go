package plan

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/Ri4dH/failtrace/internal/joblog"
)

// setupAction describes a toolchain setup action and where to find the
// version it installed.
type setupAction struct {
	name          string   // owner/repo, lowercase
	tool          string   // toolchain name used in plans
	versionInputs []string // `with:` inputs holding a version spec
	fileInputs    []string // `with:` inputs pointing at a version file
	fallbackFile  string   // file consulted when no input is set
	cacheDirs     []string // /opt/hostedtoolcache/<dir>/<version>/ prefixes
	versionRe     *regexp.Regexp
	refIsVersion  bool // e.g. dtolnay/rust-toolchain@1.80
}

var setupActions = []setupAction{
	{name: "actions/setup-go", tool: "go", versionInputs: []string{"go-version"}, fileInputs: []string{"go-version-file"}, cacheDirs: []string{"go"}},
	{name: "actions/setup-node", tool: "node", versionInputs: []string{"node-version"}, fileInputs: []string{"node-version-file"}, cacheDirs: []string{"node"}},
	{name: "actions/setup-python", tool: "python", versionInputs: []string{"python-version"}, fileInputs: []string{"python-version-file"},
		fallbackFile: ".python-version", cacheDirs: []string{"Python", "PyPy"},
		versionRe: regexp.MustCompile(`Successfully set up (?:CPython|PyPy) \(([^)\s]+)\)`)},
	{name: "actions/setup-java", tool: "java", versionInputs: []string{"java-version"}, fileInputs: []string{"java-version-file"}, cacheDirs: []string{"Java_"}},
	{name: "ruby/setup-ruby", tool: "ruby", versionInputs: []string{"ruby-version"}, fallbackFile: ".ruby-version", cacheDirs: []string{"Ruby"}},
	{name: "dtolnay/rust-toolchain", tool: "rust", versionInputs: []string{"toolchain"}, refIsVersion: true},
	{name: "actions-rust-lang/setup-rust-toolchain", tool: "rust", versionInputs: []string{"toolchain"}},
	{name: "pnpm/action-setup", tool: "pnpm", versionInputs: []string{"version"}, fallbackFile: "package.json"},
	{name: "pnpm/setup", tool: "pnpm", versionInputs: []string{"version"}, fallbackFile: "package.json",
		versionRe: regexp.MustCompile(`pnpm-binary-[A-Za-z0-9]+-[A-Za-z0-9]+-(\d+\.\d+\.\d+)`)},
	{name: "oven-sh/setup-bun", tool: "bun", versionInputs: []string{"bun-version"}, fileInputs: []string{"bun-version-file"}},
	{name: "astral-sh/setup-uv", tool: "uv", versionInputs: []string{"version"}, fileInputs: []string{"version-file"}, cacheDirs: []string{"uv"},
		versionRe: regexp.MustCompile(`Successfully installed uv version (\S+)`)},
	{name: "denoland/setup-deno", tool: "deno", versionInputs: []string{"deno-version"}, fileInputs: []string{"deno-version-file"}},
	{name: "actions/setup-dotnet", tool: "dotnet", versionInputs: []string{"dotnet-version"}, fileInputs: []string{"global-json-file"}},
}

var (
	toolcacheRe = regexp.MustCompile(`/opt/hostedtoolcache/([^/\s'"]+)/([^/\s'"]+)/`)
	shaRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	goModRe     = regexp.MustCompile(`(?m)^go\s+(\S+)`)
	goTCRe      = regexp.MustCompile(`(?m)^toolchain\s+go(\S+)`)
)

// toolcacheTools maps tool cache directories to toolchain names.
var toolcacheTools = map[string]string{"go": "go", "node": "node", "Python": "python", "PyPy": "python", "Ruby": "ruby", "uv": "uv"}

func (b *builder) toolchain(sa *setupAction, uses string, sec *joblog.Section) Toolchain {
	tc := Toolchain{Tool: sa.tool, Source: uses}
	for _, in := range sa.versionInputs {
		if v := sec.Header.With[in]; v != "" {
			tc.Spec = v
			break
		}
	}
	if tc.Spec == "" && sa.refIsVersion {
		if _, ref, ok := strings.Cut(uses, "@"); ok && !shaRe.MatchString(ref) {
			tc.Spec = ref
		}
	}
	if v := resolvedVersion(sa, sec.Body); v != "" {
		tc.Version, tc.Exact = v, true
		return tc
	}
	if tc.Spec != "" {
		tc.Version = tc.Spec
		return tc
	}
	file := sa.fallbackFile
	for _, in := range sa.fileInputs {
		if v := sec.Header.With[in]; v != "" {
			file = v
			break
		}
	}
	if file != "" {
		tc.Spec = file
		tc.Version = b.versionFromFile(file, sa.tool)
	}
	return tc
}

var runtimeRe = regexp.MustCompile(`^([a-z]+)@(\S+)$`)

// runtimeToolchain handles setup actions that also install a runtime through
// a `runtime: node@26` input, such as pnpm/setup.
func runtimeToolchain(sec *joblog.Section, uses string) (Toolchain, bool) {
	m := runtimeRe.FindStringSubmatch(sec.Header.With["runtime"])
	if m == nil {
		return Toolchain{}, false
	}
	return Toolchain{Tool: m[1], Version: m[2], Spec: m[2], Source: uses + " (runtime input)"}, true
}

// resolvedVersion finds the exact version a setup action installed.
func resolvedVersion(sa *setupAction, body []string) string {
	for _, line := range body {
		for _, m := range toolcacheRe.FindAllStringSubmatch(line, -1) {
			for _, dir := range sa.cacheDirs {
				if strings.HasPrefix(m[1], dir) {
					return m[2]
				}
			}
		}
		if sa.versionRe != nil {
			if m := sa.versionRe.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// detectToolcache picks up toolchains installed by actions FailTrace does not
// know, such as composite setup actions, from tool cache paths in their output.
func (b *builder) detectToolcache(sec *joblog.Section, uses string) {
	for _, line := range sec.Body {
		for _, m := range toolcacheRe.FindAllStringSubmatch(line, -1) {
			tool, ok := toolcacheTools[m[1]]
			if !ok || b.hasToolchain(tool) {
				continue
			}
			b.addToolchain(Toolchain{Tool: tool, Version: m[2], Exact: true, Source: "detected in the output of " + uses})
		}
	}
}

func (b *builder) hasToolchain(tool string) bool {
	for _, t := range b.plan.Toolchains {
		if t.Tool == tool {
			return true
		}
	}
	return false
}

// addToolchain records tc; a later setup of the same tool replaces an earlier one.
func (b *builder) addToolchain(tc Toolchain) {
	for i, t := range b.plan.Toolchains {
		if t.Tool == tc.Tool {
			b.plan.Toolchains[i] = tc
			return
		}
	}
	b.plan.Toolchains = append(b.plan.Toolchains, tc)
}

func (b *builder) versionFromFile(file, tool string) string {
	if b.files == nil {
		return ""
	}
	file = path.Clean(strings.TrimPrefix(file, "./"))
	if file == "." || strings.HasPrefix(file, "..") || strings.HasPrefix(file, "/") {
		return ""
	}
	data, err := b.files.GetFile(b.ctx, b.in.Owner, b.in.Repo, file, b.in.Run.SHA)
	if err != nil {
		return ""
	}
	return parseVersionFile(file, data, tool)
}

// asdfNames maps toolchain names to their .tool-versions names.
var asdfNames = map[string]string{"go": "golang", "node": "nodejs", "python": "python", "ruby": "ruby", "java": "java", "rust": "rust", "pnpm": "pnpm", "bun": "bun", "uv": "uv", "deno": "deno", "dotnet": "dotnet"}

func parseVersionFile(file string, data []byte, tool string) string {
	text := string(data)
	switch path.Base(file) {
	case "go.mod":
		if m := goTCRe.FindStringSubmatch(text); m != nil {
			return m[1]
		}
		if m := goModRe.FindStringSubmatch(text); m != nil {
			return m[1]
		}
		return ""
	case "package.json":
		var pkg struct {
			PackageManager string `json:"packageManager"`
		}
		if json.Unmarshal(data, &pkg) != nil {
			return ""
		}
		if v, ok := strings.CutPrefix(pkg.PackageManager, tool+"@"); ok {
			v, _, _ = strings.Cut(v, "+")
			return v
		}
		return ""
	case ".tool-versions":
		for _, line := range strings.Split(text, "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == asdfNames[tool] {
				return f[1]
			}
		}
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return strings.TrimPrefix(line, "v")
		}
	}
	return ""
}

// toolCommands maps commands to the toolchain they need.
var toolCommands = map[string]string{
	"go": "go", "gofmt": "go",
	"node": "node", "npm": "node", "npx": "node", "yarn": "node", "pnpm": "node",
	"python": "python", "python3": "python", "pip": "python", "pip3": "python", "pytest": "python", "tox": "python", "nox": "python",
	"cargo": "rust", "rustc": "rust",
	"java": "java", "mvn": "java", "./mvnw": "java", "gradle": "java", "./gradlew": "java",
	"ruby": "ruby", "bundle": "ruby", "rake": "ruby",
	"dotnet": "dotnet", "deno": "deno", "bun": "bun", "uv": "uv",
}

// reviewToolUsage flags toolchains that run steps use without a setup step.
// On GitHub-hosted runners those come preinstalled; a plain image lacks them.
func (b *builder) reviewToolUsage() {
	if b.plan.Image.Exact {
		return // the job container provides its own tools
	}
	missing := map[string]bool{}
	for _, s := range b.plan.Steps {
		if s.Kind != KindRun {
			continue
		}
		for _, line := range strings.Split(s.Script, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if tool, ok := toolCommands[f[0]]; ok && !b.hasToolchain(tool) {
				missing[tool] = true
			}
		}
	}
	for _, tool := range sortedKeys(missing) {
		b.issue(SeverityInfo, 0, "steps use %s without a setup step, so CI used the version preinstalled on %s; the plan needs one installed", tool, b.runnerName())
	}
}

func (b *builder) runnerName() string {
	if r := b.ev.Log.Runner; r.Image != "" {
		return fmt.Sprintf("the %s runner image", r.Image)
	}
	return "the runner image"
}
