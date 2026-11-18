// Package runurl parses GitHub Actions run URLs into a typed reference.
package runurl

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Ref identifies a workflow run, optionally narrowed to an attempt or a job.
type Ref struct {
	Host    string `json:"host"`
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	RunID   int64  `json:"run_id"`
	Attempt int    `json:"attempt,omitempty"` // 0 means "latest attempt"
	JobID   int64  `json:"job_id,omitempty"`  // 0 means "all failed jobs"
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ErrNotRunURL is returned when the URL does not point at an Actions run.
var ErrNotRunURL = errors.New("not a GitHub Actions run URL (expected https://github.com/<owner>/<repo>/actions/runs/<id>)")

// Parse accepts URLs such as:
//
//	https://github.com/owner/repo/actions/runs/123
//	https://github.com/owner/repo/actions/runs/123/job/456
//	https://github.com/owner/repo/actions/runs/123/attempts/2
func Parse(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, ErrNotRunURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Ref{}, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return Ref{}, fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return Ref{}, ErrNotRunURL
	}
	if host == "www.github.com" {
		host = "github.com"
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 || parts[2] != "actions" || parts[3] != "runs" {
		return Ref{}, ErrNotRunURL
	}
	ref := Ref{Host: host, Owner: parts[0], Repo: parts[1]}
	if err := validateName(ref.Owner); err != nil {
		return Ref{}, fmt.Errorf("invalid owner: %w", err)
	}
	if err := validateName(ref.Repo); err != nil {
		return Ref{}, fmt.Errorf("invalid repository: %w", err)
	}
	if ref.RunID, err = parseID(parts[4]); err != nil {
		return Ref{}, fmt.Errorf("invalid run id: %w", err)
	}

	rest := parts[5:]
	for len(rest) > 0 {
		if len(rest) < 2 {
			return Ref{}, fmt.Errorf("unexpected trailing path segment %q", rest[0])
		}
		switch rest[0] {
		case "job":
			if ref.JobID, err = parseID(rest[1]); err != nil {
				return Ref{}, fmt.Errorf("invalid job id: %w", err)
			}
		case "attempts":
			n, err := parseID(rest[1])
			if err != nil || n > 1000 {
				return Ref{}, fmt.Errorf("invalid attempt number %q", rest[1])
			}
			ref.Attempt = int(n)
		default:
			return Ref{}, fmt.Errorf("unexpected path segment %q", rest[0])
		}
		rest = rest[2:]
	}
	return ref, nil
}

// APIBase returns the REST API root for the ref's host (github.com or GHES).
func (r Ref) APIBase() string {
	if r.Host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + r.Host + "/api/v3"
}

// Repository returns "owner/repo".
func (r Ref) Repository() string { return r.Owner + "/" + r.Repo }

func validateName(s string) error {
	if s == "." || s == ".." || !nameRe.MatchString(s) {
		return fmt.Errorf("%q contains invalid characters", s)
	}
	return nil
}

func parseID(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive integer", s)
	}
	return n, nil
}
