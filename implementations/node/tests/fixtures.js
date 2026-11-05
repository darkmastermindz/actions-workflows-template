'use strict';
// §13 golden example data.

const job = (name, labels, status, started_at, completed_at) => ({ name, labels, status, started_at, completed_at });

const GOLDEN_JOBS = [
  job('build', ['ubuntu-latest'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:07:31Z'),
  job('integration (macos)', ['macos-latest'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:06:01Z'),
  job('lint', ['ubuntu-latest'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:00:40Z'),
  job('deploy', ['self-hosted', 'linux', 'x64'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:04:30Z'),
  job('big | test', ['ubuntu-latest-8-cores'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:02:00Z'),
  job('e2e', ['ubuntu-latest'], 'in_progress', '2026-06-14T01:00:00Z', null),
  job('win', ['windows-latest'], 'completed', '2026-06-14T03:00:00+02:00', '2026-06-14T01:03:00Z'),
];

const GOLDEN_BODY = `<!-- ci-ledger:cost-report -->
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

<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>`;

module.exports = { GOLDEN_JOBS, GOLDEN_BODY };
