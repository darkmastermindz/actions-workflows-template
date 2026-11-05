#!/usr/bin/env bash
# Offline test-suite for the bash CI Ledger implementation.
# Requires: bash >= 4, jq, curl, python3 (only for the local mock API).
# Usage: tests/run-tests.sh            (all tests)
#        SKIP_SLOW=1 tests/run-tests.sh (skip the retry tests that sleep 3 s)

set -o pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
SCRIPT="$ROOT/ci-ledger.sh"

# shellcheck source=../ci-ledger.sh
source "$SCRIPT"

PASS=0 FAIL=0
WORK=$(mktemp -d "${TMPDIR:-/tmp}/ci-ledger-tests.XXXXXX")
MOCK_PID=''
cleanup() {
  [[ -n $MOCK_PID ]] && kill "$MOCK_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

ok() { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
ko() { FAIL=$((FAIL + 1)); printf '  FAIL %s\n' "$1"; }
assert_eq() { # name expected actual
  if [[ "$2" == "$3" ]]; then ok "$1"; else
    ko "$1"
    diff <(printf '%s\n' "$2") <(printf '%s\n' "$3") | sed 's/^/       /' | head -40
  fi
}
assert_contains() { # name haystack needle
  if [[ "$2" == *"$3"* ]]; then ok "$1"; else ko "$1"; printf '       missing: %s\n' "$3"; fi
}
assert_not_contains() {
  if [[ "$2" != *"$3"* ]]; then ok "$1"; else ko "$1"; printf '       unexpected: %s\n' "$3"; fi
}
jqlib() { jq "$@"; } # helper alias for readability
section() { printf '\n== %s\n' "$1"; }

# ===========================================================================
section "§5 runner detection"
# ===========================================================================
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  ( ["ubuntu-latest"], ["windows-latest"], ["macos-14"], ["self-hosted","linux","x64"],
    ["self-hosted","darwin","arm64"], ["ubuntu-latest-8-cores"], ["windows-2022-16core"],
    ["build-machine"], ["twin-runner"], [], null )
  | detect | "\(.os) \(.cores) \(.self_hosted)"')
expected='linux 2 false
windows 2 false
macos 2 false
linux 2 true
macos 2 true
linux 8 false
windows 16 false
linux 2 false
linux 2 false
linux 2 false
linux 2 false'
assert_eq "spec §5 table (11 rows incl. missing labels)" "$expected" "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  ( ["Windows2022"], ["win64"], ["osx-arm"], ["MacOS14"], ["mac"], ["x64 4-core"], ["16_cores"],
    ["a 12 cores"], ["Self-Hosted"], ["self-hosted-linux"] ) | detect | "\(.os) \(.cores) \(.self_hosted)"')
expected='windows 2 false
windows 2 false
macos 2 false
macos 2 false
macos 2 false
linux 4 false
linux 16 false
linux 12 false
linux 2 true
linux 2 false'
assert_eq "detection extra cases (case, prefixes, separators)" "$expected" "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  ({os:"linux",cores:2,self_hosted:false}, {os:"linux",cores:2,self_hosted:true},
   {os:"linux",cores:8,self_hosted:false}, {os:"macos",cores:4,self_hosted:true}) | runner_label')
assert_eq "runner label (§9)" $'linux\nself-hosted linux\nlinux 8c\nself-hosted macos 4c' "$actual"

# ===========================================================================
section "§6 billable minutes, timestamps and money"
# ===========================================================================
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  ( ["2026-06-14T01:00:00Z","2026-06-14T01:00:00Z"],
    ["2026-06-14T01:00:00Z","2026-06-14T01:00:01Z"],
    ["2026-06-14T01:00:00Z","2026-06-14T01:01:00Z"],
    ["2026-06-14T01:00:00Z","2026-06-14T01:01:00.5Z"],
    ["2026-06-14T01:00:00Z","2026-06-14T01:01:01Z"],
    ["2026-06-14T01:00:00Z","2026-06-14T01:09:59Z"] )
  | {started_at: .[0], completed_at: .[1]} | job_minutes | tostring' | paste -sd' ')
