package toolchain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const goManifest = `[
 {"version":"1.23.1","stable":true,"files":[{"arch":"x64","platform":"linux","download_url":"https://dl/go-1.23.1-linux-x64.tar.gz"}]},
 {"version":"1.22.10","stable":true,"files":[{"arch":"x64","platform":"linux","download_url":"https://dl/go-1.22.10-linux-x64.tar.gz"},{"arch":"arm64","platform":"linux","download_url":"https://dl/go-1.22.10-linux-arm64.tar.gz"}]},
 {"version":"1.22.9","stable":true,"files":[{"arch":"x64","platform":"linux","download_url":"https://dl/go-1.22.9-linux-x64.tar.gz"}]},
 {"version":"1.24.0-rc.1","stable":false,"files":[{"arch":"x64","platform":"linux","download_url":"https://dl/go-1.24.0-rc.1.tar.gz"}]}
]`

const pythonManifest = `[
 {"version":"3.11.18","stable":true,"files":[
   {"arch":"x64","platform":"linux","platform_version":"22.04","download_url":"https://dl/py-22.04.tar.gz"},
   {"arch":"x64","platform":"linux","platform_version":"24.04","download_url":"https://dl/py-24.04.tar.gz"},
   {"arch":"x64-freethreaded","platform":"linux","platform_version":"24.04","download_url":"https://dl/py-ft.tar.gz"},
   {"arch":"x64","platform":"darwin","download_url":"https://dl/py-mac.tar.gz"}]}
]`

func newTestResolver(t *testing.T) (*Resolver, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/go-versions/main/versions-manifest.json":
			_, _ = w.Write([]byte(goManifest))
		case "/python-versions/main/versions-manifest.json":
			_, _ = w.Write([]byte(pythonManifest))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Resolver{HTTP: srv.Client(), ManifestBase: srv.URL}, &hits
}

func TestResolveGoFromManifest(t *testing.T) {
	r, hits := newTestResolver(t)
	cases := []struct{ spec, want, url string }{
		{"1.22.9", "1.22.9", "https://dl/go-1.22.9-linux-x64.tar.gz"},
		{"1.22", "1.22.10", "https://dl/go-1.22.10-linux-x64.tar.gz"},
		{"1.22.x", "1.22.10", ""},
		{"^1", "1.23.1", ""},
		{"stable", "1.23.1", ""},
		{"1.24.0-rc.1", "1.24.0-rc.1", ""},
	}
	for _, c := range cases {
		in, err := r.Resolve(context.Background(), "go", c.spec, "linux/amd64", "24.04")
		if err != nil {
			t.Errorf("Resolve(go %s): %v", c.spec, err)
			continue
		}
		if in.Version != c.want || (c.url != "" && in.URL != c.url) {
			t.Errorf("Resolve(go %s) = %s %s, want %s %s", c.spec, in.Version, in.URL, c.want, c.url)
		}
	}
	if *hits != 1 {
		t.Errorf("manifest fetched %d times, want 1 (cached)", *hits)
	}

	in, _ := r.Resolve(context.Background(), "go", "1.22.10", "linux/arm64", "")
	if in.URL != "https://dl/go-1.22.10-linux-arm64.tar.gz" || in.Dir != "/opt/hostedtoolcache/go/1.22.10/arm64" {
		t.Errorf("arm64 install = %+v", in)
	}
	if !strings.Contains(in.Script, "tar -xz -C /opt/hostedtoolcache/go/1.22.10/arm64") || in.Path[0] != in.Dir+"/bin" {
		t.Errorf("install script = %q path = %q", in.Script, in.Path)
	}
}

func TestResolveFallsBackToOfficialRelease(t *testing.T) {
	r, _ := newTestResolver(t)
	in, err := r.Resolve(context.Background(), "go", "1.21.13", "linux/amd64", "")
	if err != nil {
		t.Fatal(err)
	}
	if in.URL != "https://go.dev/dl/go1.21.13.linux-amd64.tar.gz" || !strings.Contains(in.Script, "--strip-components=1") {
		t.Errorf("fallback = %+v", in)
	}
	in, err = r.Resolve(context.Background(), "node", "20.11.1", "linux/arm64", "") // node manifest 404s
	if err != nil || in.URL != "https://nodejs.org/dist/v20.11.1/node-v20.11.1-linux-arm64.tar.gz" {
		t.Errorf("node fallback = %+v, %v", in, err)
	}
	if _, err := r.Resolve(context.Background(), "go", "1.19", "linux/amd64", ""); err == nil {
		t.Error("a non-exact spec with no manifest match should fail")
	}
}

func TestResolvePython(t *testing.T) {
	r, _ := newTestResolver(t)
	in, err := r.Resolve(context.Background(), "python", "3.11", "linux/amd64", "24.04")
	if err != nil {
		t.Fatal(err)
	}
	if in.Version != "3.11.18" || in.URL != "https://dl/py-24.04.tar.gz" {
		t.Errorf("python = %+v", in)
	}
	if in.Env["LD_LIBRARY_PATH"] != "/opt/hostedtoolcache/Python/3.11.18/x64/lib" || !strings.Contains(in.Script, "setup.sh") {
		t.Errorf("python install = %+v", in)
	}
	// No 26.04 build: use the newest older one.
	in, _ = r.Resolve(context.Background(), "python", "3.11.18", "linux/amd64", "26.04")
	if in.URL != "https://dl/py-24.04.tar.gz" {
		t.Errorf("python on 26.04 = %s", in.URL)
	}
	// Older Ubuntu than any build: take what exists.
	in, _ = r.Resolve(context.Background(), "python", "3.11.18", "linux/amd64", "20.04")
	if in.URL != "https://dl/py-24.04.tar.gz" {
		t.Errorf("python on 20.04 = %s", in.URL)
	}
	if _, err := r.Resolve(context.Background(), "python", "3.11.18", "linux/arm64", "24.04"); err == nil {
		t.Error("expected an error when no arm64 build exists")
	}
}

func TestResolveOtherTools(t *testing.T) {
	r, _ := newTestResolver(t)
	uv, err := r.Resolve(context.Background(), "uv", "0.13.8", "linux/amd64", "")
	if err != nil || uv.Dir != "/opt/hostedtoolcache/uv/0.13.8/x86_64" || !strings.Contains(uv.URL, "uv-x86_64-unknown-linux-gnu") {
		t.Errorf("uv = %+v, %v", uv, err)
	}
	if _, err := r.Resolve(context.Background(), "uv", "latest", "linux/amd64", ""); err == nil {
		t.Error("uv needs an exact version")
	}
	pnpm, err := r.Resolve(context.Background(), "pnpm", "9.12.0", "linux/amd64", "")
	if err != nil || pnpm.Script != "npm install --global --silent 'pnpm@9.12.0'" {
		t.Errorf("pnpm = %+v, %v", pnpm, err)
	}
	rust, err := r.Resolve(context.Background(), "rust", "", "linux/amd64", "")
	if err != nil || rust.Version != "stable" {
		t.Errorf("rust = %+v, %v", rust, err)
	}
	if _, err := r.Resolve(context.Background(), "java", "21", "linux/amd64", ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("java err = %v, want ErrUnsupported", err)
	}
	if _, err := r.Resolve(context.Background(), "go", "lts/*", "linux/amd64", ""); err == nil || !strings.Contains(err.Error(), "--toolchain") {
		t.Errorf("lts alias err = %v", err)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.22.10", "1.22.9", 1},
		{"1.22", "1.22.0", 0},
		{"1.24.0-rc.1", "1.24.0", -1},
		{"3.9", "3.11", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}
