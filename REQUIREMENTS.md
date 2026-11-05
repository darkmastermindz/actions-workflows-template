# CI Ledger — Requirements

**CI Ledger** is a GitHub Action that attributes a USD cost to every job of a
GitHub Actions workflow run and reports the breakdown as a job summary, an
idempotent PR comment, step outputs and a JSON report. It runs entirely inside
the user's CI — no external service, no sign-up, no data leaves GitHub.

This document is the single source of truth for every implementation
(bash, Go, Node.js). Implementations MUST produce **byte-identical** markdown,
stdout lines and step outputs for the same inputs, so they can be compared and
benchmarked against each other.

Requirement levels: **MUST** / **SHOULD** / **MAY** (RFC 2119).
Items tagged **[IMPROVEMENT]** go beyond the feature set of the tool this
action is modelled on; they are still mandatory unless marked SHOULD/MAY.

---

## 1. Feature summary

| # | Feature | Notes |
|---|---|---|
| F1 | Fetch all jobs of a workflow run (paginated) | GitHub REST API |
| F2 | Detect runner OS / core count / self-hosted from job labels | §5 |
| F3 | Bill each job per **started minute** (round up) | §6 |
| F4 | Price each job from a built-in list-price rate table | §7 |
| F5 | Sort jobs by cost, compute run totals | §8 |
| F6 | Optional per-run budget with OVER / ok flag | §8 |
| F7 | Render a markdown report | §9 |
| F8 | Append report to the job summary | `GITHUB_STEP_SUMMARY` |
| F9 | Create **or update** a single PR comment (idempotent via marker) | §10 |
| F10 | Auto-detect run id and PR number from the event | §4 |
| F11 | Never fail CI on API / permission errors (warn instead) | §11 |
| F12 | [IMPROVEMENT] GHES / custom API base URL (`GITHUB_API_URL`) | also enables offline testing |
| F13 | [IMPROVEMENT] Token-based runner detection (fixes `darwin`→windows, `machine`→macos false matches) | §5 |
| F14 | [IMPROVEMENT] Exact integer money math (micro-USD), no float drift | §6 |
| F15 | [IMPROVEMENT] In-progress jobs excluded and reported, not silently billed as 0 | §6 |
| F16 | [IMPROVEMENT] Paginate existing PR comments (original only read the first 100) | §10 |
| F17 | [IMPROVEMENT] Step outputs: `total_cost`, `total_minutes`, `job_count`, `over_budget`, `report_path` | §12 |
| F18 | [IMPROVEMENT] Machine-readable JSON report file | §12 |
| F19 | [IMPROVEMENT] `rates_json` override of the rate table | §7 |
| F20 | [IMPROVEMENT] `top_n`, `comment` toggle, opt-in `fail_on_budget` | §3 |
| F21 | [IMPROVEMENT] Invalid budget / network errors degrade to warnings (original crashed) | §11 |
| F22 | [IMPROVEMENT] Retry transient API failures (5xx / 429) | §11 |
| F23 | [IMPROVEMENT] Markdown-escape job names (`\|` in names broke the table) | §9 |
| F24 | [IMPROVEMENT] RFC 3339 timestamps with offsets and fractional seconds | §6 |

---

## 2. Runtime contract

Each implementation is a **command-line program** configured only through
environment variables (exactly how a composite action passes inputs). It takes
no command-line arguments.

| Implementation | Folder | Entry point | Constraints |
|---|---|---|---|
| bash | `implementations/bash/` | `ci-ledger.sh` | bash ≥ 4, `curl`, `jq` only |
| Go | `implementations/go/` | `go build -o ci-ledger .` → `./ci-ledger` | Go ≥ 1.22, **standard library only** |
| Node.js | `implementations/node/` | `node index.js` | Node ≥ 20, **no npm dependencies** (built-in `fetch`) |

Each folder MUST also contain:
- an `action.yml` making that implementation usable as a **composite action** on its own (`runs.using: composite`) with the inputs/outputs in §3 and §12;
- a `README.md` (short: how to run, how to test);
- unit tests runnable offline (`bats`-free plain bash script for bash; `go test ./...`; `node --test`).

### 2.1 Environment variables read

