'use strict';
// End-to-end tests: run `node index.js` against a node:http mock of the GitHub API.
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFile } = require('node:child_process');
const { GOLDEN_JOBS, GOLDEN_BODY } = require('./fixtures');

const INDEX = path.join(__dirname, '..', 'index.js');

// opts: jobs (array), comments (array), status overrides via fail(req) → status|undefined
function startMock(opts) {
  const log = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const body = Buffer.concat(chunks).toString('utf8');
      const u = new URL(req.url, 'http://x');
      const entry = { method: req.method, path: u.pathname, query: u.search, headers: req.headers, body };
      log.push(entry);
      const send = (status, obj) => {
        const s = JSON.stringify(obj);
        res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(s) });
        res.end(s);
      };
      const forced = opts.fail && opts.fail(entry, log);
      if (forced) return send(forced, { message: 'forced' });
      const page = Number(u.searchParams.get('page') || 1);
      const per = Number(u.searchParams.get('per_page') || 30);
      let m;
      if (req.method === 'GET' && (m = /^\/repos\/owner\/repo\/actions\/runs\/(\d+)\/jobs$/.exec(u.pathname))) {
        const jobs = opts.jobs || [];
        return send(200, { total_count: jobs.length, jobs: jobs.slice((page - 1) * per, page * per) });
      }
      if (req.method === 'GET' && /^\/repos\/owner\/repo\/issues\/\d+\/comments$/.test(u.pathname)) {
        const c = opts.comments || [];
        return send(200, c.slice((page - 1) * per, page * per));
      }
      if (req.method === 'POST' && /^\/repos\/owner\/repo\/issues\/\d+\/comments$/.test(u.pathname)) {
        return send(201, { id: 999, body: JSON.parse(body).body });
      }
      if (req.method === 'PATCH' && (m = /^\/repos\/owner\/repo\/issues\/comments\/(\d+)$/.exec(u.pathname))) {
        return send(200, { id: Number(m[1]), body: JSON.parse(body).body });
      }
      send(404, { message: 'Not Found' });
    });
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const url = `http://127.0.0.1:${server.address().port}`;
      resolve({ url, log, close: () => new Promise((r) => server.close(r)) });
    });
  });
}

function tmpdir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'ci-ledger-test-'));
}

function run(env) {
  return new Promise((resolve) => {
    const clean = {};
    for (const k of ['PATH', 'SYSTEMROOT', 'HOME']) if (process.env[k]) clean[k] = process.env[k];
    execFile(process.execPath, [INDEX], { env: { ...clean, ...env } }, (err, stdout, stderr) => {
      resolve({ code: err ? err.code : 0, stdout, stderr });
    });
  });
}

async function setup(mockOpts, extraEnv = {}, event = { workflow_run: { id: 123, pull_requests: [{ number: 42 }] } }) {
  const mock = await startMock(mockOpts);
  const dir = tmpdir();
  const eventPath = path.join(dir, 'event.json');
  fs.writeFileSync(eventPath, JSON.stringify(event));
  const runnerTemp = path.join(dir, 'runner');
  fs.mkdirSync(runnerTemp);
  const env = {
    INPUT_GITHUB_TOKEN: 'tok-secret',
    GITHUB_REPOSITORY: 'owner/repo',
    GITHUB_SERVER_URL: 'https://github.com/',
    GITHUB_API_URL: mock.url + '/',
    GITHUB_EVENT_PATH: eventPath,
    GITHUB_STEP_SUMMARY: path.join(dir, 'summary.md'),
    GITHUB_OUTPUT: path.join(dir, 'output.txt'),
    RUNNER_TEMP: runnerTemp,
    INPUT_BUDGET_USD: '0.50',
    CI_LEDGER_RETRY_BASE_MS: '10',
    ...extraEnv,
  };
  const r = await run(env);
  await mock.close();
  const read = (p) => (fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : null);
  return {
    ...r,
    log: mock.log,
    summary: read(env.GITHUB_STEP_SUMMARY),
    output: read(env.GITHUB_OUTPUT),
    reportPath: path.join(runnerTemp, 'ci-ledger-report.json'),
    json: read(path.join(runnerTemp, 'ci-ledger-report.json')),
  };
}

const outputsFor = (reportPath) =>
  `total_cost=0.5620\ntotal_minutes=26\njob_count=6\nover_budget=true\nreport_path=${reportPath}\n`;

