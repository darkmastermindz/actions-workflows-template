# CI Ledger — Go implementation

Go (standard library only, Go >= 1.22) implementation of the CI Ledger action
specified in [`REQUIREMENTS.md`](../../REQUIREMENTS.md). It is configured only
through environment variables (spec §2.1) and takes no arguments.

## Run

```sh
go build -o ci-ledger .
GITHUB_TOKEN=... GITHUB_REPOSITORY=owner/repo GITHUB_RUN_ID=123 ./ci-ledger
```

Point `GITHUB_API_URL` at a local server to run offline.

## Use as an action

```yaml
- uses: <owner>/<repo>/implementations/go@<ref>
  with:
    budget_usd: "0.50"
```

`action.yml` is a composite action. The binary is **compiled on first use**
with the Go toolchain preinstalled on GitHub-hosted runners and cached in
`$RUNNER_TEMP/ci-ledger-go/` for the rest of the job.

Trade-off: building from source needs no release pipeline, no checked-in
binaries and no downloads (nothing leaves GitHub; only the standard library is
used), at the cost of a one-time compile per job (a few seconds on a cold build
cache). Alternatives would be committing per-OS binaries (fastest start, but
binaries in git and a release step) or `actions/setup-go` (network download).
If Go is missing or the build fails, the action prints a `::warning::` and
skips, so CI is never broken by the action itself.

## Test

```sh
go vet ./...
go test ./...                      # unit + end-to-end (httptest mock API)
go test -run x -bench . ./...      # 1000 jobs / 300 comments benchmark
```

Files: `ledger.go` (pure logic: detection, minutes, rates, money),
`render.go` (markdown + JSON report), `github.go` (HTTP, retries, pagination),
`main.go` (orchestration and side effects).

## Notes on spec interpretation

- Empty `INPUT_COMMENT` / `INPUT_FAIL_ON_BUDGET` take their defaults (`true` / `false`).
- `INPUT_RUN_ID`, `INPUT_PR_NUMBER` and `INPUT_TOP_N` are whitespace-trimmed; `top_n` must be all digits.
- Labels are lower-cased ASCII-only.
- Comment paging stops at the first page containing the marker (same result as reading every page, fewer requests).
- When page 1 of the jobs reports `total_count`, the remaining pages are fetched concurrently (max 8 in flight) and then consumed in page order, still ending at the first page with < 100 jobs.
- A 2xx response whose body is not valid JSON is reported as `(HTTP <code>)`.
- A `status` that is present but not the string `completed` (including `""`) is counted as in progress.