assert_eq "seconds 0,1,60,60.5,61,599 -> minutes 0,1,1,2,2,10" "0 1 1 2 2 10" "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  ( ["2026-06-14T03:00:00+02:00","2026-06-14T01:03:00Z"],
    ["2026-06-13T23:00:00-02:00","2026-06-14T01:00:30Z"],
    ["2026-06-14T01:00:00.250Z","2026-06-14T01:01:00.25Z"],
    ["2026-06-14T01:00:00.9Z","2026-06-14T01:01:00.1Z"],
    ["2026-06-14T01:00:00.1Z","2026-06-14T01:01:00.100000001Z"],
    ["2026-06-14T01:00:00.5Z","2026-06-14T01:00:00.5Z"],
    ["2026-06-14T01:00:00.4Z","2026-06-14T01:00:00.5Z"],
    ["2026-06-14T01:00:00.5Z","2026-06-14T01:00:01.4Z"],
    ["2026-06-14T01:05:00Z","2026-06-14T01:00:00Z"],
    [null,"2026-06-14T01:00:00Z"],
    ["2026-06-14T01:00:00Z",null],
    ["garbage","2026-06-14T01:00:00Z"],
    ["2026-13-14T01:00:00Z","2026-06-14T01:00:00Z"],
    ["2024-02-28T23:59:30Z","2024-03-01T00:00:30Z"],
    ["2025-12-31T23:59:00Z","2026-01-01T00:01:00Z"],
    ["2026-06-14t01:00:00z","2026-06-14T01:02:00Z"] )
  | {started_at: .[0], completed_at: .[1]} | job_minutes | tostring' | paste -sd' ')
assert_eq "offsets, fractions, invalid/negative -> 0, leap day, year end" \
  "3 1 1 1 2 0 1 1 0 0 0 0 0 1441 2 2" "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'(562000, 1234567, 0, 49, 50, 434000, 33000000) | fmt4' | paste -sd' ')
assert_eq "fmt4 examples" '$0.5620 $1.2346 $0.0000 $0.0000 $0.0001 $0.4340 $33.0000' "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'(500000, 4999, 5000, 1234567) | fmt2' | paste -sd' ')
assert_eq "fmt2 (jq) examples" '$0.50 $0.00 $0.01 $1.23' "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'(0.006, 0.010, 0.062, 0.0, 0.0000005, 0.0000004, 12.3456785, 1, 0.25) | to_micro' | paste -sd' ')
assert_eq "rate -> micro rounding half-up" "6000 10000 62000 0 1 0 12345679 1000000 250000" "$actual"

# ===========================================================================
section "§7 rate table"
# ===========================================================================
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  (default_rates | normalize_rates) as $r
  | ( ["linux",8,false], ["linux",12,false], ["linux",3,false], ["macos",12,false], ["windows",2,false],
      ["linux",2,false], ["windows",2,false], ["macos",2,false], ["linux",64,true], ["windows",4,false],
      ["windows",128,false], ["linux",4,false] )
  | rate_micro($r; .[0]; .[1]; .[2]) | tostring' | paste -sd' ')
assert_eq "linux/8 linux/12 linux/3 macos/12 windows/2 std... self-hosted" \
  "22000 22000 6000 62000 10000 6000 10000 62000 0 22000 322000 12000" "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  (merge_rates({"standard": {"linux": 0.005}, "self_hosted": 0.002, "larger": {"linux": {"4": 0.011, "96": 0.25}}, "bogus": 1}) | normalize_rates) as $r
  | ( ["linux",2,false], ["linux",4,false], ["linux",128,false], ["linux",8,false], ["windows",2,false], ["macos",2,true] )
  | rate_micro($r; .[0]; .[1]; .[2]) | tostring' | paste -sd' ')
assert_eq "rates_json deep merge (spec §7.2 example)" "5000 11000 250000 22000 10000 2000" "$actual"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'
  (merge_rates({"larger": {"macos": {"12": 0.5}}, "standard": {"macos": "x"}}) | normalize_rates) as $r
  | ( ["macos",12,false], ["macos",8,false] ) | rate_micro($r; .[0]; .[1]; .[2]) | tostring' | paste -sd' ')
assert_eq "rates_json adds macos tier, ignores non-numeric values" "500000 62000" "$actual"

# ===========================================================================
section "§2/§8/§9 bash helpers"
# ===========================================================================
r=''
for v in 0.50 1 0 1.2345675 1.2345674 0.004999 0.0049995 9.999999999 007.5 123456789012345678901234.5; do
  if parse_budget "$v"; then r+="$v=$BUDGET_MICRO:$BUDGET_FMT2 "; else r+="$v=invalid "; fi
