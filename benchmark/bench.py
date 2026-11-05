#!/usr/bin/env python3
"""Conformance + benchmark harness for the three CI Ledger implementations.

Usage: python3 benchmark/bench.py [--runs N] [--out benchmark/results]

1. Builds the Go binary (cold cache, timed = fresh-runner setup cost).
2. Starts benchmark/mock_api.js on a random port.
3. Runs every scenario against every implementation and diffs stdout, exit code,
   job summary, GITHUB_OUTPUT and JSON report across implementations, and the
   golden scenario against REQUIREMENTS.md §13 verbatim.
4. Times N iterations per implementation per perf scenario (wall time and peak
   RSS of the process tree, via os.wait4), and writes results.{json,md}.
"""
import argparse
import json
import os
import shutil
import statistics
import subprocess
import sys
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMPL = os.path.join(ROOT, "implementations")
WORK = tempfile.mkdtemp(prefix="ci-ledger-bench-")

GOLDEN_BODY = """<!-- ci-ledger:cost-report -->
### 💸 CI Ledger — GitHub Actions cost

**Run total: $0.5620** (26 billable min, 6 jobs) · [run 123](https://github.com/owner/repo/actions/runs/123)

**Budget $0.50/run: 🔴 OVER**

| Job | Runner | Min | Cost |
|---|---|--:|--:|
| integration (macos) | macos | 7 | $0.4340 |
| build | linux | 8 | $0.0480 |
| big \\| test | linux 8c | 2 | $0.0440 |
| win | windows | 3 | $0.0300 |
| lint | linux | 1 | $0.0060 |
| deploy | self-hosted linux | 5 | $0.0000 |

_1 job(s) still in progress were not counted._

<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>
"""
GOLDEN_STDOUT = """CI Ledger comment created on PR #42
::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)
::warning::Run cost $0.5620 exceeds budget $0.50
"""

SCENARIOS = {
    # name: (event, env overrides)
    "golden": ({"workflow_run": {"id": 123, "pull_requests": [{"number": 42}]}},
               {"INPUT_BUDGET_USD": "0.50"}),
    "golden_top4_fail": ({"workflow_run": {"id": 123, "pull_requests": [{"number": 42}]}},
                         {"INPUT_BUDGET_USD": "0.50", "INPUT_TOP_N": "4", "INPUT_FAIL_ON_BUDGET": "TRUE"}),
    "large_patch": ({"pull_request": {"number": 300}},
                    {"INPUT_RUN_ID": "1000", "INPUT_BUDGET_USD": "250"}),
    "large_rates_override": ({"workflow_run": {"id": 1000, "pull_requests": []}},
                             {"INPUT_RATES_JSON": '{"standard":{"linux":0.005},"self_hosted":0.002,'
                                                  '"larger":{"linux":{"12":0.03}},"bogus":1}',
                              "INPUT_TOP_N": "0"}),
    "small_no_event": (None, {"GITHUB_RUN_ID": "5"}),
    "small_comment_off": ({"pull_request": {"number": 42}},
                          {"INPUT_RUN_ID": "5", "INPUT_COMMENT": "false"}),
    "comment_403": ({}, {"INPUT_RUN_ID": "5", "INPUT_PR_NUMBER": "403"}),
    "jobs_404": ({}, {"INPUT_RUN_ID": "404"}),
    "bad_inputs": ({}, {"INPUT_RUN_ID": "5", "INPUT_BUDGET_USD": "1e3",
                        "INPUT_RATES_JSON": "{not json", "INPUT_TOP_N": "abc"}),
    "missing_token": ({}, {"INPUT_RUN_ID": "5", "INPUT_GITHUB_TOKEN": "", "GITHUB_TOKEN": ""}),
    "missing_run_id": ({}, {}),
}
PERF_SCENARIOS = ["golden", "large_patch"]


def sh(cmd, **kw):
    return subprocess.run(cmd, check=True, capture_output=True, text=True, **kw)


def build():
    """Prepare commands; time Go's cold build (what a fresh runner pays)."""
    setup = {"bash": 0.0, "node": 0.0}
    gobin = os.path.join(WORK, "ci-ledger-go")
    env = dict(os.environ, GOCACHE=os.path.join(WORK, "gocache"), GOFLAGS="-mod=mod")
    t = time.perf_counter()
    sh(["go", "build", "-o", gobin, "."], cwd=os.path.join(IMPL, "go"), env=env)
    setup["go"] = time.perf_counter() - t
    t = time.perf_counter()
    sh(["go", "build", "-o", gobin, "."], cwd=os.path.join(IMPL, "go"), env=env)
    setup["go_warm"] = time.perf_counter() - t
    cmds = {
        "bash": ["bash", os.path.join(IMPL, "bash", "ci-ledger.sh")],
        "go": [gobin],
        "node": ["node", os.path.join(IMPL, "node", "index.js")],
    }
    return cmds, setup, os.path.getsize(gobin)