test('e2e golden: summary, outputs, stdout, comment created', async () => {
  const r = await setup({ jobs: GOLDEN_JOBS, comments: [{ id: 1, body: 'hello' }] });
  assert.equal(r.code, 0);
  assert.equal(
    r.stdout,
    'CI Ledger comment created on PR #42\n' +
      '::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)\n' +
      '::warning::Run cost $0.5620 exceeds budget $0.50\n'
  );
  assert.equal(r.summary, GOLDEN_BODY + '\n');
  assert.equal(r.output, outputsFor(r.reportPath));
  const json = JSON.parse(r.json);
  assert.equal(json.total_cost_usd, 0.562);
  assert.equal(json.run_id, '123');
  assert.equal(json.jobs.length, 6);
  // requests
  const jobsReq = r.log[0];
  assert.equal(jobsReq.path, '/repos/owner/repo/actions/runs/123/jobs');
  assert.equal(jobsReq.query, '?per_page=100&page=1');
  assert.equal(jobsReq.headers.authorization, 'Bearer tok-secret');
  assert.equal(jobsReq.headers.accept, 'application/vnd.github+json');
  assert.equal(jobsReq.headers['x-github-api-version'], '2022-11-28');
  assert.equal(jobsReq.headers['user-agent'], 'ci-ledger');
  assert.equal(jobsReq.headers['content-type'], undefined);
  const post = r.log.find((e) => e.method === 'POST');
  assert.equal(post.path, '/repos/owner/repo/issues/42/comments');
  assert.equal(post.headers['content-type'], 'application/json');
  assert.deepEqual(JSON.parse(post.body), { body: GOLDEN_BODY });
  assert.ok(!r.stdout.includes('tok-secret'));
});

test('e2e golden: fail_on_budget → exit 1; top_n=4', async () => {
  const r = await setup({ jobs: GOLDEN_JOBS }, { INPUT_FAIL_ON_BUDGET: 'TRUE', INPUT_TOP_N: '4' });
  assert.equal(r.code, 1);
  const expected = GOLDEN_BODY.replace(
    '| lint | linux | 1 | $0.0060 |\n| deploy | self-hosted linux | 5 | $0.0000 |',
    '| … +2 more | | | |'
  );
  assert.equal(r.summary, expected + '\n');
});

test('e2e PATCH when marker comment is on a later page (300 comments)', async () => {
  const comments = [];
  for (let i = 1; i <= 300; i++) comments.push({ id: i, body: i === 250 ? 'x <!-- ci-ledger:cost-report --> y' : `c${i}` });
  const r = await setup({ jobs: GOLDEN_JOBS, comments });
  assert.equal(r.code, 0);
  assert.equal(r.stdout.split('\n')[0], 'CI Ledger comment updated on PR #42');
  const lists = r.log.filter((e) => e.method === 'GET' && e.path.endsWith('/comments'));
  assert.deepEqual(lists.map((e) => e.query), ['?per_page=100&page=1', '?per_page=100&page=2', '?per_page=100&page=3']);
  const patch = r.log.find((e) => e.method === 'PATCH');
  assert.equal(patch.path, '/repos/owner/repo/issues/comments/250');
  assert.deepEqual(JSON.parse(patch.body), { body: GOLDEN_BODY });
  assert.equal(r.log.filter((e) => e.method === 'POST').length, 0);
});

test('e2e pagination of 1000 jobs', async () => {
  const jobs = [];
  for (let i = 0; i < 1000; i++) {
    jobs.push({
      name: `job ${i}`,
      labels: [i % 3 === 0 ? 'windows-latest' : 'ubuntu-latest'],
      status: 'completed',
      started_at: '2026-06-14T01:00:00Z',
      completed_at: `2026-06-14T01:${String((i % 59) + 1).padStart(2, '0')}:00Z`,
    });
  }
  let totalMicro = 0;
  let totalMin = 0;
  jobs.forEach((j, i) => {
    const m = (i % 59) + 1;
    totalMin += m;
    totalMicro += m * (i % 3 === 0 ? 10000 : 6000);
  });
  const r = await setup({ jobs }, { INPUT_BUDGET_USD: '' }, { workflow_run: { id: 77 } });
  assert.equal(r.code, 0);
  const pages = r.log.filter((e) => e.path.endsWith('/jobs')).map((e) => e.query);
  assert.equal(pages.length, 11); // 10 full pages + an empty one
  assert.equal(pages[10], '?per_page=100&page=11');
  const u = Math.floor((totalMicro + 50) / 100);
  const cost = `${Math.floor(u / 10000)}.${String(u % 10000).padStart(4, '0')}`;
  assert.equal(
    r.output,
    `total_cost=${cost}\ntotal_minutes=${totalMin}\njob_count=1000\nover_budget=false\nreport_path=${r.reportPath}\n`
  );
  assert.equal(
    r.stdout,
    `CI Ledger: no PR context; wrote job summary only\n::notice::CI Ledger run total $${cost} (${totalMin} min, 1000 jobs)\n`
  );
  assert.ok(r.summary.includes('| … +985 more | | | |\n'));
  assert.equal(JSON.parse(r.json).jobs.length, 1000);
});

test('e2e comment disabled', async () => {
  const r = await setup({ jobs: GOLDEN_JOBS }, { INPUT_COMMENT: 'false', INPUT_BUDGET_USD: '' });
  assert.equal(r.stdout, 'CI Ledger: comment disabled; wrote job summary only\n::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)\n');
  assert.equal(r.log.length, 1);
  assert.ok(!r.summary.includes('Budget'));
});

