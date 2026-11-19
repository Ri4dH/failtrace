// Package joblog parses GitHub Actions job logs into per-step sections.
package joblog

import (
	"regexp"
	"strings"
)

const (
	groupPrefix   = "##[group]"
	endGroup      = "##[endgroup]"
	errorPrefix   = "##[error]"
	stepPrefix    = "##[group]Run "
	postStepStart = "Post job cleanup."
	setupTitle    = "Set up job"
)

var (
	timestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z ?`)
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	exitCodeRe  = regexp.MustCompile(`Process completed with exit code (\d+)`)
	runnerVerRe = regexp.MustCompile(`^Current runner version: '([^']+)'`)
)

// Log is a parsed job log.
type Log struct {
	Sections []*Section `json:"sections"`
	Runner   RunnerInfo `json:"runner"`
}

// RunnerInfo describes the machine the job ran on, as printed in the log.
type RunnerInfo struct {
	Version      string `json:"version,omitempty"`
	Image        string `json:"image,omitempty"`
	ImageVersion string `json:"image_version,omitempty"`
}

// Section is the log output of one step.
type Section struct {
	Title  string   `json:"title"`
	Header Header   `json:"header"`
	Body   []string `json:"body"`
	Errors []string `json:"errors,omitempty"`
	Post   bool     `json:"post,omitempty"`
}

// Header is the collapsed group GitHub prints before each step: the evaluated
// script (for run steps), the shell, and the step's inputs and environment.
type Header struct {
	Script []string          `json:"script,omitempty"`
	Shell  string            `json:"shell,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	With   map[string]string `json:"with,omitempty"`
}

// Parse splits a raw job log into sections, one per step.
func Parse(raw string) *Log {
	raw = strings.TrimPrefix(raw, "\uFEFF")
	log := &Log{}
	cur := &Section{Title: setupTitle}
	log.Sections = append(log.Sections, cur)

	inHeader, inRunnerImage := false, false
	var hs headerState
	for _, line := range strings.Split(raw, "\n") {
		line = Clean(line)

		if !inHeader && strings.HasPrefix(line, stepPrefix) {
			cur = &Section{Title: strings.TrimPrefix(line, groupPrefix)}
			log.Sections = append(log.Sections, cur)
			inHeader, hs = true, headerState{}
			continue
		}
		if !inHeader && line == postStepStart {
			cur = &Section{Title: "Post job cleanup", Post: true}
			log.Sections = append(log.Sections, cur)
			continue
		}
		if inHeader {
			if line == endGroup {
				inHeader = false
			} else {
				parseHeaderLine(&cur.Header, line, &hs)
			}
			continue
		}

		if msg, ok := strings.CutPrefix(line, errorPrefix); ok {
			cur.Errors = append(cur.Errors, msg)
		}
		if cur == log.Sections[0] {
			inRunnerImage = parseRunnerInfo(&log.Runner, line, inRunnerImage)
		}
		cur.Body = append(cur.Body, line)
	}
	for _, s := range log.Sections {
		s.Body = trimTrailingBlank(s.Body)
	}
	return log
}

// FailedSection returns the step section that failed: the one that reported a
// non-zero exit code, or otherwise the first non-post section with an error.
func (l *Log) FailedSection() *Section {
	var firstErr *Section
	for _, s := range l.Sections {
		if s.Post {
			continue
		}
		for _, e := range s.Errors {
			if exitCodeRe.MatchString(e) {
				return s
			}
		}
		if firstErr == nil && len(s.Errors) > 0 {
			firstErr = s
		}
	}
	return firstErr
}

// Clean strips the timestamp prefix, ANSI escapes and trailing CR from a line.
func Clean(line string) string {
	line = strings.TrimRight(line, "\r")
	line = timestampRe.ReplaceAllString(line, "")
	return ansiRe.ReplaceAllString(line, "")
}

// headerState tracks where parseHeaderLine is in a step header.
type headerState struct {
	block string // "", "shell", "env" or "with"
	key   string // the last env or with key, for multi-line values
}

// headerEntryRe matches an env or with entry ("  NAME: value"). Other lines
// in those blocks continue the previous value: GitHub prints multi-line
// values (JSON, scripts) as they are, without indentation.
var headerEntryRe = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_.-]*):(?: (.*))?$`)

func parseHeaderLine(h *Header, line string, st *headerState) {
	switch {
	case strings.HasPrefix(line, "shell: "):
		h.Shell = strings.TrimPrefix(line, "shell: ")
		*st = headerState{block: "shell"}
	case line == "env:" || line == "with:":
		*st = headerState{block: strings.TrimSuffix(line, ":")}
	case st.block == "env" || st.block == "with":
		target := &h.Env
		if st.block == "with" {
			target = &h.With
		}
		if *target == nil {
			*target = map[string]string{}
		}
		if m := headerEntryRe.FindStringSubmatch(line); m != nil {
			st.key = m[1]
			(*target)[m[1]] = strings.TrimSpace(m[2])
		} else if st.key != "" {
			(*target)[st.key] += "\n" + line
		}
	case st.block == "":
		h.Script = append(h.Script, line)
	}
}

func parseRunnerInfo(r *RunnerInfo, line string, inImage bool) bool {
	if m := runnerVerRe.FindStringSubmatch(line); m != nil {
		r.Version = m[1]
	}
	switch {
	case line == groupPrefix+"Runner Image":
		return true
	case inImage && line == endGroup:
		return false
	case inImage && strings.HasPrefix(line, "Image: "):
		r.Image = strings.TrimPrefix(line, "Image: ")
	case inImage && strings.HasPrefix(line, "Version: "):
		r.ImageVersion = strings.TrimPrefix(line, "Version: ")
	}
	return inImage
}

func trimTrailingBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