| Variable | Meaning | Default |
|---|---|---|
| `INPUT_GITHUB_TOKEN` | API token (falls back to `GITHUB_TOKEN`) | — |
| `INPUT_BUDGET_USD` | per-run budget, decimal string | empty = no budget |
| `INPUT_RUN_ID` | run id override | empty |
| `INPUT_PR_NUMBER` | PR number override | empty |
| `INPUT_TOP_N` | max job rows in table | `15` |
| `INPUT_COMMENT` | `true`/`false`: post PR comment | `true` |
| `INPUT_FAIL_ON_BUDGET` | `true`/`false`: exit 1 when over budget | `false` |
| `INPUT_RATES_JSON` | JSON rate overrides (§7.2) | empty |
| `GITHUB_REPOSITORY` | `owner/repo` | — (required) |
| `GITHUB_API_URL` | API base URL, no trailing slash | `https://api.github.com` |
| `GITHUB_SERVER_URL` | web base URL for links | `https://github.com` |
| `GITHUB_EVENT_PATH` | path to event JSON | — |
| `GITHUB_RUN_ID` | current run id | — |
| `GITHUB_STEP_SUMMARY` | file to append report to | unset = skip |
| `GITHUB_OUTPUT` | file to append outputs to | unset = skip |
| `RUNNER_TEMP` | dir for JSON report | system temp dir |

Boolean inputs: case-insensitive `true`, `1`, `yes` → true; anything else → false.
A trailing `/` on `GITHUB_API_URL` / `GITHUB_SERVER_URL` MUST be stripped.

---

## 3. Action inputs (`action.yml`)

| Input | Default | Description |
|---|---|---|
| `github_token` | `${{ github.token }}` | Needs `actions:read`; `pull-requests:write` to comment. |
| `budget_usd` | `""` | Flag the run when its cost exceeds this. |
| `run_id` | `""` | Run to analyze (default: triggering `workflow_run`, else current run). |
| `pr_number` | `""` | PR to comment on (auto-detected). |
| `top_n` | `"15"` | Max job rows in the table. |
| `comment` | `"true"` | Set `false` to only write the job summary. |
| `fail_on_budget` | `"false"` | Exit non-zero when over budget (opt-in). |
| `rates_json` | `""` | JSON rate overrides. |

Branding: icon `dollar-sign`, color `green`. Name/description MUST say "CI Ledger".

---

## 4. Resolution of run id and PR

Event = parsed JSON of `GITHUB_EVENT_PATH` if the file exists, else `{}`.
An unreadable/invalid event file is treated as `{}`.

**run id** = first non-empty of: `INPUT_RUN_ID`, `event.workflow_run.id`,
`GITHUB_RUN_ID`. If none → print `::error::could not resolve a run id to analyze`, exit 1.

**PR number** = first non-empty of: `INPUT_PR_NUMBER`,
`event.pull_request.number`, `event.workflow_run.pull_requests[0].number`.
None → no PR context.

Token = `INPUT_GITHUB_TOKEN` or `GITHUB_TOKEN`. If token or `GITHUB_REPOSITORY`
missing → print `::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required`, exit 1.
(Token and repo are checked before run id.)

---

## 5. Runner detection (F2, F13)

Input: the job's `labels` array (may be missing/null → empty).

1. Lower-case every label.
2. **self_hosted** = some label is exactly `self-hosted`.
3. Tokenize: split every label on any run of characters outside `[a-z0-9]`; collect all non-empty tokens.
4. **os**:
   - `windows` if any token is `windows`, `win`, `win32`, `win64`, or starts with `windows` (e.g. `windows2022`);
   - else `macos` if any token is `macos`, `mac`, `osx`, `darwin`, or starts with `macos` (e.g. `macos14`);
   - else `linux`.
5. **cores**: join the lower-cased labels with a single space; first match of regex `([0-9]+)[-_ ]?cores?` (the char after `core`/`cores` is irrelevant) → that integer; no match → `2`.

| labels | os | cores | self_hosted |
|---|---|---|---|
| `["ubuntu-latest"]` | linux | 2 | false |
| `["windows-latest"]` | windows | 2 | false |
| `["macos-14"]` | macos | 2 | false |
| `["self-hosted","linux","x64"]` | linux | 2 | true |
| `["self-hosted","darwin","arm64"]` | macos | 2 | true |
| `["ubuntu-latest-8-cores"]` | linux | 8 | false |
| `["windows-2022-16core"]` | windows | 16 | false |
| `["build-machine"]` | linux | 2 | false |
| `["twin-runner"]` | linux | 2 | false |
| `[]` / missing | linux | 2 | false |

