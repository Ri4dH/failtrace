package runurl

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Ref
	}{
		{"https://github.com/acme/widget/actions/runs/123", Ref{Host: "github.com", Owner: "acme", Repo: "widget", RunID: 123}},
		{"https://github.com/acme/widget/actions/runs/123/job/456", Ref{Host: "github.com", Owner: "acme", Repo: "widget", RunID: 123, JobID: 456}},
		{"https://github.com/acme/widget/actions/runs/123/attempts/2", Ref{Host: "github.com", Owner: "acme", Repo: "widget", RunID: 123, Attempt: 2}},
		{"https://github.com/acme/widget/actions/runs/123/job/456?pr=7#step:4:12", Ref{Host: "github.com", Owner: "acme", Repo: "widget", RunID: 123, JobID: 456}},
		{"  github.com/acme/my.repo_x/actions/runs/9/  ", Ref{Host: "github.com", Owner: "acme", Repo: "my.repo_x", RunID: 9}},
		{"https://www.github.com/acme/widget/actions/runs/1", Ref{Host: "github.com", Owner: "acme", Repo: "widget", RunID: 1}},
		{"https://ghe.corp.example/acme/widget/actions/runs/5", Ref{Host: "ghe.corp.example", Owner: "acme", Repo: "widget", RunID: 5}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	bad := []string{
		"",
		"https://github.com/acme/widget",
		"https://github.com/acme/widget/pull/12",
		"https://github.com/acme/widget/actions/runs/abc",
		"https://github.com/acme/widget/actions/runs/0",
		"https://github.com/acme/widget/actions/runs/-4",
		"https://github.com/acme/widget/actions/runs/1/job",
		"https://github.com/acme/widget/actions/runs/1/logs/2",
		"https://github.com/ac%20me/widget/actions/runs/1",
		"https://github.com/acme/../actions/runs/1",
		"ftp://github.com/acme/widget/actions/runs/1",
	}
	for _, in := range bad {
		if ref, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %+v, want error", in, ref)
		}
	}
}

func TestAPIBase(t *testing.T) {
	if got := (Ref{Host: "github.com"}).APIBase(); got != "https://api.github.com" {
		t.Errorf("github.com API base = %q", got)
	}
	if got := (Ref{Host: "ghe.corp.example"}).APIBase(); got != "https://ghe.corp.example/api/v3" {
		t.Errorf("GHES API base = %q", got)
	}
}