done
assert_eq "budget parsing exact + fmt2" \
  '0.50=500000:$0.50 1=1000000:$1.00 0=0:$0.00 1.2345675=1234568:$1.23 1.2345674=1234567:$1.23 0.004999=4999:$0.00 0.0049995=5000:$0.01 9.999999999=10000000:$10.00 007.5=7500000:$7.50 123456789012345678901234.5=123456789012345678901234500000:$123456789012345678901234.50 ' "$r"
r=''
for v in abc -1 1. .5 '1,5' '1e3' '0x10'; do
  if parse_budget "$v"; then r+="ok "; else r+="invalid "; fi
done
assert_eq "budget invalid forms" 'invalid invalid invalid invalid invalid invalid invalid ' "$r"
r=''
for v in 4 '' 0 -3 abc ' 7 ' 015 99999999999999; do parse_top_n t "$v"; r+="$t "; done
assert_eq "top_n parsing" '4 15 15 15 15 7 15 999999999 ' "$r"
r=''
for v in true TRUE True 1 yes YES false 0 no '' maybe ' true '; do if is_true "$v"; then r+=T; else r+=F; fi; done
assert_eq "boolean parsing" 'TTTTTTFFFFFT' "$r"
actual=$(jq -nr "$CI_LEDGER_JQ_LIB"'"big | test\r\nx||y" | md_name')
assert_eq "markdown escaping of names" 'big \| test  x\|\|y' "$actual"

# ===========================================================================
section "end-to-end against a local mock API"
# ===========================================================================
if ! command -v python3 >/dev/null; then
  ko "python3 is required for the end-to-end tests"
  printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
  exit 1
fi
python3 "$HERE/mock_api.py" "$WORK/port" &
MOCK_PID=$!
for _ in $(seq 100); do [[ -s $WORK/port ]] && break; sleep 0.05; done
PORT=$(<"$WORK/port")
API="http://127.0.0.1:$PORT"
mock_load() { curl -sS --noproxy '*' -o /dev/null -X POST --data-binary "@$1" "$API/__load"; }
mock_log() { curl -sS --noproxy '*' "$API/__log"; }

TOKEN='ghs_SECRET_TOKEN_value_123'
JOBS_URL='/repos/owner/repo/actions/runs/123/jobs?per_page=100&page='
COMMENTS_URL='/repos/owner/repo/issues/42/comments'

# run_ledger [VAR=value ...]: runs the script in a clean env; sets OUT, RC.
run_ledger() {
  local case_dir="$WORK/case"
  rm -rf "$case_dir"; mkdir -p "$case_dir/runner"
  : >"$case_dir/summary.md"; : >"$case_dir/output.txt"
  printf '%s' '{"workflow_run":{"id":123,"pull_requests":[{"number":42}]}}' >"$case_dir/event.json"
  OUT=$(env -i PATH="$PATH" HOME="$HOME" TMPDIR="$case_dir" \
    INPUT_GITHUB_TOKEN="$TOKEN" GITHUB_REPOSITORY=owner/repo GITHUB_API_URL="$API" \
    GITHUB_SERVER_URL=https://github.com GITHUB_EVENT_PATH="$case_dir/event.json" \
    GITHUB_STEP_SUMMARY="$case_dir/summary.md" GITHUB_OUTPUT="$case_dir/output.txt" \
    RUNNER_TEMP="$case_dir/runner" INPUT_BUDGET_USD=0.50 "$@" bash "$SCRIPT" 2>"$case_dir/stderr")
  RC=$?
  SUMMARY=$(cat "$case_dir/summary.md"; printf x); SUMMARY=${SUMMARY%x}
  OUTPUTS=$(cat "$case_dir/output.txt"; printf x); OUTPUTS=${OUTPUTS%x}
  REPORT_FILE="$case_dir/runner/ci-ledger-report.json"
}

