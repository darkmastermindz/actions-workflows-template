# CI Ledger — bash implementation

Implements [`REQUIREMENTS.md`](../../REQUIREMENTS.md) using only **bash ≥ 4, curl and jq**.

## Use as an action

```yaml
permissions:
  actions: read
  pull-requests: write
steps:
  - uses: <owner>/<repo>/implementations/bash@<ref>
    with:
      budget_usd: "0.50"
```

## Run locally

All configuration goes through environment variables (spec §2.1); there are no arguments.

```bash
INPUT_GITHUB_TOKEN=ghp_xxx GITHUB_REPOSITORY=owner/repo INPUT_RUN_ID=123456 \
  ./ci-ledger.sh
```

## Test

```bash
tests/run-tests.sh             # full suite (about 10 s: the retry tests sleep 1 s + 2 s)
SKIP_SLOW=1 tests/run-tests.sh # skip the retry tests
```

The tests run offline. The end-to-end cases start `tests/mock_api.py`, a small local mock of the
GitHub REST API (python3 is needed only for the tests), and point `GITHUB_API_URL` at it. They cover
every example in spec §5, §6 and §7, the golden example in §13 (body, stdout, `GITHUB_OUTPUT`, JSON
report, `fail_on_budget`, `top_n=4`), the PATCH path with the marker comment on page 3 of 300
comments, 1 000 jobs over 11 pages, retries, HTTP and network failures, and a check that the token
never shows up in any process's arguments (NF3).

## Design notes

- **Few processes.** One `jq` call parses the event and `rates_json`. For each jobs page, a quick
  `jq` call reads the job count, then one background `jq` call works out every job's runner,
  minutes, rate and cost while the next page downloads. A final `jq` call sorts, totals and renders
  the markdown, the JSON report and the comment body in one pass. Runner detection runs once per
  distinct `labels` set. Nothing runs per job.
- **Exact math.** Money is integer micro-USD. Timestamps are split into integer epoch seconds plus
  the fraction digits; the fractions are compared as strings, so `ceil(seconds/60)` is exact.
  `budget_usd` is parsed as a decimal string in bash (any length). Rates go from their decimal text
  to micro-USD, also exactly.
- **Security (NF3).** The token is written only to a mode-600 curl config file (`--config`) in a
  private temp dir that is deleted on exit. Request bodies go through `--data-binary @file`.
- **Pure functions** live in the `CI_LEDGER_JQ_LIB` jq library and in small bash helpers
  (`parse_budget`, `parse_top_n`, `is_true`). Sourcing `ci-ledger.sh` defines them without running
  anything, which is what the unit tests rely on.
