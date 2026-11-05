'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const L = require('../lib/ledger');
const { GOLDEN_JOBS, GOLDEN_BODY } = require('./fixtures');

test('§5 runner detection table', () => {
  const cases = [
    [['ubuntu-latest'], 'linux', 2, false],
    [['windows-latest'], 'windows', 2, false],
    [['macos-14'], 'macos', 2, false],
    [['self-hosted', 'linux', 'x64'], 'linux', 2, true],
    [['self-hosted', 'darwin', 'arm64'], 'macos', 2, true],
    [['ubuntu-latest-8-cores'], 'linux', 8, false],
    [['windows-2022-16core'], 'windows', 16, false],
    [['build-machine'], 'linux', 2, false],
    [['twin-runner'], 'linux', 2, false],
    [[], 'linux', 2, false],
    [undefined, 'linux', 2, false],
    [null, 'linux', 2, false],
  ];
  for (const [labels, os, cores, selfHosted] of cases) {
    assert.deepEqual(L.detectRunner(labels), { os, cores, selfHosted }, JSON.stringify(labels));
  }
});

test('§5 extra detection details', () => {
  assert.equal(L.detectRunner(['Windows2022']).os, 'windows');
  assert.equal(L.detectRunner(['macos14-large']).os, 'macos');
  assert.equal(L.detectRunner(['osx']).os, 'macos');
  assert.equal(L.detectRunner(['win64']).os, 'windows');
  assert.equal(L.detectRunner(['darwin', 'windows']).os, 'windows');
  assert.equal(L.detectRunner(['Self-Hosted']).selfHosted, true);
  assert.equal(L.detectRunner(['self_hosted']).selfHosted, false);
  assert.equal(L.detectRunner(['linux', '4 cores']).cores, 4);
  assert.equal(L.detectRunner(['linux_32_cores']).cores, 32);
  assert.equal(L.detectRunner(['4', 'cores']).cores, 4); // joined with a space
  assert.equal(L.detectRunner(['16corex']).cores, 16);
});

test('§6 minutes from seconds examples', () => {
  for (const [s, m] of [[0, 0], [1, 1], [60, 1], [60.5, 2], [61, 2], [599, 10]]) {
    assert.equal(L.minutesFromSeconds(s), m, `${s}s`);
  }
});

test('§6 minutes from timestamps (exact, offsets, fractions)', () => {
  const t0 = '2026-06-14T01:00:00Z';
  assert.equal(L.jobMinutes(t0, '2026-06-14T01:00:00Z'), 0);
  assert.equal(L.jobMinutes(t0, '2026-06-14T01:00:01Z'), 1);
  assert.equal(L.jobMinutes(t0, '2026-06-14T01:01:00Z'), 1);
  assert.equal(L.jobMinutes(t0, '2026-06-14T01:01:00.5Z'), 2);
  assert.equal(L.jobMinutes(t0, '2026-06-14T01:01:01Z'), 2);
  assert.equal(L.jobMinutes(t0, '2026-06-14T01:09:59Z'), 10);
  assert.equal(L.jobMinutes('2026-06-14T03:00:00+02:00', '2026-06-14T01:03:00Z'), 3);
  assert.equal(L.jobMinutes('2026-06-13T20:00:00-05:00', '2026-06-14T01:01:00Z'), 1);
  assert.equal(L.jobMinutes('2026-06-14T01:00:00.250Z', '2026-06-14T01:01:00.250Z'), 1);
  assert.equal(L.jobMinutes('2026-06-14T01:00:00.000000001Z', '2026-06-14T01:01:00.000000002Z'), 2);
  assert.equal(L.jobMinutes('2026-06-14T01:00:00.1234567Z', '2026-06-14T01:01:00.1234567Z'), 1);
  assert.equal(L.jobMinutes('2026-06-14T01:00:59.9Z', '2026-06-14T01:01:00Z'), 1);
  // missing / null / unparsable / negative → 0
  assert.equal(L.jobMinutes(t0, null), 0);
  assert.equal(L.jobMinutes(undefined, t0), 0);
  assert.equal(L.jobMinutes(t0, 'garbage'), 0);
  assert.equal(L.jobMinutes(t0, '2026-02-30T01:00:00Z'), 0);
  assert.equal(L.jobMinutes(t0, '2026-06-14T01:05:00'), 0);
  assert.equal(L.jobMinutes('2026-06-14T01:05:00Z', t0), 0);
});

test('§6 money formatting', () => {
  assert.equal(L.fmt4(562000), '$0.5620');
  assert.equal(L.fmt4(1234567), '$1.2346');
  assert.equal(L.fmt4(0), '$0.0000');
  assert.equal(L.fmt4(49), '$0.0000');
  assert.equal(L.fmt4(50), '$0.0001');
  assert.equal(L.fmt4Plain(562000), '0.5620');
  assert.equal(L.fmt2(500000), '$0.50');
  assert.equal(L.fmt2(4999), '$0.00');
  assert.equal(L.fmt2(5000), '$0.01');
  assert.equal(L.fmt2(123456789), '$123.46');
});

