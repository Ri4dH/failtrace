package joblog

import "regexp"

var (
	// actions/checkout fetches the exact commit: "+<sha>:refs/remotes/pull/123/merge".
	fetchRe = regexp.MustCompile(`\+([0-9a-f]{40}):refs/remotes/(\S+)`)
	// For pull requests the checked-out commit is a merge GitHub created.
	mergeRe = regexp.MustCompile(`^HEAD is now at [0-9a-f]+ Merge ([0-9a-f]{40}) into ([0-9a-f]{40})`)
)

// CheckoutFacts is what actions/checkout printed about the commit it checked out.
type CheckoutFacts struct {
	Commit    string `json:"commit,omitempty"`     // full SHA that was fetched and checked out
	Ref       string `json:"ref,omitempty"`        // e.g. "pull/123/merge" or "origin/main"
	MergeHead string `json:"merge_head,omitempty"` // PR head, when Commit is a merge commit
	MergeBase string `json:"merge_base,omitempty"` // base branch tip, when Commit is a merge commit
}

// CheckoutFacts extracts checkout details from an actions/checkout section.
func (s *Section) CheckoutFacts() CheckoutFacts {
	var f CheckoutFacts
	for _, l := range s.Body {
		if f.Commit == "" {
			if m := fetchRe.FindStringSubmatch(l); m != nil {
				f.Commit, f.Ref = m[1], m[2]
			}
		}
		if m := mergeRe.FindStringSubmatch(l); m != nil {
			f.MergeHead, f.MergeBase = m[1], m[2]
		}
	}
	return f
}
