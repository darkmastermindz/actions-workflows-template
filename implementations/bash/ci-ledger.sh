#!/usr/bin/env bash
# CI Ledger -- attribute a USD cost to every job of a GitHub Actions run.
# bash >= 4, curl, jq only. Configured exclusively through environment
# variables (see REQUIREMENTS.md section 2.1). Takes no arguments.
#
# The script can be sourced (for unit tests): it then only defines the pure
# helpers and the jq library and does not run main.

CI_LEDGER_MARKER='<!-- ci-ledger:cost-report -->'

# ---------------------------------------------------------------------------
# jq library: pure functions (detection, timestamps, minutes, rates, money,
# rendering). Kept ASCII-only (non-ASCII glyphs are \u escapes) so the program
# text survives any command-line encoding, e.g. Git Bash on Windows.
# ---------------------------------------------------------------------------
IFS= read -r -d '' CI_LEDGER_JQ_LIB <<'JQ_LIB'
def zpad($n): tostring | if length >= $n then . else ("000000000000000000000000"[0:($n - length)]) + . end;
def zeros($n): [range(0; $n) | "0"] | join("");

# money: integer micro-USD -> "0.5620" (4 decimals, half-up)
def money4: ((. + 50) / 100 | floor) as $u | "\(($u / 10000) | floor).\($u % 10000 | zpad(4))";
def fmt4: "$" + money4;
def money2: ((. + 5000) / 10000 | floor) as $u | "\(($u / 100) | floor).\($u % 100 | zpad(2))";
def fmt2: "$" + money2;

