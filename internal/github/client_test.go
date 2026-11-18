package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/repos/acme/widget/actions/runs/42", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"name":"CI","head_sha":"abc123","path":".github/workflows/ci.yml","run_attempt":2,"conclusion":"failure"}`))
	}))
	mux.HandleFunc("/repos/acme/widget/actions/runs/42/attempts/2/jobs", auth(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		n := 100
		if page == "2" {
			n = 50
		}
		jobs := make([]Job, n)
		for i := range jobs {
			jobs[i] = Job{ID: int64(i + 1), Name: fmt.Sprintf("job-%s-%d", page, i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 150, "jobs": jobs})
	}))
	mux.HandleFunc("/repos/acme/widget/actions/jobs/7/logs", auth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/blob/7", http.StatusFound)
	}))
	mux.HandleFunc("/blob/7", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("log line 1\nlog line 2\n"))
	})
	mux.HandleFunc("/repos/acme/widget/contents/.github/workflows/ci.yml", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "abc123" || !strings.Contains(r.Header.Get("Accept"), "raw") {
			http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("name: CI\n"))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "tok"), srv
}

func TestGetRun(t *testing.T) {
	c, _ := newTestServer(t)
	run, err := c.GetRun(context.Background(), "acme", "widget", 42)
	if err != nil {
		t.Fatal(err)
	}
	if run.HeadSHA != "abc123" || run.RunAttempt != 2 || run.Path != ".github/workflows/ci.yml" {
		t.Errorf("run = %+v", run)
	}
}

func TestListJobsPaginates(t *testing.T) {
	c, _ := newTestServer(t)
	jobs, err := c.ListJobs(context.Background(), "acme", "widget", 42, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 150 {
		t.Fatalf("got %d jobs, want 150", len(jobs))
	}
}

func TestGetJobLogFollowsRedirect(t *testing.T) {
	c, _ := newTestServer(t)
	log, err := c.GetJobLog(context.Background(), "acme", "widget", 7)
	if err != nil {
		t.Fatal(err)
	}
	if log != "log line 1\nlog line 2\n" {
		t.Errorf("log = %q", log)
	}
}

func TestGetFile(t *testing.T) {
	c, _ := newTestServer(t)
	data, err := c.GetFile(context.Background(), "acme", "widget", ".github/workflows/ci.yml", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "name: CI\n" {
		t.Errorf("data = %q", data)
	}
}

func TestGetFileRejectsTraversal(t *testing.T) {
	c, _ := newTestServer(t)
	for _, p := range []string{"../secrets", ".github/../../x", "/etc/passwd", "", "a//b"} {
		if _, err := c.GetFile(context.Background(), "acme", "widget", p, "abc123"); err == nil {
			t.Errorf("GetFile(%q) succeeded, want error", p)
		}
	}
}

func TestAPIErrors(t *testing.T) {
	c, srv := newTestServer(t)

	_, err := c.GetRun(context.Background(), "acme", "widget", 999)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want 404 APIError", err)
	}
	if !strings.Contains(err.Error(), "check the URL") {
		t.Errorf("404 error should include a hint: %v", err)
	}

	bad := NewClient(srv.URL, "wrong")
	_, err = bad.GetRun(context.Background(), "acme", "widget", 42)
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v, want 401 with API message", err)
	}
}