test('§6 rate_micro rounding half-up, exact decimal', () => {
  assert.equal(L.toMicro(0.006), 6000);
  assert.equal(L.toMicro(0.062), 62000);
  assert.equal(L.toMicro(0.0000005), 1);
  assert.equal(L.toMicro(0.0000004), 0);
  assert.equal(L.toMicro(1e-7), 0);
  assert.equal(L.toMicro(2.5e-6), 3);
  assert.equal(L.toMicro(0), 0);
  assert.equal(L.toMicro(12), 12000000);
});

test('§7.1 rate examples', () => {
  const { rates, warning } = L.buildRates('');
  assert.equal(warning, null);
  const r = (os, c, sh = false) => L.rateFor(rates, os, c, sh);
  assert.equal(r('linux', 8), 22000);
  assert.equal(r('linux', 12), 22000);
  assert.equal(r('linux', 3), 6000);
  assert.equal(r('macos', 12), 62000);
  assert.equal(r('windows', 2), 10000);
  assert.equal(r('linux', 2), 6000);
  assert.equal(r('windows', 2), 10000);
  assert.equal(r('macos', 2), 62000);
  assert.equal(r('linux', 64, true), 0);
  assert.equal(r('linux', 4), 12000);
  assert.equal(r('linux', 128), 162000);
  assert.equal(r('windows', 3), 10000);
  assert.equal(r('windows', 64), 322000);
});

test('§7.2 rates_json overrides', () => {
  const { rates, warning } = L.buildRates(
    '{"standard": {"linux": 0.005}, "self_hosted": 0.002, "larger": {"linux": {"4": 0.011, "96": 0.25}}, "bogus": 1}'
  );
  assert.equal(warning, null);
  assert.equal(L.rateFor(rates, 'linux', 2, false), 5000);
  assert.equal(L.rateFor(rates, 'windows', 2, false), 10000);
  assert.equal(L.rateFor(rates, 'linux', 2, true), 2000);
  assert.equal(L.rateFor(rates, 'linux', 4, false), 11000);
  assert.equal(L.rateFor(rates, 'linux', 8, false), 22000);
  assert.equal(L.rateFor(rates, 'linux', 100, false), 250000);
  const mac = L.buildRates('{"larger":{"macos":{"12":0.1}}}').rates;
  assert.equal(L.rateFor(mac, 'macos', 12, false), 100000);
  assert.equal(L.rateFor(mac, 'macos', 8, false), 62000);
});

test('§7.2 invalid rates_json warns and keeps defaults', () => {
  for (const bad of ['{not json', '[1,2]', '42', 'null']) {
    const { rates, warning } = L.buildRates(bad);
    assert.equal(warning, '::warning::CI Ledger ignored invalid rates_json');
    assert.equal(L.rateFor(rates, 'linux', 2, false), 6000);
  }
});

test('§8 budget parsing', () => {
  assert.deepEqual(L.parseBudget(''), { budgetMicro: null, warning: null });
  assert.deepEqual(L.parseBudget('   '), { budgetMicro: null, warning: null });
  assert.equal(L.parseBudget('0.50').budgetMicro, 500000n);
  assert.equal(L.parseBudget(' 2 ').budgetMicro, 2000000n);
  assert.equal(L.parseBudget('0.0000005').budgetMicro, 1n);
  assert.equal(L.parseBudget('0.0000004999').budgetMicro, 0n);
  assert.equal(L.parseBudget('1.2345678').budgetMicro, 1234568n);
  assert.equal(L.parseBudget('99999999999999999999').budgetMicro, 99999999999999999999000000n);
  for (const bad of ['abc', '-1', '1.', '.5', '1e3', '$5']) {
    const r = L.parseBudget(bad);
    assert.equal(r.budgetMicro, null);
    assert.equal(r.warning, `::warning::CI Ledger ignored invalid budget_usd '${bad}'`);
  }
});

test('§8 sorting: cost desc, minutes desc, stable', () => {
  const { rates } = L.buildRates('');
  const mk = (name, mins, labels = ['ubuntu-latest'], status = 'completed') => ({
    name, labels, status,
    started_at: '2026-01-01T00:00:00Z',
    completed_at: `2026-01-01T00:${String(mins).padStart(2, '0')}:00Z`,
  });
  const rep = L.computeReport(
    [mk('a', 1), mk('sh1', 3, ['self-hosted']), mk('b', 1), mk('sh2', 5, ['self-hosted']), mk('c', 2), mk('q', 9, [], 'queued')],
    rates, null
  );
  assert.deepEqual(rep.rows.map((r) => r.name), ['c', 'a', 'b', 'sh2', 'sh1']);
  assert.equal(rep.inProgress, 1);
  assert.equal(rep.overBudget, false);
});

