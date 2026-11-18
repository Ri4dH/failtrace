// Package github is a small, dependency-free client for the GitHub Actions REST API.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxLogBytes  = 64 << 20 // job logs can be large; cap what we hold in memory
	maxFileBytes = 5 << 20
	jobsPerPage  = 100
	maxJobPages  = 10
	apiVersion   = "2022-11-28"
)

// Client talks to the GitHub REST API.
type Client struct {
	baseURL   string
	token     string
	http      *http.Client
	userAgent string
}

// NewClient returns a client for the given API root (e.g. https://api.github.com).
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		http:      &http.Client{Timeout: 2 * time.Minute},
		userAgent: "failtrace",
	}
}

// APIError is a non-2xx response from the API.
type APIError struct {
	StatusCode int
	Message    string
	Path       string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("GitHub API %s: %d %s", e.Path, e.StatusCode, e.Message)
	switch e.StatusCode {
	case http.StatusUnauthorized:
		msg += " (token is invalid or expired; try `gh auth login`)"
	case http.StatusForbidden:
		msg += " (rate limited or token lacks permission; private repos need the `repo` scope)"
	case http.StatusNotFound:
		msg += " (check the URL, and that your token can read this repository)"
	case http.StatusGone:
		msg += " (the logs for this run have likely expired)"
	}
	return msg
}

// GetRun fetches a workflow run.
func (c *Client) GetRun(ctx context.Context, owner, repo string, runID int64) (*Run, error) {
	var run Run
	path := fmt.Sprintf("%s/actions/runs/%d", repoPath(owner, repo), runID)
	if err := c.getJSON(ctx, path, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// ListJobs lists the jobs of a run attempt. attempt <= 0 lists the latest attempt.
func (c *Client) ListJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]Job, error) {
	var all []Job
	for page := 1; page <= maxJobPages; page++ {
		var path string
		if attempt > 0 {
			path = fmt.Sprintf("%s/actions/runs/%d/attempts/%d/jobs?per_page=%d&page=%d",
				repoPath(owner, repo), runID, attempt, jobsPerPage, page)
		} else {
			path = fmt.Sprintf("%s/actions/runs/%d/jobs?filter=latest&per_page=%d&page=%d",
				repoPath(owner, repo), runID, jobsPerPage, page)
		}
		var resp struct {
			TotalCount int   `json:"total_count"`
			Jobs       []Job `json:"jobs"`
		}
		if err := c.getJSON(ctx, path, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Jobs...)
		if len(resp.Jobs) < jobsPerPage || len(all) >= resp.TotalCount {
			break
		}
	}
	return all, nil
}

// GetJobLog downloads the plain-text log of a job.
// The API answers with a redirect to blob storage; net/http follows it and
// drops the Authorization header because the host changes.
func (c *Client) GetJobLog(ctx context.Context, owner, repo string, jobID int64) (string, error) {
	path := fmt.Sprintf("%s/actions/jobs/%d/logs", repoPath(owner, repo), jobID)
	resp, err := c.do(ctx, path, "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))
	if err != nil {
		return "", fmt.Errorf("read job log: %w", err)
	}
	return string(data), nil
}

// GetFile returns the raw contents of a repository file at ref.
func (c *Client) GetFile(ctx context.Context, owner, repo, filePath, ref string) ([]byte, error) {
	escaped, err := escapeRepoPath(filePath)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("%s/contents/%s?ref=%s", repoPath(owner, repo), escaped, url.QueryEscape(ref))
	resp, err := c.do(ctx, path, "application/vnd.github.raw+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}
	return data, nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.do(ctx, path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, path, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub API %s: %w", stripQuery(path), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Message == "" {
			payload.Message = http.StatusText(resp.StatusCode)
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: payload.Message, Path: stripQuery(path)}
	}
	return resp, nil
}

func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// escapeRepoPath escapes each segment and rejects traversal attempts.
func escapeRepoPath(p string) (string, error) {
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("invalid repository path %q", p)
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("invalid repository path %q", p)
		}
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/"), nil
}

func stripQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}
