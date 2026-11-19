package joblog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeGoTestFailure(t *testing.T) {
	sec := loadFixture(t, "go_test_failure.log").FailedSection()
	f := Analyze(sec, 25)

	if f.ExitCode == nil || *f.ExitCode != 1 {
		t.Errorf("exit code = %v, want 1", f.ExitCode)
	}
	if f.Category != CategoryTest {
		t.Errorf("category = %q, want %q", f.Category, CategoryTest)
	}
	wantTests := []string{"TestParseConfig", "TestParseConfig/empty_file"}
	if !reflect.DeepEqual(f.FailedTests, wantTests) {
		t.Errorf("failed tests = %q, want %q", f.FailedTests, wantTests)
	}
	if len(f.Errors) != 0 {
		t.Errorf("exit-code annotation should not be listed as an error: %q", f.Errors)
	}
	for _, l := range f.Excerpt {
		if strings.Contains(l, "Process completed") {
			t.Errorf("excerpt should not contain the exit-code line: %q", l)
		}
	}
}

func TestFailedTestPatterns(t *testing.T) {
	cases := []struct {
		line, want string
	}{
		{"--- FAIL: TestFoo (0.01s)", "TestFoo"},
		{"FAILED tests/test_api.py::test_login - AssertionError: 401", "tests/test_api.py::test_login"},
		{"FAILED tests/test_api.py::test_logout", "tests/test_api.py::test_logout"},
		{"test parser::tests::empty ... FAILED", "parser::tests::empty"},
		{"  ● Login › rejects bad password", "Login › rejects bad password"},
		{" FAIL  src/a.test.ts > math > adds 3ms", "src/a.test.ts > math > adds"},
		{" FAIL   serve-bundled  playground/hmr/hmr.spec.ts > accept > works", "playground/hmr/hmr.spec.ts > accept > works"},
		{"rspec ./spec/user_spec.rb:12 # User is valid", "./spec/user_spec.rb:12"},
	}
	for _, c := range cases {
		got := findFailedTests([]string{c.line})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("findFailedTests(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		title  string
		script string
		body   []string
		exit   int
		want   Category
	}{
		{"timeout", "Run make", "make", []string{"##[error]The operation was canceled."}, 1, CategoryTimeout},
		{"oom by exit code", "Run node build", "node build", nil, 137, CategoryOOM},
		{"oom by message", "Run npm run build", "npm run build", []string{"FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"}, 134, CategoryOOM},
		{"lint by command", "Run golangci-lint run", "golangci-lint run", []string{"main.go:3:1: exported func Foo should have comment"}, 1, CategoryLint},
		{"lint by action title", "Run golangci/golangci-lint-action@v6", "", []string{"main.go:3:1: undefined: x (typecheck)"}, 1, CategoryLint},
		{"generated diff", "Run make check", "make check", []string{"go mod tidy", "diff --git a/go.mod b/go.mod", "make: *** [check] Error 1"}, 2, CategoryDrift},
		{"dependency", "Run npm ci", "npm ci", []string{"npm ERR! code ERESOLVE"}, 1, CategoryDependency},
		{"build", "Run go build ./...", "go build ./...", []string{"./main.go:10:2: undefined: foo"}, 1, CategoryBuild},
		{"typescript", "Run tsc", "tsc", []string{"src/a.ts(3,1): error TS2304: Cannot find name 'x'."}, 2, CategoryBuild},
		{"unknown", "Run ./deploy.sh", "./deploy.sh", []string{"something went wrong"}, 1, CategoryUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := append(append([]string{}, c.body...), fmt.Sprintf("##[error]Process completed with exit code %d.", c.exit))
			sec := &Section{Title: c.title, Body: body}
			if c.script != "" {
				sec.Header.Script = []string{c.script}
			}
			for _, l := range body {
				if msg, ok := strings.CutPrefix(l, errorPrefix); ok {
					sec.Errors = append(sec.Errors, msg)
				}
			}
			if got := Analyze(sec, 10).Category; got != c.want {
				t.Errorf("category = %q, want %q", got, c.want)
			}
		})
	}
}

func TestExcerptTailWhenNoAnchor(t *testing.T) {
	var body []string
	for i := 0; i < 40; i++ {
		body = append(body, fmt.Sprintf("line %d", i))
	}
	got, anchored := excerpt(body, 5)
	if anchored || !reflect.DeepEqual(got, []string{"line 35", "line 36", "line 37", "line 38", "line 39"}) {
		t.Fatalf("got %q anchored=%v", got, anchored)
	}
}

func TestExcerptCentresOnFirstAnchor(t *testing.T) {
	var body []string
	for i := 0; i < 10; i++ {
		body = append(body, fmt.Sprintf("setup %d", i))
	}
	body = append(body, "make: *** [check] Error 1")
	for i := 0; i < 30; i++ {
		body = append(body, fmt.Sprintf("noise %d", i))
	}
	got, anchored := excerpt(body, 6)
	if !anchored {
		t.Fatal("expected anchored excerpt")
	}
	want := []string{"setup 6", "setup 7", "setup 8", "setup 9", "make: *** [check] Error 1", "noise 0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExcerptRewritesMarkers(t *testing.T) {
	body := []string{"##[group]Inner group", "x", "##[endgroup]", "##[warning]careful", "##[error]boom", "##[error]Process completed with exit code 1."}
	got, _ := excerpt(body, 10)
	want := []string{"▸ Inner group", "x", "Warning: careful", "Error: boom"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
