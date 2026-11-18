package cli

import (
	"context"
	"fmt"

	"github.com/Ri4dH/failtrace/internal/github"
	"github.com/Ri4dH/failtrace/internal/inspect"
	"github.com/Ri4dH/failtrace/internal/runurl"
)

// fetched is an inspected run plus the client used to fetch it.
type fetched struct {
	ref    runurl.Ref
	client *github.Client
	report *inspect.Report
}

// fetchRun validates the user's input, authenticates, and inspects the run.
func fetchRun(ctx context.Context, rawURL string, jobID int64, excerptLines int) (*fetched, error) {
	ref, err := runurl.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if jobID < 0 {
		return nil, fmt.Errorf("--job must be a positive job ID")
	}
	if jobID != 0 {
		ref.JobID = jobID
	}
	token, err := github.ResolveToken(ctx, ref.Host)
	if err != nil {
		return nil, err
	}
	client := github.NewClient(ref.APIBase(), token)
	rep, err := inspect.Run(ctx, client, ref, inspect.Options{ExcerptLines: excerptLines})
	if err != nil {
		return nil, err
	}
	return &fetched{ref: ref, client: client, report: rep}, nil
}