GOLDEN_JOBS='{"total_count":7,"jobs":[
 {"id":1,"name":"build","labels":["ubuntu-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:07:31Z"},
 {"id":2,"name":"integration (macos)","labels":["macos-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:06:01Z"},
 {"id":3,"name":"lint","labels":["ubuntu-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:00:40Z"},
 {"id":4,"name":"deploy","labels":["self-hosted","linux","x64"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:04:30Z"},
 {"id":5,"name":"big | test","labels":["ubuntu-latest-8-cores"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:02:00Z"},
 {"id":6,"name":"e2e","labels":["ubuntu-latest"],"status":"in_progress","started_at":"2026-06-14T01:00:00Z","completed_at":null},
 {"id":7,"name":"win","labels":["windows-latest"],"status":"completed","started_at":"2026-06-14T03:00:00+02:00","completed_at":"2026-06-14T01:03:00Z"}]}'

GOLDEN_BODY='<!-- ci-ledger:cost-report -->
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

<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>'

GOLDEN_STDOUT='CI Ledger comment created on PR #42
::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)
::warning::Run cost $0.5620 exceeds budget $0.50'

# fixture: golden jobs + comments; $1 = comments routes JSON object
golden_fixture() {
  local extra=${1:-}
  [[ -n $extra ]] || extra='{}'
  jq -n --argjson jobs "$GOLDEN_JOBS" --arg ju "GET ${JOBS_URL}1" --arg cu "$COMMENTS_URL" --argjson extra "$extra" '
    {routes: ({($ju): {status: 200, body: $jobs},
               ("GET " + $cu + "?per_page=100&page=1"): {status: 200, body: []},
               ("POST " + $cu): {status: 201, body: {id: 1}}} + $extra)}' >"$WORK/fx.json"
  mock_load "$WORK/fx.json"
}

# --- golden ----------------------------------------------------------------
golden_fixture
run_ledger
assert_eq "golden: exit code 0" 0 "$RC"
assert_eq "golden: stdout" "$GOLDEN_STDOUT" "$OUT"
assert_eq "golden: job summary = body + LF" "$GOLDEN_BODY"$'\n' "$SUMMARY"
assert_eq "golden: GITHUB_OUTPUT" "total_cost=0.5620
total_minutes=26
job_count=6
over_budget=true
report_path=$WORK/case/runner/ci-ledger-report.json
" "$OUTPUTS"
LOG=$(mock_log)
assert_eq "golden: request sequence" \
  "GET ${JOBS_URL}1|GET $COMMENTS_URL?per_page=100&page=1|POST $COMMENTS_URL" \
  "$(jq -r 'map("\(.method) \(.path)") | join("|")' <<<"$LOG")"
assert_eq "golden: POST body is {\"body\": report}" "$GOLDEN_BODY" \
  "$(jq -j 'map(select(.method == "POST"))[0].body | fromjson | .body' <<<"$LOG")"
assert_eq "golden: headers on every request" "true" "$(jq --arg t "Bearer $TOKEN" 'all(.[];
  .authorization == $t and .accept == "application/vnd.github+json" and .api_version == "2022-11-28"
  and .user_agent == "ci-ledger" and ((.method == "GET" and .content_type == null) or (.method != "GET" and .content_type == "application/json")))' <<<"$LOG")"
assert_eq "golden: JSON report" "$(jq -S . <<<'{
  "repository": "owner/repo", "run_id": "123", "total_cost_usd": 0.562, "total_minutes": 26,
  "job_count": 6, "in_progress_jobs": 1, "budget_usd": 0.5, "over_budget": true,
  "jobs": [
    {"name": "integration (macos)", "os": "macos", "cores": 2, "self_hosted": false, "minutes": 7, "rate_usd": 0.062, "cost_usd": 0.434},
    {"name": "build", "os": "linux", "cores": 2, "self_hosted": false, "minutes": 8, "rate_usd": 0.006, "cost_usd": 0.048},
    {"name": "big | test", "os": "linux", "cores": 8, "self_hosted": false, "minutes": 2, "rate_usd": 0.022, "cost_usd": 0.044},
    {"name": "win", "os": "windows", "cores": 2, "self_hosted": false, "minutes": 3, "rate_usd": 0.01, "cost_usd": 0.03},
    {"name": "lint", "os": "linux", "cores": 2, "self_hosted": false, "minutes": 1, "rate_usd": 0.006, "cost_usd": 0.006},
    {"name": "deploy", "os": "linux", "cores": 2, "self_hosted": true, "minutes": 5, "rate_usd": 0, "cost_usd": 0}
  ]}')" "$(jq -S . "$REPORT_FILE")"
assert_eq "golden: stderr empty" "" "$(<"$WORK/case/stderr")"

run_ledger INPUT_FAIL_ON_BUDGET=true
assert_eq "golden + fail_on_budget=true: exit 1" 1 "$RC"
assert_eq "golden + fail_on_budget=true: stdout unchanged" "$GOLDEN_STDOUT" "$OUT"

run_ledger INPUT_TOP_N=4
TOP4_BODY=${GOLDEN_BODY/$'| lint | linux | 1 | $0.0060 |\n| deploy | self-hosted linux | 5 | $0.0000 |'/'| … +2 more | | | |'}
assert_eq "golden + top_n=4: truncated table" "$TOP4_BODY"$'\n' "$SUMMARY"

run_ledger INPUT_BUDGET_USD=
NOBUDGET_BODY=${GOLDEN_BODY/$'**Budget $0.50/run: 🔴 OVER**\n\n'/}
assert_eq "no budget: body without budget line" "$NOBUDGET_BODY"$'\n' "$SUMMARY"
assert_eq "no budget: stdout" $'CI Ledger comment created on PR #42\n::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)' "$OUT"
assert_contains "no budget: over_budget=false" "$OUTPUTS" $'over_budget=false\n'
assert_eq "no budget: report nulls" "null null" "$(jq -r '"\(.budget_usd) \(.over_budget)"' "$REPORT_FILE")"

run_ledger INPUT_BUDGET_USD=' 1 '
assert_contains "budget ok: green line" "$SUMMARY" '**Budget $1.00/run: 🟢 ok**'
assert_eq "budget ok: exit 0 and no warning" "0|CI Ledger comment created on PR #42
::notice::CI Ledger run total \$0.5620 (26 min, 6 jobs)" "$RC|$OUT"

# --- warnings ----------------------------------------------------------------
run_ledger INPUT_BUDGET_USD=abc INPUT_RATES_JSON='{not json'
assert_eq "invalid rates_json + budget: warnings first, in order" "::warning::CI Ledger ignored invalid rates_json
::warning::CI Ledger ignored invalid budget_usd 'abc'
CI Ledger comment created on PR #42
::notice::CI Ledger run total \$0.5620 (26 min, 6 jobs)" "$OUT"
run_ledger INPUT_RATES_JSON='[1,2]'
assert_contains "rates_json non-object -> warning" "$OUT" '::warning::CI Ledger ignored invalid rates_json'
run_ledger INPUT_RATES_JSON='{"standard":{"linux":0.005},"self_hosted":0.002,"larger":{"linux":{"8":0.03}}}' INPUT_BUDGET_USD=
# build 8*5000=40000, lint 5000, big 2*30000=60000, deploy 5*2000=10000, mac 434000, win 30000 -> 579000
assert_contains "rates_json override applied" "$OUT" '::notice::CI Ledger run total $0.5790 (26 min, 6 jobs)'
assert_contains "rates_json override: self-hosted row" "$SUMMARY" '| deploy | self-hosted linux | 5 | $0.0100 |'

# --- PR context resolution ---------------------------------------------------
golden_fixture
run_ledger INPUT_COMMENT=false
assert_eq "comment disabled" "CI Ledger: comment disabled; wrote job summary only" "$(head -1 <<<"$OUT")"
assert_eq "comment disabled: no comment requests" "1" "$(mock_log | jq length)"
run_ledger INPUT_COMMENT=No
assert_eq "comment=No is false" "CI Ledger: comment disabled; wrote job summary only" "$(head -1 <<<"$OUT")"

printf '%s' '{"workflow_run":{"id":123}}' >"$WORK/ev-nopr.json"
run_ledger GITHUB_EVENT_PATH="$WORK/ev-nopr.json"
assert_eq "no PR context" "CI Ledger: no PR context; wrote job summary only" "$(head -1 <<<"$OUT")"

printf '%s' '{"pull_request":{"number":42}}' >"$WORK/ev-pr.json"
run_ledger GITHUB_EVENT_PATH="$WORK/ev-pr.json" GITHUB_RUN_ID=123
assert_eq "pull_request event + GITHUB_RUN_ID fallback" "$GOLDEN_STDOUT" "$OUT"

printf '%s' '{oops' >"$WORK/ev-bad.json"
run_ledger GITHUB_EVENT_PATH="$WORK/ev-bad.json" GITHUB_RUN_ID=123 INPUT_PR_NUMBER=42
assert_eq "invalid event file treated as {}" "$GOLDEN_STDOUT" "$OUT"

run_ledger GITHUB_EVENT_PATH="$WORK/missing.json" INPUT_RUN_ID=123 INPUT_PR_NUMBER=42 GITHUB_RUN_ID=999 GITHUB_API_URL="$API/"
assert_eq "INPUT_RUN_ID wins, missing event, API URL trailing slash stripped" "$GOLDEN_STDOUT" "$OUT"

run_ledger GITHUB_SERVER_URL=https://ghe.example.com/
assert_contains "server URL trailing slash stripped" "$SUMMARY" '[run 123](https://ghe.example.com/owner/repo/actions/runs/123)'

# --- config errors -----------------------------------------------------------
run_ledger INPUT_GITHUB_TOKEN=
assert_eq "missing token -> error, exit 1" "1|::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required" "$RC|$OUT"
run_ledger INPUT_GITHUB_TOKEN= GITHUB_TOKEN="$TOKEN"
assert_eq "GITHUB_TOKEN fallback" "0" "$RC"
run_ledger GITHUB_REPOSITORY=
assert_eq "missing repo -> error, exit 1" "1|::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required" "$RC|$OUT"
run_ledger GITHUB_EVENT_PATH="$WORK/missing.json"
assert_eq "no run id -> error, exit 1" "1|::error::could not resolve a run id to analyze" "$RC|$OUT"
run_ledger GITHUB_EVENT_PATH="$WORK/missing.json" GITHUB_REPOSITORY= INPUT_RATES_JSON=bad
assert_eq "token/repo checked before run id" "1|::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required" "$RC|$OUT"

# --- comment PATCH path on a later page -------------------------------------
# 300 comments; marker comments with ids 5250 (page 3) and 5299 (page 3);
# a body mentioning the marker inside a non-string is ignored.
jq -n --argjson jobs "$GOLDEN_JOBS" --arg ju "GET ${JOBS_URL}1" --arg cu "$COMMENTS_URL" --arg m "$CI_LEDGER_MARKER" '
  def page($p): [range(($p - 1) * 100; $p * 100) as $i
      | {id: (5000 + $i), body: (if $i == 250 then "old report\n" + $m + "\nstuff"
                                  elif $i == 299 then $m else "comment \($i)" end)}];
  {routes: {($ju): {status: 200, body: $jobs},
            ("GET " + $cu + "?per_page=100&page=1"): {status: 200, body: page(1)},
            ("GET " + $cu + "?per_page=100&page=2"): {status: 200, body: page(2)},
            ("GET " + $cu + "?per_page=100&page=3"): {status: 200, body: page(3)},
            ("GET " + $cu + "?per_page=100&page=4"): {status: 200, body: []},
            "PATCH /repos/owner/repo/issues/comments/5250": {status: 200, body: {id: 5250}}}}' >"$WORK/fx.json"
mock_load "$WORK/fx.json"
run_ledger
assert_eq "PATCH path: stdout" "${GOLDEN_STDOUT/created/updated}" "$OUT"
LOG=$(mock_log)
assert_eq "PATCH path: pages 1..3 then PATCH of first marker comment" \
  "GET ${JOBS_URL}1|GET $COMMENTS_URL?per_page=100&page=1|GET $COMMENTS_URL?per_page=100&page=2|GET $COMMENTS_URL?per_page=100&page=3|PATCH /repos/owner/repo/issues/comments/5250" \
  "$(jq -r 'map("\(.method) \(.path)") | join("|")' <<<"$LOG")"
assert_eq "PATCH path: body" "$GOLDEN_BODY" "$(jq -j 'map(select(.method == "PATCH"))[0].body | fromjson | .body' <<<"$LOG")"

# --- comment failures ----------------------------------------------------------
golden_fixture '{"POST /repos/owner/repo/issues/42/comments": {"status": 403, "body": {"message": "Forbidden"}}}'
run_ledger
assert_eq "comment POST 403 -> warning, exit 0" "0|::warning::CI Ledger could not post comment (HTTP 403)
::notice::CI Ledger run total \$0.5620 (26 min, 6 jobs)
::warning::Run cost \$0.5620 exceeds budget \$0.50" "$RC|$OUT"
golden_fixture '{"GET /repos/owner/repo/issues/42/comments?per_page=100&page=1": {"status": 404, "body": {}}}'
run_ledger
assert_contains "comment list 404 -> warning" "$OUT" '::warning::CI Ledger could not post comment (HTTP 404)'

# --- zero counted jobs ---------------------------------------------------------
jq -n --arg ju "GET ${JOBS_URL}1" '{routes: {($ju): {status: 200, body: {jobs: [
  {name: "a", labels: ["ubuntu-latest"], status: "queued"},
  {name: "b", labels: ["ubuntu-latest"], status: "waiting"}]}}}}' >"$WORK/fx.json"
