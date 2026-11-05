'use strict';
// Minimal GitHub REST client on node:http / node:https (lighter cold start than fetch/undici).

const MAX_ATTEMPTS = 3;
const TIMEOUT_MS = 30000;

function retryBaseMs() {
  // Test hook only: scales the 1 s / 2 s back-off (default 1000 ms).
  const v = Number(process.env.CI_LEDGER_RETRY_BASE_MS);
  return Number.isFinite(v) && v >= 0 ? v : 1000;
}

function once(method, url, token, body) {
  const mod = url.charCodeAt(4) === 115 /* 's' */ ? require('node:https') : require('node:http');
  const headers = {
    Authorization: `Bearer ${token}`,
    Accept: 'application/vnd.github+json',
    'X-GitHub-Api-Version': '2022-11-28',
    'User-Agent': 'ci-ledger',
  };
  let payload;
  if (body !== undefined) {
    payload = Buffer.from(JSON.stringify(body), 'utf8');
    headers['Content-Type'] = 'application/json';
    headers['Content-Length'] = payload.length;
  }
  return new Promise((resolve) => {
    let done = false;
    const finish = (v) => {
      if (!done) {
        done = true;
        resolve(v);
      }
    };
    let req;
    try {
      req = mod.request(url, { method, headers, timeout: TIMEOUT_MS }, (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => finish({ status: res.statusCode, body: Buffer.concat(chunks).toString('utf8') }));
        res.on('error', () => finish({ status: 0, body: '' }));
      });
    } catch {
      return finish({ status: 0, body: '' });
    }
    req.on('timeout', () => req.destroy(new Error('timeout')));
    req.on('error', () => finish({ status: 0, body: '' }));
    req.end(payload);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Returns {status, body}; status 0 means network error. Retries 429 / 5xx / network errors.
async function request(method, url, token, body) {
  let res;
  for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
    if (attempt > 0) await sleep(retryBaseMs() * attempt);
    res = await once(method, url, token, body);
    const s = res.status;
    if (!(s === 0 || s === 429 || s >= 500)) return res;
  }
  return res;
}

function failText(status) {
  return status === 0 ? '(network error)' : `(HTTP ${status})`;
}

class ApiError extends Error {
  constructor(status) {
    super(failText(status));
    this.status = status;
  }
}

async function getJson(url, token) {
  const res = await request('GET', url, token);
  if (res.status < 200 || res.status >= 300) throw new ApiError(res.status);
  try {
    return JSON.parse(res.body);
  } catch {
    throw new ApiError(res.status);
  }
}

async function fetchJobs(api, repo, runId, token) {
  const all = [];
  for (let page = 1; ; page++) {
    const data = await getJson(`${api}/repos/${repo}/actions/runs/${runId}/jobs?per_page=100&page=${page}`, token);
    const jobs = data && Array.isArray(data.jobs) ? data.jobs : [];
    for (const j of jobs) all.push(j);
    if (jobs.length < 100) return all;
  }
}

// Creates or updates the marker comment. Returns the stdout line to print.
async function upsertComment(api, repo, pr, token, body, marker) {
  try {
    let id = null;
    for (let page = 1; id === null; page++) {
      const list = await getJson(`${api}/repos/${repo}/issues/${pr}/comments?per_page=100&page=${page}`, token);
      const items = Array.isArray(list) ? list : [];
      for (const c of items) {
        if (c && typeof c.body === 'string' && c.body.includes(marker)) {
          id = c.id;
          break;
        }
      }
      if (items.length < 100) break;
    }
    if (id !== null) {
      const res = await request('PATCH', `${api}/repos/${repo}/issues/comments/${id}`, token, { body });
      if (res.status < 200 || res.status >= 300) throw new ApiError(res.status);
      return `CI Ledger comment updated on PR #${pr}`;
    }
    const res = await request('POST', `${api}/repos/${repo}/issues/${pr}/comments`, token, { body });
    if (res.status < 200 || res.status >= 300) throw new ApiError(res.status);
    return `CI Ledger comment created on PR #${pr}`;
  } catch (e) {
    const status = e instanceof ApiError ? e.status : 0;
    return `::warning::CI Ledger could not post comment ${failText(status)}`;
  }
}

module.exports = { request, fetchJobs, upsertComment, ApiError, failText };
