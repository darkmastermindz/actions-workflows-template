# CI Ledger — GitHub Actions cost per job

CI Ledger works out a USD cost for every job in a workflow run. It writes the
breakdown to the job summary and posts it as a single PR comment that gets
updated on each run. It also sets step outputs and writes a JSON report. It
runs inside your CI, with no external service.

```yaml
# .github/workflows/ci-ledger.yml
name: CI Ledger
on:
  workflow_run:
    workflows: ["CI"]
    types: [completed]

permissions:
  actions: read
  pull-requests: write

jobs:
  cost:
    runs-on: ubuntu-latest
    steps:
      - uses: darkmastermindz/actions-workflows-template@<ref>
        id: ledger
        with:
          budget_usd: "1.00"
      - run: echo "Run cost $${{ steps.ledger.outputs.total_cost }}"
```

| Input | Default | Description |
|---|---|---|
| `github_token` | `${{ github.token }}` | Needs `actions:read`, plus `pull-requests:write` to comment. |
| `budget_usd` | — | Flags the run when its cost exceeds this amount. |
| `run_id` | triggering run | Run to analyze. |
| `pr_number` | auto | PR to comment on. |
| `top_n` | `15` | Maximum number of job rows in the table. |
| `comment` | `true` | Set to `false` to write the job summary only. |
| `fail_on_budget` | `false` | Fails the step when the run is over budget. |
| `rates_json` | — | Overrides the price table, e.g. `{"self_hosted":0.002}`. |

Outputs: `total_cost`, `total_minutes`, `job_count`, `over_budget`, `report_path`.

## Repository layout

| Path | Contents |
|---|---|
| [`REQUIREMENTS.md`](REQUIREMENTS.md) | The spec, with the suggested improvements marked **[IMPROVEMENT]**. |
| `implementations/bash` · `go` · `node` | Three separate implementations, each written from the spec alone. Each folder is also a working composite action. |
| [`benchmark/`](benchmark) | Mock GitHub API, cross-implementation conformance check and benchmark ([results](benchmark/results.md)). |
| [`action.yml`](action.yml) | The published composite action. It uses the **Node.js** engine. |

### Why Node.js

All three produce byte-identical output in every conformance scenario. **Go** is
the fastest once it is running: about 13 ms for 1000 jobs and 300 comments. But a
composite action has to compile it first, which takes about 9 s on a cold runner,
unless you commit and maintain binaries for each platform. **Node** runs in about
65 ms with zero setup, since Node is preinstalled on every hosted runner and the
code has no dependencies. That gives it the lowest total time on a fresh runner.
**Bash** also needs no setup, but it is about 3× slower than Node on large runs
because of the time spent starting `jq` and `curl` processes.

Re-run with `python3 benchmark/bench.py`.