mock_load "$WORK/fx.json"
run_ledger GITHUB_EVENT_PATH="$WORK/ev-nopr.json" INPUT_BUDGET_USD=
assert_eq "zero counted jobs: header only, in-progress note" '<!-- ci-ledger:cost-report -->
### 💸 CI Ledger — GitHub Actions cost

**Run total: $0.0000** (0 billable min, 0 jobs) · [run 123](https://github.com/owner/repo/actions/runs/123)

| Job | Runner | Min | Cost |
|---|---|--:|--:|

_2 job(s) still in progress were not counted._

<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>
' "$SUMMARY"
assert_eq "zero counted jobs: report" '0 0 0 2 []' "$(jq -c '"\(.total_cost_usd) \(.total_minutes) \(.job_count) \(.in_progress_jobs) \(.jobs)"' -r "$REPORT_FILE")"

# missing status counted, missing name -> "?", null labels, no timestamps
jq -n --arg ju "GET ${JOBS_URL}1" '{routes: {($ju): {status: 200, body: {jobs: [
  {labels: null, started_at: "2026-06-14T01:00:00Z", completed_at: "2026-06-14T01:00:10Z"},
  {name: "x\ny", status: "completed"}]}}}}' >"$WORK/fx.json"
mock_load "$WORK/fx.json"
run_ledger GITHUB_EVENT_PATH="$WORK/ev-nopr.json" INPUT_BUDGET_USD=
assert_contains "missing status counted, missing name -> ?" "$SUMMARY" $'| ? | linux | 1 | $0.0060 |\n| x y | linux | 0 | $0.0000 |\n'

