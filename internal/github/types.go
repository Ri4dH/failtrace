package github

import "time"

// Run is the subset of a workflow run that FailTrace uses.
type Run struct {
	ID             int64       `json:"id"`
	Name           string      `json:"name"`
	DisplayTitle   string      `json:"display_title"`
	HeadBranch     string      `json:"head_branch"`
	HeadSHA        string      `json:"head_sha"`
	Path           string      `json:"path"`
	Event          string      `json:"event"`
	Status         string      `json:"status"`
	Conclusion     string      `json:"conclusion"`
	RunAttempt     int         `json:"run_attempt"`
	HTMLURL        string      `json:"html_url"`
	HeadRepository *Repository `json:"head_repository"`
}

// Repository is a minimal repository reference.
type Repository struct {
	FullName string `json:"full_name"`
}

// Job is a single job within a workflow run attempt.
type Job struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	HTMLURL     string     `json:"html_url"`
	Labels      []string   `json:"labels"`
	RunnerName  string     `json:"runner_name"`
	Steps       []Step     `json:"steps"`
}

// Step is a step as reported by the jobs API.
type Step struct {
	Name       string `json:"name"`
	Number     int    `json:"number"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}