# USD number -> integer micro-USD, rounded half-up. Plain decimals are parsed
# from their textual form so no float drift is involved.
def to_micro:
  if type != "number" then null
  else (tostring | capture("^(?<i>[0-9]+)(\\.(?<f>[0-9]+))?([eE](?<e>[+-]?[0-9]+))?$") // null) as $c
    | if $c == null then (. * 1000000 + 0.5 | floor)   # negative: plain float rounding
      else ($c.i + ($c.f // "")) as $digits
        | (($c.i | length) + (($c.e // "0") | tonumber) + 6) as $pos   # decimal point of value * 10^6
        | (if $pos <= 0 then "0"
           elif $pos >= ($digits | length) then $digits + zeros($pos - ($digits | length))
           else $digits[0:$pos] end) as $int
        | (if $pos >= 0 and $pos < ($digits | length) then ($digits[$pos:$pos + 1] | tonumber) else 0 end) as $round
        | ($int | tonumber) + (if $round >= 5 then 1 else 0 end)
      end
  end;

# runner detection from a labels value (array, or missing/null)
def detect:
  (if type == "array" then . else [] end | map(tostring | ascii_downcase)) as $l
  | [$l[] | splits("[^a-z0-9]+") | select(length > 0)] as $t
  | { os: (if any($t[]; . == "windows" or . == "win" or . == "win32" or . == "win64" or startswith("windows")) then "windows"
           elif any($t[]; . == "macos" or . == "mac" or . == "osx" or . == "darwin" or startswith("macos")) then "macos"
           else "linux" end),
      cores: ([$l | join(" ") | match("([0-9]+)[-_ ]?cores?") | .captures[0].string | tonumber] | .[0] // 2),
      self_hosted: any($l[]; . == "self-hosted") };

def runner_label:
  (if .self_hosted then "self-hosted " else "" end) + .os + (if .cores != 2 then " \(.cores)c" else "" end);

# RFC 3339 -> {s: integer epoch seconds, f: fraction digits} or null
def days_from_civil($y; $m; $d):
  (if $m <= 2 then $y - 1 else $y end) as $yy
  | (($yy / 400) | floor) as $era
  | ($yy - $era * 400) as $yoe
  | (($m + 9) % 12) as $mp
  | ((((153 * $mp + 2) / 5) | floor) + $d - 1) as $doy
  | ($yoe * 365 + (($yoe / 4) | floor) - (($yoe / 100) | floor) + $doy) as $doe
  | $era * 146097 + $doe - 719468;

def parse_ts:
  if type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt ][0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?([Zz]|[+-][0-9]{2}:[0-9]{2})$") then
    (if (.[-1:] == "Z" or .[-1:] == "z") then 1 else 6 end) as $zl
    | .[length - $zl:] as $z
    | (.[0:4] | tonumber) as $y | (.[5:7] | tonumber) as $mo | (.[8:10] | tonumber) as $d
    | (.[11:13] | tonumber) as $h | (.[14:16] | tonumber) as $mi | (.[17:19] | tonumber) as $s
    | (if $zl == 1 then 0 else (($z[1:3] | tonumber) * 3600 + ($z[4:6] | tonumber) * 60) * (if $z[0:1] == "-" then -1 else 1 end) end) as $off
    | if $mo < 1 or $mo > 12 or $d < 1 or $d > 31 or $h > 23 or $mi > 59 or $s > 60 or ($zl == 6 and (($z[1:3] | tonumber) > 23 or ($z[4:6] | tonumber) > 59)) then null
      else { s: (days_from_civil($y; $mo; $d) * 86400 + $h * 3600 + $mi * 60 + $s - $off), f: .[20:length - $zl] } end
  else null end;

# billable minutes between two parsed timestamps: ceil(seconds / 60),
# 0 when either is null or the difference is <= 0. Exact: whole seconds are
# integers and the fractional parts are compared as equal-length digit strings.
def billable_minutes($a; $b):
  if $a == null or $b == null then 0
  else ($b.s - $a.s) as $w
    | ([($a.f | length), ($b.f | length)] | max) as $n
    | ($a.f + zeros($n - ($a.f | length))) as $fa
    | ($b.f + zeros($n - ($b.f | length))) as $fb
    | if $fb == $fa then (if $w <= 0 then 0 else (($w + 59) / 60 | floor) end)
      elif $fb > $fa then (if $w < 0 then 0 else (($w / 60) | floor) + 1 end)
      else (if $w < 1 then 0 else ((($w - 1) / 60) | floor) + 1 end)
      end
  end;
def job_minutes: billable_minutes((.started_at | parse_ts); (.completed_at | parse_ts));

# rate table: defaults deep-merged with an override object, normalised to
# {standard:{os:micro}, self_hosted:micro, larger:{os:[[cores,micro],...]}}
def default_rates:
  { standard: {linux: 0.006, windows: 0.010, macos: 0.062},
    self_hosted: 0.0,
    larger: { linux:   {"4": 0.012, "8": 0.022, "16": 0.042, "32": 0.082, "64": 0.162},
              windows: {"2": 0.010, "4": 0.022, "8": 0.042, "16": 0.082, "32": 0.162, "64": 0.322},
              macos:   {} } };
def merge_rates($o):
  default_rates
  | if ($o.standard | type) == "object" then
      reduce ("linux", "windows", "macos") as $k (.;
        if ($o.standard[$k] | type) == "number" then .standard[$k] = $o.standard[$k] else . end)
    else . end
  | if ($o.self_hosted | type) == "number" then .self_hosted = $o.self_hosted else . end
  | if ($o.larger | type) == "object" then
      reduce ("linux", "windows", "macos") as $k (.;
        if ($o.larger[$k] | type) == "object" then
          reduce ($o.larger[$k] | to_entries[] | select((.key | test("^[0-9]+$")) and (.value | type) == "number")) as $e (.;
            .larger[$k][$e.key] = $e.value)
        else . end)
    else . end;
def normalize_rates:
  { standard: (.standard | map_values(to_micro)),
    self_hosted: (.self_hosted | to_micro),
    larger: (.larger | map_values(to_entries | map([(.key | tonumber), (.value | to_micro)]) | sort_by(.[0]))) };

def rate_micro($r; $os; $cores; $sh):
  if $sh then $r.self_hosted
  else ($r.larger[$os] // []) as $tiers
    | if $cores > 2 and ($tiers | length) > 0 then
        ([$tiers[] | select(.[0] <= $cores)] | last) as $t
        | if $t == null then $r.standard[$os] else $t[1] end
      else $r.standard[$os] end
  end;

def md_name: split("|") | join("\\|") | split("\r") | join(" ") | split("\n") | join(" ") | split("\u0000") | join("");

# per-page computation: jobs array -> {rows (unsorted), ip}. $offset keeps the
# original API order across pages for the stable sort.
def job_rows($rates; $offset):
  [.[] | select(type == "object")] as $jobs
  # detection memoised per distinct labels value (runs reuse few label sets)
  | (reduce ($jobs[] | .labels | tojson) as $k ({}; if has($k) then . else .[$k] = ($k | fromjson | detect) end)) as $det
  | { rows: [ $jobs | to_entries[] | .key as $i | .value
              | select(.status == null or .status == "completed")
              | $det[.labels | tojson] as $d
              | job_minutes as $m
              | rate_micro($rates; $d.os; $d.cores; $d.self_hosted) as $r
              | { i: ($offset + $i), name: (if .name == null then "?" else (.name | tostring) end),
                  os: $d.os, cores: $d.cores, self_hosted: $d.self_hosted,
                  minutes: $m, rate: $r, cost: ($m * $r) } ],
      ip: ([$jobs[] | select(.status != null and .status != "completed")] | length) };

# totals, budget and renderings. $ctx: {rates, run_id, repo, server,
# budget_micro (digit string, "" = none), budget_fmt2, top_n}
def render($ctx; $unsorted; $ip):
  ($unsorted | sort_by([-.cost, -.minutes, .i])) as $rows
  | (($rows | map(.cost) | add) // 0) as $total
  | (($rows | map(.minutes) | add) // 0) as $tmin
  | ($rows | length) as $n
  | ($ctx.budget_micro != "") as $hasb
  | (if $hasb then ($ctx.budget_micro | tonumber) else null end) as $bm
  | ($hasb and $total > $bm) as $over
  | $ctx.top_n as $top
  | { total: $total, minutes: $tmin, job_count: $n, in_progress: $ip, over: $over,
      markdown: ((
          [ "<!-- ci-ledger:cost-report -->",
            "### \ud83d\udcb8 CI Ledger \u2014 GitHub Actions cost",
            "",
            "**Run total: \($total | fmt4)** (\($tmin) billable min, \($n) jobs) \u00b7 [run \($ctx.run_id)](\($ctx.server)/\($ctx.repo)/actions/runs/\($ctx.run_id))",
            "" ]
          + (if $hasb then ["**Budget \($ctx.budget_fmt2)/run: \(if $over then "\ud83d\udd34 OVER" else "\ud83d\udfe2 ok" end)**", ""] else [] end)
          + [ "| Job | Runner | Min | Cost |", "|---|---|--:|--:|" ]
          + [ $rows[0:$top][] | "| \(.name | md_name) | \(runner_label) | \(.minutes) | \(.cost | fmt4) |" ]
          + (if $n > $top then ["| \u2026 +\($n - $top) more | | | |"] else [] end)
          + [ "" ]
          + (if $ip > 0 then ["_\($ip) job(s) still in progress were not counted._", ""] else [] end)
          + [ "<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>" ]
        ) | join("\n")),
      report: { repository: $ctx.repo, run_id: $ctx.run_id,
                total_cost_usd: ($total / 1000000), total_minutes: $tmin, job_count: $n,
                in_progress_jobs: $ip,
                budget_usd: (if $hasb then $bm / 1000000 else null end),
                over_budget: (if $hasb then $over else null end),
                jobs: [ $rows[] | { name, os, cores, self_hosted, minutes,
                                    rate_usd: (.rate / 1000000), cost_usd: (.cost / 1000000) } ] } };
def ledger($ctx): job_rows($ctx.rates; 0) as $p | render($ctx; $p.rows; $p.ip);
JQ_LIB

# Prelude program: parse the event and rates_json once.
# Prints 4 lines: rates status (ok|invalid), normalised rates JSON,
# event workflow_run.id, event PR number.
IFS= read -r -d '' CI_LEDGER_JQ_PRELUDE <<'JQ_PRELUDE'
def nz: if type == "number" then tostring elif type == "string" then gsub("[\r\n]"; "") else "" end;
(try ($ev | fromjson) catch {}) as $e
| (if ($e | type) == "object" then $e else {} end) as $e
| (if ($rates | test("^\\s*$")) then {ok: true, o: {}}
   else (try ($rates | fromjson) catch null) as $o
     | if ($o | type) == "object" then {ok: true, o: $o} else {ok: false, o: {}} end
   end) as $rj
| (if $rj.ok then "ok" else "invalid" end),
  (merge_rates($rj.o) | normalize_rates | tojson),
  ((try $e.workflow_run.id catch null) | nz),
  ([ ((try $e.pull_request.number catch null) | nz),
     ((try $e.workflow_run.pull_requests[0].number catch null) | nz) ] | map(select(. != "")) | .[0] // "")
JQ_PRELUDE

# Main program: reads every fetched jobs page as an input and emits
# NUL-terminated parts: scalars, markdown, report JSON, comment body JSON.
# Page program: one jobs page -> {rows, ip} for that page.
IFS= read -r -d '' CI_LEDGER_JQ_PAGE <<'JQ_PAGE'
(.jobs? // []) | (if type == "array" then . else [] end) | job_rows($rates; $offset)
JQ_PAGE

# Main program: reads the per-page results and emits NUL-terminated parts:
# scalars, markdown, report JSON, comment body JSON.
IFS= read -r -d '' CI_LEDGER_JQ_MAIN <<'JQ_MAIN'
[inputs | objects] as $p
| render($ctx; [$p[].rows[]]; (($p | map(.ip) | add) // 0))
| "\(.total | money4) \(.minutes) \(.job_count) \(.over)\u0000",
  .markdown + "\u0000",
  (.report | tojson) + "\u0000",
  ({body: .markdown} | tojson) + "\u0000"
JQ_MAIN

CI_LEDGER_JQ=(jq)
case "${OSTYPE:-}" in msys* | cygwin* | win*) CI_LEDGER_JQ=(jq -b) ;; esac

# ---------------------------------------------------------------------------
# Pure bash helpers
# ---------------------------------------------------------------------------
trim() { # trim VAR_NAME VALUE (internal names are prefixed to avoid shadowing)
  local __trim_v=$2
  __trim_v=${__trim_v#"${__trim_v%%[![:space:]]*}"}
  __trim_v=${__trim_v%"${__trim_v##*[![:space:]]}"}
  printf -v "$1" '%s' "$__trim_v"
}

is_true() {
  local v
  trim v "${1:-}"
  # bash 3.2 (macOS /bin/bash) has no ${v,,}
  case $v in
    [Tt][Rr][Uu][Ee] | 1 | [Yy][Ee][Ss]) return 0 ;;
  esac
  return 1
}

# str_inc VAR_NAME DIGITS: VAR = DIGITS + 1 (arbitrary length)
str_inc() {
  local s=$2 __inc_out='' carry=1 i d
  for ((i = ${#s} - 1; i >= 0; i--)); do
    d=$((${s:i:1} + carry))
    if ((d == 10)); then d=0; carry=1; else carry=0; fi
    __inc_out="$d$__inc_out"
  done
  ((carry)) && __inc_out="1$__inc_out"
  printf -v "$1" '%s' "$__inc_out"
}

# parse_budget VALUE: exact decimal -> BUDGET_MICRO (digit string) and
# BUDGET_FMT2 ("$0.50"). Returns 1 when VALUE is not ^[0-9]+(\.[0-9]+)?$.
parse_budget() {
  local v=$1 i f fr q inc
  [[ $v =~ ^([0-9]+)(\.([0-9]+))?$ ]] || return 1
  i=${BASH_REMATCH[1]} f=${BASH_REMATCH[3]}
  while [[ $i == 0?* ]]; do i=${i#0}; done
  f="${f}0000000"
  fr=$((10#${f:0:6} + (${f:6:1} >= 5 ? 1 : 0))) # 0..1000000
  q=$(((fr + 5000) / 10000))                   # 0..100
  if ((fr == 1000000)); then
    str_inc inc "$i"
    BUDGET_MICRO="${inc}000000"
  else
    printf -v BUDGET_MICRO '%s%06d' "$i" "$fr"
  fi
  while [[ $BUDGET_MICRO == 0?* ]]; do BUDGET_MICRO=${BUDGET_MICRO#0}; done
  if ((q == 100)); then
    str_inc inc "$i"
    BUDGET_FMT2="\$${inc}.00"
  else
    printf -v BUDGET_FMT2 '$%s.%02d' "$i" "$q"
  fi
}

# parse_top_n VAR_NAME VALUE: positive integer, else 15
parse_top_n() {
  local __top_v
  trim __top_v "$2"
  if [[ $__top_v =~ ^[0-9]+$ ]]; then
    while [[ $__top_v == 0?* ]]; do __top_v=${__top_v#0}; done
    if ((${#__top_v} > 9)); then __top_v=999999999; fi
    ((__top_v >= 1)) || __top_v=15
  else
    __top_v=15
  fi
  printf -v "$1" '%s' "$__top_v"
}

# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------
# http_request METHOD URL OUTFILE [BODYFILE] -> sets HTTP_CODE ("000" on
# network error); returns 0 on 2xx. Retries 429/5xx/network twice (1 s, 2 s).
# The token only lives in the curl config file ($CI_LEDGER_CURLRC).
http_request() {
  local method=$1 url=$2 out=$3 body=${4:-} attempt code rc
  local -a extra=()
  [[ $method != GET ]] && extra+=(-X "$method")
  [[ -n $body ]] && extra+=(-H 'Content-Type: application/json' --data-binary "@$body")
  for attempt in 0 1 2; do
    ((attempt > 0)) && sleep "$attempt"
    code=$(curl -sS --config "$CI_LEDGER_CURLRC" -o "$out" -w '%{http_code}' "${extra[@]}" -- "$url")
    rc=$?
    ((rc != 0)) && code=000
    HTTP_CODE=$code
    if [[ $code == 2[0-9][0-9] ]]; then return 0; fi
    if [[ $code != 000 && $code != 429 ]] && ((10#$code < 500)); then return 1; fi
  done
  return 1
}

http_reason() { # -> "(HTTP 404)" or "(network error)"
  if [[ $HTTP_CODE == 000 ]]; then printf '(network error)'; else printf '(HTTP %s)' "$HTTP_CODE"; fi
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
ci_ledger_main() {
  local token=${INPUT_GITHUB_TOKEN:-${GITHUB_TOKEN:-}}
  local repo=${GITHUB_REPOSITORY:-}
  if [[ -z $token || -z $repo ]]; then
    echo '::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required'
    return 1
  fi
  if ! command -v jq >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1; then
    echo '::warning::CI Ledger requires curl and jq; skipping'
    return 0
  fi

  local api=${GITHUB_API_URL:-https://api.github.com}
  local server=${GITHUB_SERVER_URL:-https://github.com}
  while [[ $api == */ ]]; do api=${api%/}; done
  while [[ $server == */ ]]; do server=${server%/}; done

  # event + rates_json, in one jq call
  local event_file=${GITHUB_EVENT_PATH:-}
  [[ -n $event_file && -f $event_file && -r $event_file ]] || event_file=/dev/null
  local -a pre=()
  local __line # bash 3.2 (macOS /bin/bash) has no mapfile
  while IFS= read -r __line || [[ -n $__line ]]; do pre+=("$__line"); done < <(
    "${CI_LEDGER_JQ[@]}" -nr --rawfile ev "$event_file" --arg rates "${INPUT_RATES_JSON:-}" \
      "$CI_LEDGER_JQ_LIB $CI_LEDGER_JQ_PRELUDE" 2>/dev/null)
  if ((${#pre[@]} < 4)); then # e.g. unreadable event file: retry with an empty event
    pre=()
    while IFS= read -r __line || [[ -n $__line ]]; do pre+=("$__line"); done < <(
      "${CI_LEDGER_JQ[@]}" -nr --rawfile ev /dev/null --arg rates "${INPUT_RATES_JSON:-}" \
        "$CI_LEDGER_JQ_LIB $CI_LEDGER_JQ_PRELUDE")
  fi
  if ((${#pre[@]} < 4)); then
    echo '::warning::CI Ledger internal error (jq); skipping'
    return 0
  fi
  local rates_status=${pre[0]} rates=${pre[1]} ev_run=${pre[2]} ev_pr=${pre[3]}

  local run_id pr
  trim run_id "${INPUT_RUN_ID:-}"
  [[ -n $run_id ]] || run_id=$ev_run
  [[ -n $run_id ]] || run_id=${GITHUB_RUN_ID:-}
  if [[ -z $run_id ]]; then
    echo '::error::could not resolve a run id to analyze'
    return 1
  fi
  trim pr "${INPUT_PR_NUMBER:-}"
  [[ -n $pr ]] || pr=$ev_pr

  [[ $rates_status == ok ]] || echo '::warning::CI Ledger ignored invalid rates_json'

  local budget_raw budget_trim
  budget_raw=${INPUT_BUDGET_USD:-}
  trim budget_trim "$budget_raw"
  BUDGET_MICRO='' BUDGET_FMT2=''
  if [[ -n $budget_trim ]] && ! parse_budget "$budget_trim"; then
    echo "::warning::CI Ledger ignored invalid budget_usd '$budget_raw'"
    BUDGET_MICRO='' BUDGET_FMT2=''
  fi

  local top_n
  parse_top_n top_n "${INPUT_TOP_N:-15}"

  # private scratch dir + curl config holding the token (never on a command line)
  local tmp
  tmp=$(umask 077 && mktemp -d "${TMPDIR:-/tmp}/ci-ledger.XXXXXX") || {
    echo '::warning::CI Ledger could not create a temp dir; skipping'
    return 0
  }
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  CI_LEDGER_CURLRC="$tmp/curlrc"
  local tok=${token//[$'\r\n']/}
  tok=${tok//\\/\\\\}
  tok=${tok//\"/\\\"}
  (
    umask 077
    printf 'header = "Authorization: Bearer %s"\nheader = "Accept: application/vnd.github+json"\nheader = "X-GitHub-Api-Version: 2022-11-28"\nuser-agent = "ci-ledger"\nconnect-timeout = 15\nmax-time = 120\n' "$tok" >"$CI_LEDGER_CURLRC"
  )
  unset tok

  # F1: fetch all job pages
  local page=1 n rf offset=0 pid
  local -a pages=() pids=()
  while :; do
    local pf
    printf -v pf '%s/jobs-%05d.json' "$tmp" "$page"
    if ! http_request GET "$api/repos/$repo/actions/runs/$run_id/jobs?per_page=100&page=$page" "$pf"; then
      echo "::warning::CI Ledger could not fetch jobs $(http_reason); skipping"
      return 0
    fi
    n=$("${CI_LEDGER_JQ[@]}" '.jobs? // [] | if type == "array" then length else 0 end' "$pf" 2>/dev/null)
    if [[ ! $n =~ ^[0-9]+$ ]]; then
      echo '::warning::CI Ledger could not parse the jobs response; skipping'
      return 0
    fi
    # per-job computation for this page runs in the background while the
    # next page is fetched
    rf="$pf.rows"
    "${CI_LEDGER_JQ[@]}" -c --argjson rates "$rates" --argjson offset "$offset" \
      "$CI_LEDGER_JQ_LIB $CI_LEDGER_JQ_PAGE" "$pf" >"$rf" 2>/dev/null &
    pids+=($!)
    pages+=("$rf")
    offset=$((offset + n))
    ((n < 100)) && break
    page=$((page + 1))
  done

  for pid in "${pids[@]}"; do
    if ! wait "$pid"; then
      echo '::warning::CI Ledger internal error while computing the report; skipping'
      return 0
    fi
  done

  # one jq pass over the per-page rows for totals and all renderings
  local ctx out="$tmp/result.bin"
  printf -v ctx '{"top_n":%s}' "$top_n"
  if ! "${CI_LEDGER_JQ[@]}" -nj --argjson ctx "$ctx" --arg run_id "$run_id" --arg repo "$repo" \
    --arg server "$server" --arg bm "$BUDGET_MICRO" --arg b2 "$BUDGET_FMT2" \
    "$CI_LEDGER_JQ_LIB (\$ctx + {run_id: \$run_id, repo: \$repo, server: \$server, budget_micro: \$bm, budget_fmt2: \$b2}) as \$ctx | $CI_LEDGER_JQ_MAIN" \
    "${pages[@]}" >"$out"; then
    echo '::warning::CI Ledger internal error while computing the report; skipping'
    return 0
  fi
  local scalars md report body
  {
    IFS= read -r -d '' scalars
    IFS= read -r -d '' md
    IFS= read -r -d '' report
    IFS= read -r -d '' body
  } <"$out"
  local total_cost total_minutes job_count over
  read -r total_cost total_minutes job_count over <<<"$scalars"
  if [[ -z $over ]]; then
    echo '::warning::CI Ledger internal error while computing the report; skipping'
    return 0
  fi

  # 1. job summary
  if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
    printf '%s\n' "$md" >>"$GITHUB_STEP_SUMMARY" || echo '::warning::CI Ledger could not write the job summary' >&2
  fi
  # 2. JSON report
  local rdir=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
  while [[ $rdir == ?*/ ]]; do rdir=${rdir%/}; done
  local report_path="$rdir/ci-ledger-report.json"
  printf '%s\n' "$report" >"$report_path" || echo '::warning::CI Ledger could not write the JSON report' >&2
  # 3. step outputs
  if [[ -n ${GITHUB_OUTPUT:-} ]]; then
    printf 'total_cost=%s\ntotal_minutes=%s\njob_count=%s\nover_budget=%s\nreport_path=%s\n' \
      "$total_cost" "$total_minutes" "$job_count" "$over" "$report_path" >>"$GITHUB_OUTPUT" ||
      echo '::warning::CI Ledger could not write step outputs' >&2
  fi
  # 4. PR comment
  if [[ -z $pr ]]; then
    echo 'CI Ledger: no PR context; wrote job summary only'
  elif ! is_true "${INPUT_COMMENT:-true}"; then
    echo 'CI Ledger: comment disabled; wrote job summary only'
  else
    printf '%s' "$body" >"$tmp/body.json"
    ci_ledger_comment "$api" "$repo" "$pr" "$tmp"
  fi
  # 5-7.
  echo "::notice::CI Ledger run total \$$total_cost ($total_minutes min, $job_count jobs)"
  if [[ $over == true ]]; then
    echo "::warning::Run cost \$$total_cost exceeds budget $BUDGET_FMT2"
    if is_true "${INPUT_FAIL_ON_BUDGET:-false}"; then return 1; fi
  fi
  return 0
}

# ci_ledger_comment API REPO PR TMPDIR: create or update the marker comment.
ci_ledger_comment() {
  local api=$1 repo=$2 pr=$3 tmp=$4 page=1 line len id='' cf
  while :; do
    cf="$tmp/comments-$page.json"
    if ! http_request GET "$api/repos/$repo/issues/$pr/comments?per_page=100&page=$page" "$cf"; then
      echo "::warning::CI Ledger could not post comment $(http_reason)"
      return 0
    fi
    line=$("${CI_LEDGER_JQ[@]}" -r --arg m "$CI_LEDGER_MARKER" \
      'if type == "array" then "\(length) \(first(.[] | select(type == "object" and (.body | type) == "string" and (.body | contains($m))) | .id) // "")" else "0 " end' \
      "$cf" 2>/dev/null) || line='0 '
    len=${line%% *} id=${line#* }
    # The first marker comment is final: later pages cannot change the outcome.
    [[ -n $id ]] && break
    ((len < 100)) && break
    page=$((page + 1))
  done
  if [[ -n $id ]]; then
    if http_request PATCH "$api/repos/$repo/issues/comments/$id" "$tmp/resp.json" "$tmp/body.json"; then
      echo "CI Ledger comment updated on PR #$pr"
    else
      echo "::warning::CI Ledger could not post comment $(http_reason)"
    fi
  else
    if http_request POST "$api/repos/$repo/issues/$pr/comments" "$tmp/resp.json" "$tmp/body.json"; then
      echo "CI Ledger comment created on PR #$pr"
    else
      echo "::warning::CI Ledger could not post comment $(http_reason)"
    fi
  fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  ci_ledger_main
  exit $?
fi