---

## 6. Billable minutes and money (F3, F14, F15, F24)

**Counted jobs:** a job is counted when `status` is `completed`, or when
`status` is missing/null. Jobs with any other status (`queued`, `in_progress`,
`waiting`, `pending`, `requested`) are **excluded** from rows and totals and
increment `in_progress_jobs`.

**Duration:** parse `started_at` and `completed_at` as RFC 3339
(`Z` or `±hh:mm` offset, optional fractional seconds). `seconds` = exact
difference including fractions. Missing, null, unparsable, or negative →
`seconds = 0`.

**Billable minutes** = `ceil(seconds / 60)`; `0` when `seconds == 0`.
Examples: 0→0, 1→1, 60→1, 60.5→2, 61→2, 599→10.

**Money is integer micro-USD** (1 USD = 1 000 000 µ$). Never use floating
point to accumulate money.
- `rate_micro` = rate in USD × 1 000 000, rounded half-up to an integer.
- `cost_micro` = `minutes × rate_micro`.
- `total_micro` = Σ `cost_micro` of counted jobs. `total_minutes` = Σ minutes.

**Formatting money** (`fmt4`): `$` + dollars with exactly 4 decimals,
rounding `micro` half-up at the 4th decimal: `u = (micro + 50) / 100` (integer
division) → `$<u / 10000>.<u % 10000 zero-padded to 4>`.
Example: 562000 → `$0.5620`; 1234567 → `$1.2346`; 0 → `$0.0000`.

**Budget formatting** (`fmt2`): same idea at 2 decimals:
`u = (micro + 5000) / 10000` → `$<u/100>.<u%100 padded to 2>`. 500000 → `$0.50`.

Output values without `$` use the same digits (e.g. `total_cost=0.5620`).

---

## 7. Rate table (F4, F19)

### 7.1 Defaults (USD per billable minute)

```
standard:    linux 0.006   windows 0.010   macos 0.062
self_hosted: 0.0
larger:
  linux:   4→0.012  8→0.022  16→0.042  32→0.082  64→0.162
  windows: 2→0.010  4→0.022  8→0.042  16→0.082  32→0.162  64→0.322
  macos:   (none)
default cores: 2
```

**rate(os, cores, self_hosted):**
1. self_hosted → `self_hosted` rate.
2. if `cores > 2` and `larger[os]` has entries:
   exact tier `cores` → that rate; else the largest tier ≤ `cores` if any → its rate.
3. otherwise `standard[os]`.

Examples: linux/8 → 0.022; linux/12 → 0.022 (nearest smaller = 8);
linux/3 → 0.006 (no tier ≤ 3); macos/12 → 0.062; windows/2 → 0.010 (cores not > 2 → standard).

### 7.2 `INPUT_RATES_JSON` overrides

Optional JSON object, deep-merged over the defaults:

```json
{"standard": {"linux": 0.005}, "self_hosted": 0.002, "larger": {"linux": {"4": 0.011, "96": 0.25}}}
```

Only these keys are honoured; unknown keys are ignored. Larger-tier keys are
decimal core counts as strings. Invalid JSON → print
`::warning::CI Ledger ignored invalid rates_json` and use defaults.

---

## 8. Sorting, totals and budget (F5, F6)

- Rows sorted by `cost_micro` descending, then `minutes` descending, then
  original API order (stable).
- Budget: `INPUT_BUDGET_USD` trimmed; empty → no budget. Must match
  `^[0-9]+(\.[0-9]+)?$`; otherwise print
  `::warning::CI Ledger ignored invalid budget_usd '<raw value>'` and treat as no budget.
  `budget_micro` = value × 10⁶ rounded half-up (parse the decimal string exactly; do not rely on float).
- `over_budget` = budget set and `total_micro > budget_micro`.

---

## 9. Markdown report (F7, F23)

Exact format (`\n` line endings, no trailing newline at the end of the body):