test('e2e jobs fetch failure → warning, exit 0, nothing written', async () => {
  const r = await setup({ jobs: GOLDEN_JOBS, fail: (e) => (e.path.endsWith('/jobs') ? 403 : 0) });
  assert.equal(r.code, 0);
  assert.equal(r.stdout, '::warning::CI Ledger could not fetch jobs (HTTP 403); skipping\n');
  assert.equal(r.summary, null);
  assert.equal(r.output, null);
  assert.equal(r.json, null);
  assert.equal(r.log.length, 1); // 4xx not retried
});

test('e2e retries 5xx then gives up after 3 attempts', async () => {
  const r = await setup({ jobs: GOLDEN_JOBS, fail: (e) => (e.path.endsWith('/jobs') ? 502 : 0) });
  assert.equal(r.stdout, '::warning::CI Ledger could not fetch jobs (HTTP 502); skipping\n');
  assert.equal(r.log.length, 3);
});

test('e2e retry on 429 recovers', async () => {
  let n = 0;
  const r = await setup({ jobs: GOLDEN_JOBS, fail: (e) => (e.path.endsWith('/jobs') && n++ < 2 ? 429 : 0) });
  assert.equal(r.code, 0);
  assert.equal(r.summary, GOLDEN_BODY + '\n');
});

test('e2e network error on jobs', async () => {
  const r = await setup({}, { GITHUB_API_URL: 'http://127.0.0.1:1' });
  assert.equal(r.code, 0);
  assert.equal(r.stdout, '::warning::CI Ledger could not fetch jobs (network error); skipping\n');
});

test('e2e comment POST failure is a warning only', async () => {
  const r = await setup(
    { jobs: GOLDEN_JOBS, fail: (e) => (e.method === 'POST' ? 403 : 0) },
    { INPUT_BUDGET_USD: '1', INPUT_FAIL_ON_BUDGET: 'true' }
  );
  assert.equal(r.code, 0);
  assert.equal(
    r.stdout,
    '::warning::CI Ledger could not post comment (HTTP 403)\n::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)\n'
  );
  assert.ok(r.summary.includes('**Budget $1.00/run: 🟢 ok**'));
});

test('e2e config warnings come first', async () => {
  const r = await setup({ jobs: GOLDEN_JOBS }, { INPUT_BUDGET_USD: 'lots', INPUT_RATES_JSON: '{oops', INPUT_PR_NUMBER: '7' });
  const lines = r.stdout.split('\n');
  assert.equal(lines[0], '::warning::CI Ledger ignored invalid rates_json');
  assert.equal(lines[1], "::warning::CI Ledger ignored invalid budget_usd 'lots'");
  assert.equal(lines[2], 'CI Ledger comment created on PR #7');
  assert.equal(r.output.split('\n')[3], 'over_budget=false');
});

test('e2e rates_json override applied', async () => {
  const r = await setup({ jobs: GOLDEN_JOBS }, { INPUT_RATES_JSON: '{"self_hosted": 0.002, "standard": {"macos": 0.1}}', INPUT_COMMENT: 'no' });
  assert.ok(r.summary.includes('| integration (macos) | macos | 7 | $0.7000 |'));
  assert.ok(r.summary.includes('| deploy | self-hosted linux | 5 | $0.0100 |'));
});

test('e2e config errors exit 1', async () => {
  let r = await run({ GITHUB_REPOSITORY: 'owner/repo' });
  assert.equal(r.code, 1);
  assert.equal(r.stdout, '::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required\n');
  r = await run({ GITHUB_TOKEN: 't' });
  assert.equal(r.stdout, '::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required\n');
  r = await run({ GITHUB_TOKEN: 't', GITHUB_REPOSITORY: 'owner/repo', GITHUB_EVENT_PATH: '/nonexistent/event.json' });
  assert.equal(r.code, 1);
  assert.equal(r.stdout, '::error::could not resolve a run id to analyze\n');
});

test('e2e run id / PR resolution precedence', async () => {
  const r = await setup(
    { jobs: GOLDEN_JOBS },
    { INPUT_RUN_ID: '555', GITHUB_RUN_ID: '1', INPUT_BUDGET_USD: '' },
    { pull_request: { number: 9 }, workflow_run: { id: 123, pull_requests: [{ number: 42 }] } }
  );
  assert.equal(r.log[0].path, '/repos/owner/repo/actions/runs/555/jobs');
  assert.equal(r.stdout.split('\n')[0], 'CI Ledger comment created on PR #9');
  const r2 = await setup({ jobs: [] }, { GITHUB_RUN_ID: '31', INPUT_BUDGET_USD: '' }, {});
  assert.equal(r2.log[0].path, '/repos/owner/repo/actions/runs/31/jobs');
  assert.ok(r2.summary.includes('(0 billable min, 0 jobs) · [run 31](https://github.com/owner/repo/actions/runs/31)'));
});