# --- 1000 jobs (10 full pages + empty page 11) -------------------------------
mkdir -p "$WORK/big"
for p in $(seq 1 11); do
  jq -n --argjson p "$p" '{total_count: 1000, jobs: (if $p > 10 then [] else [range(($p - 1) * 100; $p * 100) as $i |
    {id: $i, run_id: 123, name: "job \($i)", labels: (if $i % 50 == 0 then ["windows-2022-16core"] else ["ubuntu-latest"] end),
     status: "completed", conclusion: "success",
     started_at: "2026-06-14T01:00:00Z",
     completed_at: ("2026-06-14T01:\(($i % 10) + 1 | tostring | if length < 2 then "0" + . else . end):00Z"),
     steps: [range(0; 8) as $s | {name: "step \($s)", status: "completed", conclusion: "success", number: $s,
                                  started_at: "2026-06-14T01:00:00Z", completed_at: "2026-06-14T01:00:05Z"}]}] end)}' >"$WORK/big/p$p.json"
done
jq -n --arg base "GET $JOBS_URL" --arg d "$WORK/big" --arg cu "$COMMENTS_URL" --arg m "$CI_LEDGER_MARKER" '
  def page($p): [range(($p - 1) * 100; $p * 100) as $i | {id: (9000 + $i), body: (if $i == 280 then $m else "c\($i)" end)}];
  {files: (reduce range(1; 12) as $p ({}; .[$base + ($p | tostring)] = "\($d)/p\($p).json")),
   routes: {("GET " + $cu + "?per_page=100&page=1"): {status: 200, body: page(1)},
            ("GET " + $cu + "?per_page=100&page=2"): {status: 200, body: page(2)},
            ("GET " + $cu + "?per_page=100&page=3"): {status: 200, body: page(3)},
            ("GET " + $cu + "?per_page=100&page=4"): {status: 200, body: []},
            "PATCH /repos/owner/repo/issues/comments/9280": {status: 200, body: {}}}}' >"$WORK/fx.json"