```
<!-- ci-ledger:cost-report -->
### 💸 CI Ledger — GitHub Actions cost

**Run total: {fmt4(total)}** ({total_minutes} billable min, {job_count} jobs) · [run {run_id}]({server}/{repo}/actions/runs/{run_id})

**Budget {fmt2(budget)}/run: {🔴 OVER|🟢 ok}**

| Job | Runner | Min | Cost |
|---|---|--:|--:|
| {name} | {runner} | {minutes} | {fmt4(cost)} |
| … +{remaining} more | | | |

_{n} job(s) still in progress were not counted._

<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>
```

Rules:
- The `**Budget …**` line **and the blank line after it** appear only when a budget is set.
- At most `top_n` job rows (invalid / < 1 `INPUT_TOP_N` → 15). The `… +N more` row appears only when rows were truncated. `…` is U+2026.
- The `_… in progress …_` line **and the blank line after it** appear only when `in_progress_jobs > 0`.
- When there are zero counted jobs, the table header is still printed (no rows).
- `job_count` = number of counted jobs.
- `{name}`: job `name` (missing → `?`); replace every `|` with `\|`, and every `\r`/`\n` with a space.
- `{runner}`: `self-hosted ` prefix if self-hosted, then os, then ` {cores}c` if cores ≠ 2.
  e.g. `linux`, `self-hosted linux`, `linux 8c`, `self-hosted macos 4c`.

---

## 10. PR comment (F9, F16)

Only when there is PR context **and** `INPUT_COMMENT` is true.

1. List comments: `GET {api}/repos/{repo}/issues/{pr}/comments?per_page=100&page={n}`, n = 1,2,… until a page returns < 100 items.
2. First comment whose `body` contains `<!-- ci-ledger:cost-report -->` →
   `PATCH {api}/repos/{repo}/issues/comments/{id}` with `{"body": <report>}` → print `CI Ledger comment updated on PR #{pr}`.
3. Else `POST {api}/repos/{repo}/issues/{pr}/comments` with `{"body": <report>}` → print `CI Ledger comment created on PR #{pr}`.
4. Any failure → `::warning::CI Ledger could not post comment (HTTP {code})` or `(network error)`; continue, exit code unaffected.

No PR context → print `CI Ledger: no PR context; wrote job summary only`.
PR context but comment disabled → print `CI Ledger: comment disabled; wrote job summary only`.

---

## 11. HTTP behaviour and error handling (F1, F11, F12, F21, F22)

Every request sends:
```
Authorization: Bearer {token}
Accept: application/vnd.github+json
X-GitHub-Api-Version: 2022-11-28
User-Agent: ci-ledger
Content-Type: application/json      (only when a body is sent)
```

**Jobs:** `GET {api}/repos/{repo}/actions/runs/{run_id}/jobs?per_page=100&page={n}`,
n = 1,2,… collecting `.jobs`, until a page returns < 100 jobs.

**Retries:** a response with status 429 or ≥ 500, or a network error, is
retried up to **2** more times, sleeping 1 s then 2 s. Other 4xx are not retried.

**Failures:**
- jobs fetch fails (after retries) → `::warning::CI Ledger could not fetch jobs (HTTP {code}); skipping` (or `(network error)`), **exit 0**, write nothing else.
- Unexpected internal errors SHOULD also degrade to a `::warning::` and exit 0 — the action must never break the user's CI except via `fail_on_budget` or the config errors in §4.

---

## 12. Outputs (F8, F17, F18)

Order of side effects after a successful jobs fetch:
1. Append report body + `\n` to `GITHUB_STEP_SUMMARY` (if set).
2. Write JSON report to `{RUNNER_TEMP}/ci-ledger-report.json` (overwrite).
3. Append to `GITHUB_OUTPUT` (if set), exactly these lines in this order:
   ```
   total_cost=0.5620
   total_minutes=26
   job_count=6
   over_budget=true
   report_path=/tmp/runner/ci-ledger-report.json
   ```
   (`over_budget=false` when no budget.)
4. PR comment step (§10) and its stdout line.
5. `::notice::CI Ledger run total {fmt4(total)} ({total_minutes} min, {job_count} jobs)`
6. If over budget: `::warning::Run cost {fmt4(total)} exceeds budget {fmt2(budget)}`
7. Exit 1 if over budget and `fail_on_budget` true; else exit 0.

