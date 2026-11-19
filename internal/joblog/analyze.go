package joblog

import (
	"regexp"
	"strconv"
	"strings"
)

// Category is a coarse classification of why a step failed.
type Category string

const (
	CategoryTest       Category = "test"
	CategoryBuild      Category = "build"
	CategoryDependency Category = "dependency"
	CategoryLint       Category = "lint"
	CategoryDrift      Category = "generated-diff"
	CategoryTimeout    Category = "timeout"
	CategoryOOM        Category = "out-of-memory"
	CategoryUnknown    Category = "unknown"
)

const maxFailedTests = 25

// Failure summarises a failed step's log section.
type Failure struct {
	Section         string            `json:"section"`
	Command         []string          `json:"command,omitempty"`
	Shell           string            `json:"shell,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	With            map[string]string `json:"with,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	Errors          []string          `json:"errors,omitempty"`
	FailedTests     []string          `json:"failed_tests,omitempty"`
	Excerpt         []string          `json:"excerpt"`
	ExcerptAnchored bool              `json:"excerpt_anchored,omitempty"` // centred on the first error line rather than the tail
	Signature       string            `json:"signature,omitempty"`        // first error line, normalised for comparison
	Category        Category          `json:"category"`
}

var failedTestPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*--- FAIL: (\S+)`),                      // go test
	regexp.MustCompile(`^FAILED (\S+?)(?: - .*)?$`),                // pytest
	regexp.MustCompile(`^test (\S+) \.\.\. FAILED$`),               // cargo test
	regexp.MustCompile(`^\s*● (.+ › .+)$`),                         // jest
	regexp.MustCompile(`^\s*(?:FAIL|×)\s+(\S+ > .+?)(?: \d+ms)?$`), // vitest
	regexp.MustCompile(`^rspec (\./\S+:\d+)`),                      // rspec
}

// anchorRe matches lines that usually mark where a failure starts. The
// excerpt is centred on the first one so that trailing noise (diff hunks,
// cleanup output) does not push the cause out of view.
var anchorRe = regexp.MustCompile(strings.Join([]string{
	`^\s*--- FAIL: `,
	`^FAIL\s`,
	`^panic: `,
	`^make(?:\[\d+\])?: \*\*\* `,
	`^npm ERR! `,
	`^Error: `,
	`^error(?:\[E\d+\])?: `,
	`^fatal: `,
	`^Traceback \(most recent call last\)`,
	`^FAILED `,
	`^diff --git `,
	`error TS\d+:`,
	`^\S+\.\w+:\d+:\d+: `, // file:line:col: diagnostics (go vet, gcc, rustc, ...)
}, "|"))

var (
	timeoutRe = regexp.MustCompile(`exceeded the maximum execution time|The operation was canceled`)
	oomRe     = regexp.MustCompile(`(?i)heap out of memory|out of memory|OOMKilled|^Killed$|signal: killed`)
	lintCmdRe = regexp.MustCompile(`(?i)\b(lint|golangci-lint|eslint|ruff|flake8|pylint|prettier|gofmt|black|rubocop|clippy|fmt|go vet|govet|staticcheck|mypy)\b`)
	driftRe   = regexp.MustCompile(`^diff --git `)
	depRe     = regexp.MustCompile(`npm ERR! code E|ERESOLVE|ERR_PNPM_|No matching distribution found|Could not find a version that satisfies|ResolutionImpossible|missing go\.sum entry|Could not resolve dependencies|failed to select a version|unable to resolve dependency`)
	buildRe   = regexp.MustCompile(`error TS\d+:|error\[E\d+\]|\.go:\d+:\d+: |\[build failed\]|SyntaxError:|cannot find symbol|COMPILATION ERROR|error: could not compile|Build FAILED`)
	testRe    = regexp.MustCompile(`^FAIL\b|Tests?:\s+\d+ failed|\b\d+ failed\b|npm ERR! Test failed|AssertionError`)
)

// Analyze extracts the failure signature from a failed section.
func Analyze(s *Section, excerptLines int) *Failure {
	f := &Failure{
		Section: s.Title,
		Command: s.Header.Script,
		Shell:   s.Header.Shell,
		Env:     s.Header.Env,
		With:    s.Header.With,
	}
	for _, e := range s.Errors {
		if m := exitCodeRe.FindStringSubmatch(e); m != nil {
			if code, err := strconv.Atoi(m[1]); err == nil && f.ExitCode == nil {
				f.ExitCode = &code
			}
			continue
		}
		f.Errors = append(f.Errors, e)
	}
	f.FailedTests = findFailedTests(s.Body)
	f.Excerpt, f.ExcerptAnchored = excerpt(s.Body, excerptLines)
	f.Signature = signature(s.Body, f.Errors)
	f.Category = classify(f, s)
	return f
}

func findFailedTests(body []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range body {
		for _, re := range failedTestPatterns {
			m := re.FindStringSubmatch(line)
			if m == nil || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			out = append(out, m[1])
			if len(out) == maxFailedTests {
				return out
			}
			break
		}
	}
	return out
}

// excerpt returns up to n readable lines. When an anchor line exists it keeps
// roughly two thirds of the window before it and one third after.
func excerpt(body []string, n int) ([]string, bool) {
	lines := make([]string, 0, len(body))
	for _, l := range body {
		switch {
		case l == endGroup:
			continue
		case strings.HasPrefix(l, errorPrefix):
			msg := strings.TrimPrefix(l, errorPrefix)
			if exitCodeRe.MatchString(msg) {
				continue
			}
			l = "Error: " + msg
		case strings.HasPrefix(l, "##[warning]"):
			l = "Warning: " + strings.TrimPrefix(l, "##[warning]")
		case strings.HasPrefix(l, groupPrefix):
			l = "▸ " + strings.TrimPrefix(l, groupPrefix)
		}
		lines = append(lines, l)
	}
	lines = trimTrailingBlank(lines)
	if n <= 0 || len(lines) <= n {
		return lines, false
	}
	tailStart := len(lines) - n
	anchor := -1
	for i, l := range lines {
		if anchorRe.MatchString(l) {
			anchor = i
			break
		}
	}
	if anchor < 0 || anchor >= tailStart {
		return lines[tailStart:], false
	}
	start := anchor - (2*n)/3
	if start < 0 {
		start = 0
	}
	return lines[start : start+n], true
}

var (
	sigHexRe   = regexp.MustCompile(`\b[0-9a-f]{7,64}\b`)
	sigNumRe   = regexp.MustCompile(`\d+(\.\d+)?`)
	sigSpaceRe = regexp.MustCompile(`\s+`)
)

// signature is the first error line with volatile parts (durations, line
// numbers, hashes, timings) replaced, so CI and local runs can be compared.
func signature(body, errors []string) string {
	line := ""
	for _, l := range body {
		if anchorRe.MatchString(l) {
			line = l
			break
		}
	}
	if line == "" && len(errors) > 0 {
		line = errors[0]
	}
	line = strings.TrimPrefix(line, errorPrefix)
	line = sigHexRe.ReplaceAllString(line, "<hex>")
	line = sigNumRe.ReplaceAllString(line, "<n>")
	return strings.TrimSpace(sigSpaceRe.ReplaceAllString(line, " "))
}

func classify(f *Failure, s *Section) Category {
	lines := append(append([]string{}, s.Body...), f.Errors...)
	switch {
	case anyLine(timeoutRe, lines):
		return CategoryTimeout
	case (f.ExitCode != nil && *f.ExitCode == 137) || anyLine(oomRe, lines):
		return CategoryOOM
	case len(f.FailedTests) > 0:
		return CategoryTest
	case lintCmdRe.MatchString(s.Title + "\n" + strings.Join(f.Command, "\n")):
		return CategoryLint
	case anyLine(driftRe, lines):
		return CategoryDrift
	case anyLine(depRe, lines):
		return CategoryDependency
	case anyLine(buildRe, lines):
		return CategoryBuild
	case anyLine(testRe, lines):
		return CategoryTest
	}
	return CategoryUnknown
}

func anyLine(re *regexp.Regexp, lines []string) bool {
	for _, l := range lines {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}