mock_load "$WORK/fx.json"
start=$(date +%s%N)
run_ledger INPUT_BUDGET_USD=
elapsed_ms=$((($(date +%s%N) - start) / 1000000))
# minutes: (i%10)+1 -> 100 * 55 = 5500 min. 20 windows 16c jobs (i%50==0, i%10==0 -> 1 min) at 0.082,
# 980 linux jobs at 0.006: (5500 - 20) * 6000 + 20 * 82000 = 32880000 + 1640000 = 34520000
assert_eq "1000 jobs: stdout" "CI Ledger comment updated on PR #42
::notice::CI Ledger run total \$34.5200 (5500 min, 1000 jobs)" "$OUT"
assert_eq "1000 jobs: 11 job pages + 3 comment pages + PATCH" "15" "$(mock_log | jq length)"
assert_contains "1000 jobs: truncation row" "$SUMMARY" '| … +985 more | | | |'
assert_eq "1000 jobs: 15 rows, first is windows 16c" "15|| job 0 | windows 16c | 1 | \$0.0820 |" \
  "$(grep -c '^| job ' <<<"$SUMMARY")|$(grep -m1 '^| job ' <<<"$SUMMARY")"
assert_eq "1000 jobs: report has 1000 jobs" "1000" "$(jq '.jobs | length' "$REPORT_FILE")"
if ((elapsed_ms < 2000)); then ok "1000 jobs + 300 comments end-to-end in ${elapsed_ms} ms (< 2000)"; else ko "too slow: ${elapsed_ms} ms"; fi

