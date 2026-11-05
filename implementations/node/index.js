#!/usr/bin/env node
'use strict';
// CI Ledger — entry point. Configured only via environment variables (REQUIREMENTS.md §2).

const fs = require('node:fs');
const path = require('node:path');
const L = require('./lib/ledger');
const { fetchJobs, upsertComment, failText } = require('./lib/http');

const env = process.env;
const say = (line) => process.stdout.write(line + '\n');
const nonEmpty = (v) => (v == null ? '' : String(v).trim());

function readEvent(p) {
  if (!p) return {};
  try {
    const o = JSON.parse(fs.readFileSync(p, 'utf8'));
    return o !== null && typeof o === 'object' ? o : {};
  } catch {
    return {};
  }
}

function firstNonEmpty(...vals) {
  for (const v of vals) {
    const s = nonEmpty(v);
    if (s !== '') return s;
  }
  return '';
}

async function main() {
  const token = env.INPUT_GITHUB_TOKEN || env.GITHUB_TOKEN || '';
  const repo = env.GITHUB_REPOSITORY || '';
  if (!token || !repo) {
    say('::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required');
    return 1;
  }

  const event = readEvent(env.GITHUB_EVENT_PATH);
  const wr = event.workflow_run && typeof event.workflow_run === 'object' ? event.workflow_run : {};
  const runId = firstNonEmpty(env.INPUT_RUN_ID, wr.id, env.GITHUB_RUN_ID);
  if (!runId) {
    say('::error::could not resolve a run id to analyze');
    return 1;
  }
  const prs = Array.isArray(wr.pull_requests) ? wr.pull_requests : [];
  const pr = firstNonEmpty(
    env.INPUT_PR_NUMBER,
    event.pull_request && event.pull_request.number,
    prs[0] && prs[0].number
  );

  const api = L.stripSlash(env.GITHUB_API_URL || 'https://api.github.com');
  const server = L.stripSlash(env.GITHUB_SERVER_URL || 'https://github.com');
  const topN = L.parseTopN(env.INPUT_TOP_N);
  const commentOn = env.INPUT_COMMENT == null || env.INPUT_COMMENT === '' ? true : L.parseBool(env.INPUT_COMMENT);
  const failOnBudget = L.parseBool(env.INPUT_FAIL_ON_BUDGET);

  const { rates, warning: rw } = L.buildRates(env.INPUT_RATES_JSON);
  if (rw) say(rw);
  const { budgetMicro, warning: bw } = L.parseBudget(env.INPUT_BUDGET_USD);
  if (bw) say(bw);

  let jobs;
  try {
    jobs = await fetchJobs(api, repo, runId, token);
  } catch (e) {
    say(`::warning::CI Ledger could not fetch jobs ${failText(e && e.status ? e.status : 0)}; skipping`);
    return 0;
  }

  const report = L.computeReport(jobs, rates, budgetMicro);
  const body = L.renderMarkdown(report, { runId, server, repo, topN });

  if (env.GITHUB_STEP_SUMMARY) fs.appendFileSync(env.GITHUB_STEP_SUMMARY, body + '\n');

  const dir = env.RUNNER_TEMP || require('node:os').tmpdir();
  const reportPath = path.join(dir, 'ci-ledger-report.json');
  try {
    fs.writeFileSync(reportPath, JSON.stringify(L.buildJsonReport(report, { repo, runId }), null, 2) + '\n');
  } catch {
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(reportPath, JSON.stringify(L.buildJsonReport(report, { repo, runId }), null, 2) + '\n');
  }

  if (env.GITHUB_OUTPUT) fs.appendFileSync(env.GITHUB_OUTPUT, L.outputLines(report, reportPath));

  if (!pr) say('CI Ledger: no PR context; wrote job summary only');
  else if (!commentOn) say('CI Ledger: comment disabled; wrote job summary only');
  else say(await upsertComment(api, repo, pr, token, body, L.MARKER));

  const total = L.fmt4(report.totalMicro);
  say(`::notice::CI Ledger run total ${total} (${report.totalMinutes} min, ${report.jobCount} jobs)`);
  if (report.overBudget) {
    say(`::warning::Run cost ${total} exceeds budget ${L.fmt2(report.budgetMicro)}`);
    if (failOnBudget) return 1;
  }
  return 0;
}

main()
  .catch((e) => {
    process.stderr.write(String((e && e.stack) || e) + '\n');
    say(`::warning::CI Ledger internal error: ${String((e && e.message) || e).replace(/[\r\n]+/g, ' ')}`);
    return 0;
  })
  .then((code) => {
    process.exitCode = code;
    // Drop idle keep-alive sockets so the event loop ends right away.
    require('node:http').globalAgent.destroy();
    if (/^https:/i.test(env.GITHUB_API_URL || 'https://api.github.com')) require('node:https').globalAgent.destroy();
  });
