// Package toolchain resolves plan toolchains to concrete downloads and the
// shell commands that install them inside a Linux container.
//
// Go, Node and Python come from the same manifests actions/setup-go,
// setup-node and setup-python use, and are installed under
// /opt/hostedtoolcache exactly where GitHub's runners keep them.
package toolchain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ToolCache is where GitHub-hosted runners keep toolchains.
const ToolCache = "/opt/hostedtoolcache"

// DefaultManifestBase hosts the actions/<tool>-versions manifests.
const DefaultManifestBase = "https://raw.githubusercontent.com/actions"

// ErrUnsupported is returned for toolchains FailTrace cannot install yet.
var ErrUnsupported = errors.New("installing this toolchain is not supported yet")

// Install is a resolved toolchain installation.
type Install struct {
	Tool    string            `json:"tool"`
	Version string            `json:"version"` // concrete version that will be installed
	URL     string            `json:"url,omitempty"`
	Dir     string            `json:"dir,omitempty"`
	Script  string            `json:"script"`         // POSIX shell, run as root during the image build
	Env     map[string]string `json:"env,omitempty"`  // set after installation
	Path    []string          `json:"path,omitempty"` // prepended to PATH, first entry first
	Source  string            `json:"source"`
}

// Resolver turns toolchain requests into Installs.
type Resolver struct {
	HTTP         *http.Client
	ManifestBase string

	mu        sync.Mutex
	manifests map[string][]manifestEntry
}

// NewResolver returns a resolver using the public manifests.
func NewResolver() *Resolver {
	return &Resolver{HTTP: &http.Client{Timeout: time.Minute}, ManifestBase: DefaultManifestBase}
}

type manifestEntry struct {
	Version string         `json:"version"`
	Stable  bool           `json:"stable"`
	Files   []manifestFile `json:"files"`
}

type manifestFile struct {
	Arch            string `json:"arch"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platform_version"`
	DownloadURL     string `json:"download_url"`
}

// Resolve resolves tool at version (an exact version or a spec such as
// "1.22", "20.x" or "stable") for a linux/<platform> container whose base is
// Ubuntu ubuntu (e.g. "24.04"; may be empty for non-Ubuntu images).
func (r *Resolver) Resolve(ctx context.Context, tool, version, platform, ubuntu string) (*Install, error) {
	arch := "x64"
	if platform == "linux/arm64" {
		arch = "arm64"
	}
	switch tool {
	case "go", "node":
		return r.resolveManifest(ctx, tool, version, arch, "")
	case "python":
		return r.resolveManifest(ctx, tool, version, arch, ubuntu)
	case "uv":
		return resolveUV(version, arch)
	case "pnpm":
		return resolvePnpm(version)
	case "rust":
		return resolveRust(version)
	}
	return nil, fmt.Errorf("%s: %w", tool, ErrUnsupported)
}

func (r *Resolver) resolveManifest(ctx context.Context, tool, spec, arch, ubuntu string) (*Install, error) {
	entries, err := r.manifest(ctx, tool)
	if err != nil {
		if exact := isConcrete(spec); exact && tool != "python" {
			return officialInstall(tool, spec, arch), nil
		}
		return nil, err
	}
	entry, err := pickVersion(entries, spec)
	if err != nil {
		if isConcrete(spec) && tool != "python" {
			return officialInstall(tool, spec, arch), nil
		}
		return nil, fmt.Errorf("%s %s: %w", tool, spec, err)
	}
	file := pickFile(entry.Files, arch, ubuntu)
	if file == nil {
		if tool != "python" {
			return officialInstall(tool, entry.Version, arch), nil
		}
		return nil, fmt.Errorf("python %s has no build for linux %s", entry.Version, arch)
	}
	return manifestInstall(tool, entry.Version, arch, file.DownloadURL), nil
}

func (r *Resolver) manifest(ctx context.Context, tool string) ([]manifestEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.manifests[tool]; ok {
		return m, nil
	}
	url := fmt.Sprintf("%s/%s-versions/main/versions-manifest.json", strings.TrimRight(r.ManifestBase, "/"), tool)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "failtrace")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s versions: %w", tool, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s versions: %s", tool, resp.Status)
	}
	var entries []manifestEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode %s versions: %w", tool, err)
	}
	if r.manifests == nil {
		r.manifests = map[string][]manifestEntry{}
	}
	r.manifests[tool] = entries
	return entries, nil
}

// pickVersion returns the highest version matching spec. Pre-releases are
// only considered when the spec names one.
func pickVersion(entries []manifestEntry, spec string) (*manifestEntry, error) {
	spec = normalizeSpec(spec)
	if spec == "unsupported" {
		return nil, errors.New("version ranges and aliases like lts/* are not supported; pass --toolchain <tool>=<version>")
	}
	var best *manifestEntry
	for i := range entries {
		e := &entries[i]
		if !matches(spec, e.Version) {
			continue
		}
		if !e.Stable && !strings.Contains(spec, "-") && e.Version != spec {
			continue
		}
		if best == nil || compareVersions(e.Version, best.Version) > 0 {
			best = e
		}
	}
	if best == nil {
		return nil, errors.New("no matching version in the actions manifest")
	}
	return best, nil
}

// normalizeSpec maps common setup-action specs to a prefix match:
// "v1.22" → "1.22", "1.22.x" → "1.22", "^18" → "18", "~1.2" → "1.2",
// "stable"/"latest"/"*" → "".
func normalizeSpec(spec string) string {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(spec), "v"))
	switch strings.ToLower(s) {
	case "", "stable", "latest", "*", "x", "current", "node":
		return ""
	}
	if strings.HasPrefix(s, "^") {
		major, _, _ := strings.Cut(strings.TrimPrefix(s, "^"), ".")
		return major
	}
	if strings.HasPrefix(s, "~") {
		parts := strings.SplitN(strings.TrimPrefix(s, "~"), ".", 3)
		return strings.Join(parts[:min(2, len(parts))], ".")
	}
	if strings.ContainsAny(s, "<>=| /") {
		return "unsupported"
	}
	for strings.HasSuffix(s, ".x") || strings.HasSuffix(s, ".*") {
		s = s[:len(s)-2]
	}
	return s
}

func matches(spec, version string) bool {
	if spec == "" || spec == version {
		return true
	}
	return strings.HasPrefix(version, spec+".") || strings.HasPrefix(version, spec+"-")
}

// isConcrete reports whether spec names a full version like 1.22.7.
func isConcrete(spec string) bool {
	parts := strings.Split(strings.TrimPrefix(spec, "v"), ".")
	if len(parts) < 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// compareVersions compares dotted versions numerically; a pre-release
// sorts before its release.
func compareVersions(a, b string) int {
	ar, apre, _ := strings.Cut(a, "-")
	br, bpre, _ := strings.Cut(b, "-")
	as, bs := strings.Split(ar, "."), strings.Split(br, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return strings.Compare(apre, bpre)
}

// pickFile chooses the build for arch, preferring the exact Ubuntu release,
// then the newest older one.
func pickFile(files []manifestFile, arch, ubuntu string) *manifestFile {
	var cands []manifestFile
	for _, f := range files {
		if f.Platform == "linux" && f.Arch == arch {
			cands = append(cands, f)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool { return compareVersions(cands[i].PlatformVersion, cands[j].PlatformVersion) > 0 })
	if ubuntu != "" {
		for i := range cands {
			if cands[i].PlatformVersion == "" || compareVersions(cands[i].PlatformVersion, ubuntu) <= 0 {
				return &cands[i]
			}
		}
	}
	return &cands[0]
}