# --- NF3: token never on a command line ----------------------------------------
mkdir -p "$WORK/shim"
REAL_CURL=$(command -v curl) REAL_JQ=$(command -v jq)
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "curl $*" >>"%s/argv.log"\nexec "%s" "$@"\n' "$WORK" "$REAL_CURL" >"$WORK/shim/curl"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "jq $*" >>"%s/argv.log"\nexec "%s" "$@"\n' "$WORK" "$REAL_JQ" >"$WORK/shim/jq"
chmod +x "$WORK/shim/curl" "$WORK/shim/jq"
golden_fixture
: >"$WORK/argv.log"
PATH="$WORK/shim:$PATH" run_ledger
assert_eq "NF3 shim run succeeded" "$GOLDEN_STDOUT" "$OUT"
assert_contains "NF3 shim saw curl calls" "$(<"$WORK/argv.log")" "curl -sS --config"
assert_not_contains "NF3 token absent from every argv" "$(<"$WORK/argv.log")" "$TOKEN"
assert_not_contains "NF3 report body not passed as argv" "$(grep '^curl' "$WORK/argv.log")" "integration (macos)"
assert_not_contains "NF3 token not printed" "$OUT$(<"$WORK/case/stderr")" "$TOKEN"

# --- HTTP failures and retries ---------------------------------------------------
golden_fixture "{\"GET ${JOBS_URL}1\": {\"status\": 404, \"body\": {}}}"
run_ledger
assert_eq "jobs 404: warning, exit 0, no retry" "0|::warning::CI Ledger could not fetch jobs (HTTP 404); skipping|1" "$RC|$OUT|$(mock_log | jq length)"
assert_eq "jobs 404: nothing written" "||no-report" "$SUMMARY|$OUTPUTS|$([[ -e $REPORT_FILE ]] && echo report || echo no-report)"

jq -n --arg ju "GET ${JOBS_URL}1" '{routes: {($ju): {raw: "not json", status: 200}}}' >"$WORK/fx.json"
mock_load "$WORK/fx.json"
run_ledger
assert_eq "jobs invalid JSON: warning, exit 0" "0|::warning::CI Ledger could not parse the jobs response; skipping" "$RC|$OUT"

if [[ -z ${SKIP_SLOW:-} ]]; then
  golden_fixture "{\"GET ${JOBS_URL}1\": {\"status\": 502, \"body\": {}}}"
  run_ledger
  assert_eq "jobs 502: 3 attempts then warning, exit 0" "0|::warning::CI Ledger could not fetch jobs (HTTP 502); skipping|3" "$RC|$OUT|$(mock_log | jq length)"

  golden_fixture "{\"GET ${JOBS_URL}1\": [{\"status\": 429, \"body\": {}}, {\"status\": 200, \"body\": $GOLDEN_JOBS}]}"
  run_ledger
  assert_eq "jobs 429 then 200: retried and succeeded" "$GOLDEN_STDOUT|4" "$OUT|$(mock_log | jq length)"

  run_ledger GITHUB_API_URL="http://127.0.0.1:9"
  assert_eq "network error: warning, exit 0" "0|::warning::CI Ledger could not fetch jobs (network error); skipping" "$RC|$OUT"
else
  printf '  (skipped slow retry tests)\n'
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
((FAIL == 0))