test('§6 counted jobs: completed or missing status', () => {
  const { rates } = L.buildRates('');
  const base = { labels: ['ubuntu-latest'], started_at: '2026-01-01T00:00:00Z', completed_at: '2026-01-01T00:01:00Z' };
  const jobs = ['completed', null, undefined, 'queued', 'in_progress', 'waiting', 'pending', 'requested'].map((s, i) => {
    const j = { ...base, name: 'j' + i };
    if (s !== undefined) j.status = s;
    return j;
  });
  const rep = L.computeReport(jobs, rates, null);
  assert.equal(rep.jobCount, 3);
  assert.equal(rep.inProgress, 5);
  assert.equal(rep.totalMinutes, 3);
  assert.equal(rep.totalMicro, 18000);
});

test('§9 name escaping and runner label', () => {
  assert.equal(L.escapeName('big | test'), 'big \\| test');
  assert.equal(L.escapeName('a\r\nb\nc'), 'a  b c');
  assert.equal(L.runnerLabel('linux', 2, false), 'linux');
  assert.equal(L.runnerLabel('linux', 2, true), 'self-hosted linux');
  assert.equal(L.runnerLabel('linux', 8, false), 'linux 8c');
  assert.equal(L.runnerLabel('macos', 4, true), 'self-hosted macos 4c');
});

function golden(topN = 15, budget = '0.50') {
  const { rates } = L.buildRates('');
  const rep = L.computeReport(GOLDEN_JOBS, rates, L.parseBudget(budget).budgetMicro);
  return { rep, body: L.renderMarkdown(rep, { runId: '123', server: 'https://github.com', repo: 'owner/repo', topN }) };
}

test('§13 golden example body', () => {
  const { rep, body } = golden();
  assert.equal(body, GOLDEN_BODY);
  assert.equal(rep.totalMicro, 562000);
  assert.equal(rep.totalMinutes, 26);
  assert.equal(rep.jobCount, 6);
  assert.equal(rep.overBudget, true);
});

test('§13 golden with top_n=4 truncates', () => {
  const { body } = golden(4);
  const expected = GOLDEN_BODY.replace(
    '| lint | linux | 1 | $0.0060 |\n| deploy | self-hosted linux | 5 | $0.0000 |',
    '| … +2 more | | | |'
  );
  assert.equal(body, expected);
});

test('§9 no budget, no in-progress, zero jobs', () => {
  const { rates } = L.buildRates('');
  const rep = L.computeReport([], rates, null);
  const body = L.renderMarkdown(rep, { runId: '9', server: 'https://ghe.example', repo: 'o/r', topN: 15 });
  assert.equal(
    body,
    [
      '<!-- ci-ledger:cost-report -->',
      '### 💸 CI Ledger — GitHub Actions cost',
      '',
      '**Run total: $0.0000** (0 billable min, 0 jobs) · [run 9](https://ghe.example/o/r/actions/runs/9)',
      '',
      '| Job | Runner | Min | Cost |',
      '|---|---|--:|--:|',
      '',
      '<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>',
    ].join('\n')
  );
});

test('§9 budget ok line', () => {
  const { body } = golden(15, '1');
  assert.ok(body.includes('\n**Budget $1.00/run: 🟢 ok**\n\n| Job'));
});

test('§12 JSON report and output lines', () => {
  const { rep } = golden();
  const j = L.buildJsonReport(rep, { repo: 'owner/repo', runId: '123' });
  assert.equal(j.repository, 'owner/repo');
  assert.equal(j.run_id, '123');
  assert.equal(j.total_cost_usd, 0.562);
  assert.equal(j.total_minutes, 26);
  assert.equal(j.job_count, 6);
  assert.equal(j.in_progress_jobs, 1);
  assert.equal(j.budget_usd, 0.5);
  assert.equal(j.over_budget, true);
  assert.deepEqual(j.jobs[0], {
    name: 'integration (macos)', os: 'macos', cores: 2, self_hosted: false, minutes: 7, rate_usd: 0.062, cost_usd: 0.434,
  });
  assert.equal(j.jobs[2].name, 'big | test');
  assert.equal(
    L.outputLines(rep, '/tmp/runner/ci-ledger-report.json'),
    'total_cost=0.5620\ntotal_minutes=26\njob_count=6\nover_budget=true\nreport_path=/tmp/runner/ci-ledger-report.json\n'
  );
  const nb = L.buildJsonReport(golden(15, '').rep, { repo: 'owner/repo', runId: '123' });
  assert.equal(nb.budget_usd, null);
  assert.equal(nb.over_budget, null);
});

test('§2 helpers', () => {
  for (const t of ['true', 'TRUE', '1', 'yes', 'Yes', ' true ']) assert.equal(L.parseBool(t), true, t);
  for (const f of ['false', '0', 'no', '', 'on', undefined]) assert.equal(L.parseBool(f), false, String(f));
  assert.equal(L.stripSlash('http://x/'), 'http://x');
  assert.equal(L.parseTopN(undefined), 15);
  assert.equal(L.parseTopN('0'), 15);
  assert.equal(L.parseTopN('-2'), 15);
  assert.equal(L.parseTopN('abc'), 15);
  assert.equal(L.parseTopN('4'), 4);
});
