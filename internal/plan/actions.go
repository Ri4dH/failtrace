package plan

import "strings"

type actionKind int

const (
	actionUnknown actionKind = iota
	actionCheckout
	actionSetup
	actionSkip
	actionDownload
)

type actionInfo struct {
	kind   actionKind
	setup  *setupAction
	reason string
}

// skipActions do nothing that matters for reproducing a failure locally.
// Entries match the action name or any sub-action (actions/cache/restore).
var skipActions = map[string]string{
	"actions/cache":                            "restores or saves a cache only",
	"actions/upload-artifact":                  "uploads results after the fact",
	"actions/upload-pages-artifact":            "uploads a Pages artifact",
	"actions/deploy-pages":                     "deploys GitHub Pages",
	"actions/configure-pages":                  "configures GitHub Pages",
	"actions/attest-build-provenance":          "creates an attestation",
	"actions/labeler":                          "labels pull requests",
	"codecov/codecov-action":                   "uploads coverage",
	"coverallsapp/github-action":               "uploads coverage",
	"step-security/harden-runner":              "hardens the CI runner",
	"swatinem/rust-cache":                      "restores or saves a cache only",
	"github/codeql-action/upload-sarif":        "uploads scan results",
	"mikepenz/action-junit-report":             "publishes a test report",
	"dorny/test-reporter":                      "publishes a test report",
	"enricomi/publish-unit-test-result-action": "publishes a test report",
}

// lookupAction classifies a `uses:` reference such as "actions/setup-go@v5".
func lookupAction(uses string) actionInfo {
	name := strings.ToLower(uses)
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	switch {
	case name == "actions/checkout":
		return actionInfo{kind: actionCheckout}
	case name == "actions/download-artifact":
		return actionInfo{kind: actionDownload, reason: "downloads artifacts from another job"}
	}
	for i := range setupActions {
		if setupActions[i].name == name {
			return actionInfo{kind: actionSetup, setup: &setupActions[i]}
		}
	}
	for prefix, reason := range skipActions {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return actionInfo{kind: actionSkip, reason: reason}
		}
	}
	return actionInfo{kind: actionUnknown}
}