def base_env(port, sdir):
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(("INPUT_", "GITHUB_", "RUNNER_"))}
    env.update({
        "GITHUB_REPOSITORY": "owner/repo",
        "GITHUB_API_URL": f"http://127.0.0.1:{port}/",   # trailing slash must be stripped
        "GITHUB_SERVER_URL": "https://github.com",
        "INPUT_GITHUB_TOKEN": "test-token-123",
        "GITHUB_STEP_SUMMARY": os.path.join(sdir, "summary.md"),
        "GITHUB_OUTPUT": os.path.join(sdir, "output.txt"),
        "RUNNER_TEMP": sdir,
    })
    return env


def run_once(cmd, port, scenario, tag):
    sdir = os.path.join(WORK, "runs", tag)
    shutil.rmtree(sdir, ignore_errors=True)
    os.makedirs(sdir)
    event, overrides = SCENARIOS[scenario]
    env = base_env(port, sdir)
    if event is not None:
        p = os.path.join(sdir, "event.json")
        with open(p, "w") as f:
            json.dump(event, f)
        env["GITHUB_EVENT_PATH"] = p
    env.update(overrides)
    t = time.perf_counter()
    proc = subprocess.Popen(cmd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    out, err = proc.communicate()
    # communicate() reaps; get rusage of all reaped children of this process tree
    wall = time.perf_counter() - t
    return sdir, proc.returncode, out.decode(), err.decode(), wall


def timed(cmd, env):
    """Spawn and reap with wait4 to get this child's own peak RSS (KB on Linux)."""
    t = time.perf_counter()
    pid = os.posix_spawnp(cmd[0], cmd, env, file_actions=[
        (os.POSIX_SPAWN_OPEN, 1, os.devnull, os.O_WRONLY, 0),
        (os.POSIX_SPAWN_OPEN, 2, os.devnull, os.O_WRONLY, 0)])
    _, status, ru = os.wait4(pid, 0)
    return time.perf_counter() - t, ru.ru_maxrss, os.waitstatus_to_exitcode(status)


def read(p):
    try:
        with open(p, encoding="utf-8") as f:
            return f.read()
    except FileNotFoundError:
        return None


def artifacts(sdir, code, out):
    rep = read(os.path.join(sdir, "ci-ledger-report.json"))
    outputs = read(os.path.join(sdir, "output.txt"))
    if outputs:
        outputs = outputs.replace(sdir, "<RUNNER_TEMP>")
    return {
        "exit": code,
        "stdout": out,
        "summary": read(os.path.join(sdir, "summary.md")),
        "outputs": outputs,
        "report": json.loads(rep) if rep else None,
    }


def conformance(cmds, port):
    results, failures = {}, []
    for sc in SCENARIOS:
        arts = {}
        for name, cmd in cmds.items():
            sdir, code, out, err, _ = run_once(cmd, port, sc, f"{sc}-{name}")
            arts[name] = artifacts(sdir, code, out)
            arts[name]["stderr"] = err[-500:]
        if sc == "golden":
            for name, a in arts.items():
                if a["summary"] != GOLDEN_BODY:
                    failures.append(f"{sc}/{name}: summary != REQUIREMENTS §13")
                if a["stdout"] != GOLDEN_STDOUT:
                    failures.append(f"{sc}/{name}: stdout != REQUIREMENTS §13")
                if a["exit"] != 0:
                    failures.append(f"{sc}/{name}: exit {a['exit']} != 0")
        ref = arts["go"]
        for name, a in arts.items():
            for k in ("exit", "stdout", "summary", "outputs", "report"):
                if a[k] != ref[k]:
                    failures.append(f"{sc}/{name}: '{k}' differs from go")
        results[sc] = arts
    return results, failures


def bench(cmds, port, runs):
    res = {}
    for sc in PERF_SCENARIOS:
        for name, cmd in cmds.items():
            sdir = os.path.join(WORK, "perf", f"{sc}-{name}")
            os.makedirs(sdir, exist_ok=True)
            event, overrides = SCENARIOS[sc]
            env = base_env(port, sdir)
            ep = os.path.join(sdir, "event.json")
            with open(ep, "w") as f:
                json.dump(event, f)
            env["GITHUB_EVENT_PATH"] = ep
            env.update(overrides)
            for _ in range(3):  # warmup (page cache, mock JIT)
                timed(cmd, env)
            walls, rss = [], []
            for _ in range(runs):
                w, r, code = timed(cmd, env)
                assert code == 0, f"{name}/{sc} exited {code}"
                walls.append(w * 1000)
                rss.append(r / 1024)
            walls.sort()
            res.setdefault(sc, {})[name] = {
                "mean_ms": statistics.mean(walls),
                "median_ms": statistics.median(walls),
                "p95_ms": walls[int(0.95 * (len(walls) - 1))],
                "stdev_ms": statistics.stdev(walls),
                "min_ms": walls[0],
                "max_rss_mb": max(rss),
            }
    return res


def loc(folder):
    total = 0
    for dp, _, fs in os.walk(os.path.join(IMPL, folder)):
        if "test" in dp.split(os.sep)[-1]:
            continue
        for f in fs:
            if f.endswith((".sh", ".go", ".js", ".mjs", ".jq")) and "_test" not in f and ".test." not in f:
                with open(os.path.join(dp, f), encoding="utf-8") as fh:
                    total += sum(1 for line in fh if line.strip())
    return total


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=30)
    ap.add_argument("--out", default=os.path.join(ROOT, "benchmark", "results"))
    a = ap.parse_args()

    cmds, setup, gosize = build()
    mock = subprocess.Popen(["node", os.path.join(ROOT, "benchmark", "mock_api.js")],
                            stdout=subprocess.PIPE, text=True)
    port = int(mock.stdout.readline())
    try:
        conf, failures = conformance(cmds, port)
        print("conformance:", "PASS" if not failures else "FAIL")
        for f in failures:
            print("  -", f)
        perf = bench(cmds, port, a.runs)
    finally:
        mock.terminate()

    meta = {
        "runs": a.runs,
        "setup_s": setup,
        "go_binary_bytes": gosize,
        "source_loc": {n: loc(n) for n in cmds},
        "versions": {
            "bash": sh(["bash", "--version"]).stdout.splitlines()[0],
            "jq": sh(["jq", "--version"]).stdout.strip(),
            "go": sh(["go", "version"]).stdout.strip(),
            "node": sh(["node", "--version"]).stdout.strip(),
        },
        "cpus": os.cpu_count(),
    }
    with open(a.out + ".json", "w") as f:
        json.dump({"meta": meta, "conformance_failures": failures, "perf": perf}, f, indent=2)

    md = ["# CI Ledger — benchmark results", "",
          f"{a.runs} timed runs per cell after 3 warmups, local mock API "
          f"(`benchmark/mock_api.js`), {meta['cpus']} vCPU. "
          f"Conformance: **{'PASS' if not failures else 'FAIL (%d)' % len(failures)}** "
          f"across {len(SCENARIOS)} scenarios.", ""]
    for sc in PERF_SCENARIOS:
        md += [f"## `{sc}`", "", "| impl | median ms | mean ms | p95 ms | stdev | peak RSS MB |",
               "|---|--:|--:|--:|--:|--:|"]
        for n, r in sorted(perf[sc].items(), key=lambda kv: kv[1]["median_ms"]):
            md.append(f"| {n} | {r['median_ms']:.1f} | {r['mean_ms']:.1f} | {r['p95_ms']:.1f} | "
                      f"{r['stdev_ms']:.1f} | {r['max_rss_mb']:.1f} |")
        md.append("")
    lp = perf["large_patch"]
    total = {n: setup.get(n, 0.0) * 1000 + lp[n]["median_ms"] for n in cmds}
    md += ["## Fresh-runner total for a composite action (setup + `large_patch` median)", "",
           "| impl | total ms |", "|---|--:|"]
    md += [f"| {n} | {v:.0f} |" for n, v in sorted(total.items(), key=lambda kv: kv[1])]
    md.append("")
    md += ["## Setup cost on a fresh runner", "",
           "| impl | setup | notes |", "|---|--:|---|",
           "| bash | 0 s | curl + jq preinstalled |",
           "| node | 0 s | node preinstalled, no deps |",
           f"| go | {setup['go']:.2f} s cold / {setup['go_warm']:.2f} s warm | `go build`, "
           f"binary {gosize / 1e6:.1f} MB |", "",
           "## Source size (non-blank, non-test lines)", "",
           "| impl | LOC |", "|---|--:|"]
    md += [f"| {n} | {c} |" for n, c in meta["source_loc"].items()]
    md += ["", "Versions: " + "; ".join(meta["versions"].values())]
    with open(a.out + ".md", "w") as f:
        f.write("\n".join(md) + "\n")
    print("\n".join(md))
    shutil.rmtree(WORK, ignore_errors=True)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