Warnings from §7.2/§8 are printed before any of the above.
Nothing else is printed to stdout (debug output, if any, goes to stderr).

**JSON report** (key order free, numbers as JSON numbers):

```json
{
  "repository": "owner/repo",
  "run_id": "123",
  "total_cost_usd": 0.562,
  "total_minutes": 26,
  "job_count": 6,
  "in_progress_jobs": 1,
  "budget_usd": 0.5,
  "over_budget": true,
  "jobs": [
    {"name": "integration (macos)", "os": "macos", "cores": 2, "self_hosted": false,
     "minutes": 7, "rate_usd": 0.062, "cost_usd": 0.434}
  ]
}
```
`jobs` in sorted order, raw (unescaped) names; `budget_usd` / `over_budget` are `null` when no budget;
USD numbers = micro / 10⁶.

---

## 13. Golden example (acceptance test)

Env: `GITHUB_REPOSITORY=owner/repo`, `GITHUB_SERVER_URL=https://github.com`,
`INPUT_BUDGET_USD=0.50`, event = `{"workflow_run":{"id":123,"pull_requests":[{"number":42}]}}`.

Jobs API (single page, in this order):

| name | labels | status | started_at | completed_at |
|---|---|---|---|---|
| build | ubuntu-latest | completed | 2026-06-14T01:00:00Z | 2026-06-14T01:07:31Z |
| integration (macos) | macos-latest | completed | 2026-06-14T01:00:00Z | 2026-06-14T01:06:01Z |
| lint | ubuntu-latest | completed | 2026-06-14T01:00:00Z | 2026-06-14T01:00:40Z |
| deploy | self-hosted, linux, x64 | completed | 2026-06-14T01:00:00Z | 2026-06-14T01:04:30Z |
| big \| test | ubuntu-latest-8-cores | completed | 2026-06-14T01:00:00Z | 2026-06-14T01:02:00Z |
| e2e | ubuntu-latest | in_progress | 2026-06-14T01:00:00Z | null |
| win | windows-latest | completed | 2026-06-14T03:00:00+02:00 | 2026-06-14T01:03:00Z |

(The `big | test` job's real name contains a literal `|`. The `win` job starts at 01:00:00Z expressed with an offset.)

Expected body:

```
<!-- ci-ledger:cost-report -->
### 💸 CI Ledger — GitHub Actions cost

**Run total: $0.5620** (26 billable min, 6 jobs) · [run 123](https://github.com/owner/repo/actions/runs/123)

**Budget $0.50/run: 🔴 OVER**

| Job | Runner | Min | Cost |
|---|---|--:|--:|
| integration (macos) | macos | 7 | $0.4340 |
| build | linux | 8 | $0.0480 |
| big \| test | linux 8c | 2 | $0.0440 |
| win | windows | 3 | $0.0300 |
| lint | linux | 1 | $0.0060 |
| deploy | self-hosted linux | 5 | $0.0000 |

_1 job(s) still in progress were not counted._

<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>
```

Expected stdout (no existing comment on PR 42):
```
CI Ledger comment created on PR #42
::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)
::warning::Run cost $0.5620 exceeds budget $0.50
```
Exit code 0. With `INPUT_FAIL_ON_BUDGET=true`, exit code 1. With `INPUT_TOP_N=4`, the
`lint` and `deploy` rows are replaced by `| … +2 more | | | |`.

---

## 14. Non-functional requirements

- **NF1 Performance:** must process a run with 1 000 jobs (10 pages) and a PR with 300 existing comments in well under 2 s against a local API. Implementations will be benchmarked on cold-start time, end-to-end time, peak memory and setup/build cost on a fresh runner.
- **NF2 Portability:** must work on `ubuntu-latest`, `macos-latest` and `windows-latest` (Git Bash shell for composite steps) using only tools preinstalled on GitHub-hosted runners.
- **NF3 Security:** never print the token; pass JSON bodies via stdin/file/memory, not via command-line arguments that could leak in process listings (bash: use `curl --data-binary @-` or a temp file, and pass the auth header via `-H @file` or `--config`).
- **NF4 No network** other than `GITHUB_API_URL`.
- **NF5 Testability:** pure functions (detection, minutes, rate, money formatting, rendering) separated from I/O and unit-tested, including every example in §5, §6, §7 and the golden example in §13.
