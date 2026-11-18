# FailTrace

**Stop guessing why CI failed. Reproduce it locally.**

Paste a failed GitHub Actions run URL. FailTrace finds the job and step that failed, matches them to the
workflow file at that exact commit, pulls the failure out of the logs, and tells you what stands between
you and reproducing it on your machine.

```bash
failtrace inspect https://github.com/owner/repo/actions/runs/123456789
```

> **Status: early development.** `inspect` (diagnosis) and `plan` (a dry-run reproduction plan) work
> today. Executing the plan in an isolated container (`repro`) is the next milestone. See the
> [roadmap](#roadmap).

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

## Planning a reproduction

`failtrace plan` works out how to reproduce the failed job locally and prints the plan. It doesn't run
anything yet.

```bash
failtrace plan https://github.com/owner/repo/actions/runs/123456789
failtrace plan --job 987654321 <run-url>     # pick a job when several failed
failtrace plan --json <run-url>              # the full plan, including every env var
```

Real output for a failed [pytest-dev/pytest](https://github.com/pytest-dev/pytest) job (trimmed):

```text
Reproduction plan · pytest-dev/pytest
  Job        build (ubuntu-py311)  (jobs.build)
  Verdict    PARTIAL: can run, but the warnings below may make it differ from CI
  Image      ubuntu:26.04  linux/amd64
             matches the runner image ubuntu-26.04 20261111.196.1
  Commit     c484767

  Toolchains
    python   3.11.18    exact · asked for "3.11" · from actions/setup-python@6d4fd22
    uv       0.13.8     exact · from astral-sh/setup-uv@11f9893

  Steps
    1  checkout    Run actions/checkout@3d3c42e
                    pytest-dev/pytest @ c484767 (merge of 58b8dae into 50234b4)
    2  skip        Download Package
                    — downloads artifacts from another job
    3  toolchain   Set up Python 3.11
                    → install python 3.11.18
    4  composite   Run pytest-dev/pytest/.github/actions/setup-tox@c484767
                    — composite action; its steps follow
    5    toolchain   Run astral-sh/setup-uv@11f9893
                      → install uv 0.13.8
    6    run         $ if [ -n "$PYTHON_VERSION" ]; then  (+4 lines)
    7  run         Test with coverage  ← failed in CI
                    $ tox run -e py311-coverage --installpkg `find dist/*.tar.gz`
                    shell: /usr/bin/bash --noprofile --norc -e -o pipefail {0}

  Issues
    ! [step 2] actions/download-artifact@9000827 downloads artifacts produced by another job or run; files it provides will be missing locally
    i [step 1] CI tested the merge of PR head 58b8dae into base 50234b4 (merge commit c484767), not the PR branch alone; the plan checks out that merge commit
```

How the plan is built:

- **The log is the source of truth for what ran.** Scripts, shells and environment come from the step
  headers GitHub prints, so `${{ matrix.* }}` and other expressions are already filled in. Composite
  actions show their inner steps.
- **The exact commit CI tested.** For pull requests that's a merge commit GitHub created, not your branch
  head. This is a common reason a failure "doesn't happen locally".
- **The exact toolchain versions.** `"3.11"` in the workflow becomes `3.11.18`, read from the runner's
  tool cache paths. When the log doesn't show a version, it reads version files such as `go.mod`
  (including `toolchain`), `.nvmrc`, `.python-version`, `.tool-versions` and `packageManager` in
  `package.json`.
- **The right base image.** A job `container:` is used as is. Otherwise it picks the Ubuntu version
  matching the runner image the job actually got.
- **What to skip and what to flag.** Caches, uploads and coverage reporters are skipped. Downloaded
  artifacts, service containers, secrets, unknown actions, and tools used without a setup step (they were
  preinstalled on the runner) are reported as issues.

The **verdict** is `ready` (nothing known to be missing), `partial` (it can run, but some parts are
approximated or missing) or `blocked` (the failing step is an action, not a script, so it can't be replayed
yet).

Supported setup actions: `actions/setup-go`, `setup-node`, `setup-python`, `setup-java`, `setup-dotnet`,
`ruby/setup-ruby`, `dtolnay/rust-toolchain`, `actions-rust-lang/setup-rust-toolchain`,
`pnpm/action-setup`, `pnpm/setup`, `astral-sh/setup-uv`, `oven-sh/setup-bun` and `denoland/setup-deno`.

## Roadmap

- [x] **`inspect`**: diagnose a failed run from its URL
- [x] **`plan`**: turn the failed job into a local reproduction plan: base image, exact commit, toolchain
  versions, env, steps to replay
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
internal/plan/      builds reproduction plans (image, commit, toolchains, env, steps)
internal/report/    terminal rendering
```

## Contributing

Issues and pull requests are welcome. The most useful contributions right now:

- **Failed runs that FailTrace gets wrong.** Open an issue with the run URL and what you expected.
- **New failure signatures:** test runners, package managers and compilers that aren't detected yet.
  These live in `internal/joblog/analyze.go` and need a test case.
- **More known actions:** setup actions (`internal/plan/toolchains.go`) and actions that are safe to
  skip locally (`internal/plan/actions.go`). Each one is a single table entry.

## License

[MIT](LICENSE)
