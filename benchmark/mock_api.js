#!/usr/bin/env node
// Deterministic mock of the GitHub REST endpoints CI Ledger uses.
// Stateless (POST/PATCH do not mutate), so every benchmark iteration is identical.
//
// Runs:   123  golden example (REQUIREMENTS.md §13)
//         5    small run, 5 jobs
//         1000 large run, 1000 jobs (10 pages)
//         404  jobs endpoint returns 404
// PRs:    42   no comments            -> POST
//         300  300 comments, marker at index 250 (page 3) -> PATCH
//         403  comments endpoint returns 403
'use strict';
const http = require('node:http');

const MARKER = '<!-- ci-ledger:cost-report -->';
const stats = { GET: 0, POST: 0, PATCH: 0 };

const golden = [
  ['build', ['ubuntu-latest'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:07:31Z'],
  ['integration (macos)', ['macos-latest'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:06:01Z'],
  ['lint', ['ubuntu-latest'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:00:40Z'],
  ['deploy', ['self-hosted', 'linux', 'x64'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:04:30Z'],
  ['big | test', ['ubuntu-latest-8-cores'], 'completed', '2026-06-14T01:00:00Z', '2026-06-14T01:02:00Z'],
  ['e2e', ['ubuntu-latest'], 'in_progress', '2026-06-14T01:00:00Z', null],
  ['win', ['windows-latest'], 'completed', '2026-06-14T03:00:00+02:00', '2026-06-14T01:03:00Z'],
].map(([name, labels, status, started_at, completed_at], i) =>
  ({ id: i + 1, name, labels, status, started_at, completed_at }));

// Deterministic PRNG (mulberry32) for the synthetic large run.
function rng(seed) {
  return () => {
    seed |= 0; seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const LABELS = [
  ['ubuntu-latest'], ['ubuntu-24.04'], ['windows-latest'], ['macos-14'], ['macos-latest'],
  ['self-hosted', 'linux', 'x64'], ['self-hosted', 'darwin', 'arm64'], ['ubuntu-latest-4-cores'],
  ['ubuntu-latest-16-cores'], ['windows-2022-8core'], ['linux-12-cores'], ['build-machine'],
];
function synth(n, seed) {
  const r = rng(seed);
  const base = Date.UTC(2026, 5, 14, 1, 0, 0);
  const jobs = [];
  for (let i = 0; i < n; i++) {
    const start = base + Math.floor(r() * 3600) * 1000;
    const dur = Math.floor(r() * 1800 * 1000); // ms, includes fractional seconds
    const st = r() < 0.02 ? 'in_progress' : 'completed';
    const iso = (ms) => new Date(ms).toISOString();
    jobs.push({
      id: 10000 + i,
      name: `job ${i}` + (i % 97 === 0 ? ' | matrix' : '') + (i % 5 === 0 ? ` (os ${i % 3})` : ''),
      labels: LABELS[Math.floor(r() * LABELS.length)],
      status: st,
      started_at: iso(start),
      completed_at: st === 'completed' ? iso(start + dur) : null,
    });
  }
  return jobs;
}
const runs = { 123: golden, 5: synth(5, 7), 1000: synth(1000, 42) };

function comments(n, markerAt) {
  return Array.from({ length: n }, (_, i) => ({
    id: 900000 + i,
    body: i === markerAt ? `${MARKER}\nold report` : `comment ${i} lorem ipsum dolor sit amet`,
  }));
}
const prs = { 42: [], 300: comments(300, 250) };

function send(res, code, obj) {
  const s = JSON.stringify(obj);
  res.writeHead(code, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(s) });
  res.end(s);
}

const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  if (u.pathname === '/__stats') return send(res, 200, stats);
  if (u.pathname === '/__reset') { stats.GET = stats.POST = stats.PATCH = 0; return send(res, 200, stats); }
  stats[req.method] = (stats[req.method] || 0) + 1;
  if (!/^Bearer .+/.test(req.headers.authorization || '')) return send(res, 401, { message: 'no auth' });
  const page = Math.max(1, parseInt(u.searchParams.get('page') || '1', 10));
  const per = Math.min(100, parseInt(u.searchParams.get('per_page') || '30', 10));
  let m;
  if (req.method === 'GET' && (m = u.pathname.match(/^\/repos\/[^/]+\/[^/]+\/actions\/runs\/(\d+)\/jobs$/))) {
    const jobs = runs[m[1]];
    if (!jobs) return send(res, 404, { message: 'Not Found' });
    return send(res, 200, { total_count: jobs.length, jobs: jobs.slice((page - 1) * per, page * per) });
  }
  if (req.method === 'GET' && (m = u.pathname.match(/^\/repos\/[^/]+\/[^/]+\/issues\/(\d+)\/comments$/))) {
    const list = prs[m[1]];
    if (!list) return send(res, 403, { message: 'Forbidden' });
    return send(res, 200, list.slice((page - 1) * per, page * per));
  }
  if (req.method === 'POST' || req.method === 'PATCH') {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      try { JSON.parse(body).body.length; } catch { return send(res, 422, { message: 'bad body' }); }
      send(res, req.method === 'POST' ? 201 : 200, { id: 1 });
    });
    return;
  }
  send(res, 404, { message: 'Not Found' });
});

server.listen(parseInt(process.env.PORT || '0', 10), '127.0.0.1', () => {
  process.stdout.write(`${server.address().port}\n`);
});
