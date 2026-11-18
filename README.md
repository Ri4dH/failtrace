# FailTrace

**Stop guessing why CI failed. Reproduce it locally.**

Paste a failed GitHub Actions run URL. FailTrace finds the job and step that failed, matches them to the
workflow file at that exact commit, pulls the failure out of the logs, and tells you what stands between
you and reproducing it on your machine.

```bash
failtrace inspect https://github.com/owner/repo/actions/runs/123456789
```

> **Status: early development.** `inspect` (diagnosis) works today. Local reproduction in an isolated
> container (`plan` / `repro`) is the next milestone. See the [roadmap](#roadmap).

## Why not `act`?

[`act`](https://github.com/nektos/act) runs your workflows locally, and it's great at that. FailTrace
answers a different question: *why did this particular run fail, and can I make it fail the same way here?*

- **It starts from a failed run, not a workflow file.** You get the exact commit, attempt, job,
  matrix combination, and failing step. Nothing to configure.
- **It focuses on the failure.** The goal is to replay only what the failing step needs, not the
  whole workflow.
- **It gives you a verdict.** You get a diagnosis now. Later you'll also get reproduced / not reproduced
  / flaky, plus what differed between CI and your machine.

## Example

Real output for a failed [prometheus/prometheus](https://github.com/prometheus/prometheus) run (trimmed):

```text
✗ CI · prometheus/prometheus
  "remote/otlp: remove delta-to-cumulative conversion"
  pull_request · on codex/remove-otlp-deltatocumulative · @ cf7d324 · attempt 1 · failure

────────────────────────────────────────────────────────────
✗ Job: Go tests  (failure)
  Runner     ubuntu-latest → ubuntu-26.04 (20261116.202.1) · runner 2.338.2
  Workflow   .github/workflows/ci.yml → jobs.test_go

  Steps
    ✓  5  Run make GO_ONLY=1 SKIP_GOLANGCI_LINT=1
    ✓  6  Run go test ./tsdb/ -test.tsdb-isolation=false
    ✗  7  Run make -C documentation/examples/remote_storage  ← failed here
    ○  8  Run make -C documentation/examples (skipped)

  Failed step  #7 Run make -C documentation/examples/remote_storage
    shell: sh -e {0}
    $ make -C documentation/examples/remote_storage

  Diagnosis  files out of date (a check produced a git diff) · exit code 2

  Log excerpt (10 lines around the first error in "Run make -C documentation/examples/remote_storage")
    │ >> running check for unused/missing packages in go.mod
    │ go mod tidy
    │ go: downloading github.com/oapi-codegen/nullable v1.1.0
    │ diff --git a/documentation/examples/remote_storage/go.mod b/documentation/examples/remote_storage/go.mod
    │ --- a/documentation/examples/remote_storage/go.mod
    │ +++ b/documentation/examples/remote_storage/go.mod

  Reproduction notes
    ! job runs inside container quay.io/prometheus/golang-builder:1.27-base
```

The log ends with hundreds of lines of `go.sum` diff. FailTrace centres the excerpt on the first error
line so you see the cause, `go mod tidy` changing `go.mod`, instead of the noise after it.

## Install

Requires Go 1.24+.

```bash
go install github.com/Ri4dH/failtrace/cmd/failtrace@latest
```

Or build from source:

```bash
git clone https://github.com/Ri4dH/failtrace
cd failtrace
go build -o bin/failtrace ./cmd/failtrace
```

## Usage

```bash
# All failed jobs in the latest attempt of a run
failtrace inspect https://github.com/owner/repo/actions/runs/123456789

# A single job (the URL you get from clicking a job in the Actions UI)
failtrace inspect https://github.com/owner/repo/actions/runs/123456789/job/987654321

# A specific attempt of a re-run workflow
failtrace inspect https://github.com/owner/repo/actions/runs/123456789/attempts/2

# Show more log context, or emit JSON for scripts and other tools
failtrace inspect -n 60 <run-url>
failtrace inspect --json <run-url> | jq '.failed_jobs[0].failure'
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | off | Print the report as JSON |
| `-n, --lines` | 25 | Log lines to show from the failed step |
| `--job` | all failed | Inspect only this job ID |

Colour output turns off automatically when stdout is not a terminal, or when `NO_COLOR` is set.

### Authentication

FailTrace needs a GitHub token, even for public repositories, because the logs API requires one. It looks in
this order:

1. `GITHUB_TOKEN`
2. `GH_TOKEN`
3. `gh auth token` (if you use the [GitHub CLI](https://cli.github.com/), this already works)

Private repositories need a token that can read Actions on that repo. GitHub Enterprise Server URLs work too:
the API base becomes `https://<host>/api/v3`.

## What `inspect` reports

- **Run context:** workflow, event, branch, commit, attempt.
- **Job → workflow mapping:** finds the job definition, including jobs named like
  `test (ubuntu-latest, 1.22)` or `Lint ${{ matrix.tool }}`, and recovers the matrix values.
- **Runner:** the requested label and the actual runner image and version, from the log.
- **Failed step:** its definition (`run`, `uses`, `with`, `env`, shell, working directory). The command
  shown is the one printed in the log, with expressions already filled in.
- **Diagnosis:** exit code, error annotations, failing test names (Go, pytest, Jest, Vitest, Cargo, RSpec),
  and a category:

  | Category | Example signal |
  |----------|----------------|
  | test | `--- FAIL: TestX`, `FAILED tests/x.py::test_y` |
  | build | `error TS2304`, `undefined: foo`, `error[E0425]` |
  | dependency | `npm ERR! code ERESOLVE`, `missing go.sum entry` |
  | lint | `golangci-lint`, `eslint`, `ruff` steps |
  | generated-diff | a check that printed `diff --git` (tidy, codegen, formatters) |
  | timeout | `exceeded the maximum execution time` |
  | out-of-memory | exit code 137, `heap out of memory` |

- **Reproduction notes:** anything that makes a local repro harder. That means service containers, job
  containers, secrets the step uses, reusable workflows, and macOS or Windows runners.

It also explains edge cases instead of failing silently: runs that failed before any job started, dynamic
workflows with no file in the repo, and logs that have expired.

## Roadmap

- [x] **`inspect`**: diagnose a failed run from its URL
- [ ] **`plan`**: turn the failed job into a local reproduction plan: base image, toolchain versions from
  `setup-go` / `setup-node` / `setup-python`, env, working directory, steps to replay
- [ ] **`repro`**: run the plan in an isolated Docker container, then compare exit code and error signature
  with CI
- [ ] **Reports**: Markdown report with a verdict (reproduced / not reproduced) and the environment
  differences between CI and local
- [ ] **Flaky detection**: `--runs N` to repeat a reproduction
- [ ] **`--shell`**: drop into the container at the failing step
- [ ] **`gh` extension**: `gh failtrace <run-url>`

**Scope for the first reproduction release:** Linux runners, `run:` steps, and Go, Node and Python
toolchains. macOS and Windows runners, Docker-in-Docker, and secrets are out of scope for now. FailTrace
will tell you when a failure depends on them.

## Development

```bash
go test ./...          # unit tests (no network needed)
go test -race ./...
go vet ./...
```

Project layout:

```text
cmd/failtrace/      CLI entry point
internal/cli/       commands and flags
internal/runurl/    run URL parsing and validation
internal/github/    minimal GitHub REST client and token lookup
internal/joblog/    job log parsing, failure extraction and classification
internal/workflow/  workflow YAML parsing, job/step/matrix matching
internal/inspect/   puts it all together into a report
internal/report/    terminal rendering
```

## Contributing

Issues and pull requests are welcome. The most useful contributions right now:

- **Failed runs that FailTrace gets wrong.** Open an issue with the run URL and what you expected.
- **New failure signatures:** test runners, package managers and compilers that aren't detected yet.
  These live in `internal/joblog/analyze.go` and need a test case.

## License

[MIT](LICENSE)
