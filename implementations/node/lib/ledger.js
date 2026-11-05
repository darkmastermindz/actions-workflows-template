'use strict';
// Pure logic for CI Ledger (REQUIREMENTS.md §5–§9, §12). No I/O here.

const MARKER = '<!-- ci-ledger:cost-report -->';
const FOOTER =
  '<sub>GitHub list prices, billed per started minute, gross of included free minutes. ' +
  'Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>';
const OSES = ['linux', 'windows', 'macos'];

// ---------------------------------------------------------------- §2 helpers

function parseBool(v) {
  if (v == null) return false;
  const s = String(v).trim().toLowerCase();
  return s === 'true' || s === '1' || s === 'yes';
}

function stripSlash(u) {
  return String(u).replace(/\/+$/, '');
}

function parseTopN(v) {
  if (v == null) return 15;
  const s = String(v).trim();
  if (!/^[0-9]+$/.test(s)) return 15;
  const n = Number(s);
  return n >= 1 ? n : 15;
}

// ---------------------------------------------------------------- §5 runner

const SPLIT_RE = /[^a-z0-9]+/;
const CORES_RE = /([0-9]+)[-_ ]?cores?/;

function detectRunner(labels) {
  const lower = [];
  if (Array.isArray(labels)) {
    for (const l of labels) if (l != null) lower.push(String(l).toLowerCase());
  }
  let selfHosted = false;
  let win = false;
  let mac = false;
  for (const l of lower) {
    if (l === 'self-hosted') selfHosted = true;
    const toks = l.split(SPLIT_RE);
    for (const t of toks) {
      if (!t) continue;
      if (t === 'win' || t === 'win32' || t === 'win64' || t.startsWith('windows')) win = true;
      else if (t === 'mac' || t === 'osx' || t === 'darwin' || t.startsWith('macos')) mac = true;
    }
  }
  const os = win ? 'windows' : mac ? 'macos' : 'linux';
  const m = CORES_RE.exec(lower.join(' '));
  const cores = m ? Number(m[1]) : 2;
  return { os, cores, selfHosted };
}

// ---------------------------------------------------------------- §6 time/money

const TS_RE =
  /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(?:([Zz])|([+-])(\d{2}):(\d{2}))$/;

// Returns {sec: integer epoch seconds, frac: fractional digits string} or null.
function parseTimestamp(s) {
  if (typeof s !== 'string') return null;
  const m = TS_RE.exec(s);
  if (!m) return null;
  const y = +m[1], mo = +m[2], d = +m[3], h = +m[4], mi = +m[5], se = +m[6];
  if (mo < 1 || mo > 12 || d < 1 || h > 23 || mi > 59 || se > 60) return null;
  const dim = new Date(Date.UTC(y, mo, 0)).getUTCDate();
  if (d > dim) return null;
  let off = 0;
  if (!m[8]) {
    const oh = +m[10], om = +m[11];
    if (oh > 23 || om > 59) return null;
    off = (oh * 60 + om) * 60 * (m[9] === '-' ? -1 : 1);
  }
  const ms = Date.UTC(y, mo - 1, d, h, mi, se);
  const t = new Date(ms);
  if (y < 100) t.setUTCFullYear(y); // Date.UTC maps 0..99 to 1900s
  return { sec: Math.floor(t.getTime() / 1000) - off, frac: m[7] || '' };
}

// Exact billable minutes between two RFC 3339 timestamps (0 when invalid/negative).
function jobMinutes(startedAt, completedAt) {
  const a = parseTimestamp(startedAt);
  const b = parseTimestamp(completedAt);
  if (!a || !b) return 0;
  const L = Math.max(a.frac.length, b.frac.length);
  const ds = b.sec - a.sec;
  if (L === 0) return ds <= 0 ? 0 : Math.ceil(ds / 60);
  if (L <= 6) {
    const scale = 10 ** L;
    const fa = a.frac ? Number(a.frac.padEnd(L, '0')) : 0;
    const fb = b.frac ? Number(b.frac.padEnd(L, '0')) : 0;
    const total = ds * scale + (fb - fa);
    if (total <= 0) return 0;
    return Math.ceil(total / (60 * scale)); // exact: integers < 2^53, ceil of exact ratio
  }
  const scale = 10n ** BigInt(L);
  const total = BigInt(ds) * scale + BigInt(b.frac.padEnd(L, '0')) - BigInt(a.frac.padEnd(L, '0'));
  if (total <= 0n) return 0;
  const div = 60n * scale;
  return Number((total + div - 1n) / div);
}

// Billable minutes from a seconds value (for completeness / §6 examples).
function minutesFromSeconds(seconds) {
  if (!(seconds > 0) || !Number.isFinite(seconds)) return 0;
  return Math.ceil(seconds / 60);
}

// Decimal string (or JS number) → BigInt micro-units, rounded half-up (away from zero).
const DEC_RE = /^([+-]?)(\d*)(?:\.(\d*))?(?:[eE]([+-]?\d+))?$/;
function toMicroBig(v) {
  const s = typeof v === 'number' ? String(v) : String(v).trim();
  const m = DEC_RE.exec(s);
  if (!m || (!m[2] && !m[3])) return null;
  const frac = m[3] || '';
  const digits = (m[2] || '') + frac || '0';
  const shift = (m[4] ? parseInt(m[4], 10) : 0) - frac.length + 6;
  let n = BigInt(digits);
  if (shift >= 0) n *= 10n ** BigInt(shift);
  else {
    const d = 10n ** BigInt(-shift);
    const q = n / d;
    n = (n % d) * 2n >= d ? q + 1n : q;
  }
  return m[1] === '-' ? -n : n;
}

function toMicro(v) {
  const b = toMicroBig(v);
  return b == null ? null : Number(b);
}

function fmt4(micro) {
  return '$' + fmt4Plain(micro);
}
function fmt4Plain(micro) {
  const u = (BigInt(micro) + 50n) / 100n;
  return (u / 10000n).toString() + '.' + (u % 10000n).toString().padStart(4, '0');
}
function fmt2(micro) {
  const u = (BigInt(micro) + 5000n) / 10000n;
  return '$' + (u / 100n).toString() + '.' + (u % 100n).toString().padStart(2, '0');
}

// ---------------------------------------------------------------- §7 rates

const DEFAULT_RATES = {
  standard: { linux: 0.006, windows: 0.01, macos: 0.062 },
  self_hosted: 0.0,
  larger: {
    linux: { 4: 0.012, 8: 0.022, 16: 0.042, 32: 0.082, 64: 0.162 },
    windows: { 2: 0.01, 4: 0.022, 8: 0.042, 16: 0.082, 32: 0.162, 64: 0.322 },
    macos: {},
  },
};

const isNum = (v) => typeof v === 'number' && Number.isFinite(v);
const isObj = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

// Build the micro-USD rate table. Returns {rates, warning}.
function buildRates(ratesJson) {
  const r = {
    standard: {},
    selfHosted: toMicro(DEFAULT_RATES.self_hosted),
    larger: {},
  };
  for (const os of OSES) {
    r.standard[os] = toMicro(DEFAULT_RATES.standard[os]);
    const tiers = new Map();
    for (const k of Object.keys(DEFAULT_RATES.larger[os])) tiers.set(Number(k), toMicro(DEFAULT_RATES.larger[os][k]));
    r.larger[os] = tiers;
  }
  let warning = null;
  const raw = ratesJson == null ? '' : String(ratesJson);
  if (raw.trim() !== '') {
    let o;
    try {
      o = JSON.parse(raw);
    } catch {
      o = undefined;
    }
    if (!isObj(o)) {
      warning = '::warning::CI Ledger ignored invalid rates_json';
    } else {
      if (isObj(o.standard)) {
        for (const os of OSES) if (isNum(o.standard[os])) r.standard[os] = toMicro(o.standard[os]);
      }
      if (isNum(o.self_hosted)) r.selfHosted = toMicro(o.self_hosted);
      if (isObj(o.larger)) {
        for (const os of OSES) {
          const t = o.larger[os];
          if (!isObj(t)) continue;
          for (const k of Object.keys(t)) {
            if (/^[0-9]+$/.test(k) && isNum(t[k])) r.larger[os].set(Number(k), toMicro(t[k]));
          }
        }
      }
    }
  }
  // Pre-sort tiers for lookup.
  r.tiers = {};
  for (const os of OSES) r.tiers[os] = [...r.larger[os].keys()].sort((a, b) => a - b);
  return { rates: r, warning };
}

function rateFor(rates, os, cores, selfHosted) {
  if (selfHosted) return rates.selfHosted;
  const tiers = rates.tiers[os];
  if (cores > 2 && tiers && tiers.length) {
    const exact = rates.larger[os].get(cores);
    if (exact !== undefined) return exact;
    let best = -1;
    for (const t of tiers) {
      if (t <= cores) best = t;
      else break;
    }
    if (best >= 0) return rates.larger[os].get(best);
  }
  return rates.standard[os];
}

// ---------------------------------------------------------------- §8 budget

const BUDGET_RE = /^[0-9]+(\.[0-9]+)?$/;
// Returns {budgetMicro: BigInt|null, warning}.
function parseBudget(raw) {
  if (raw == null) return { budgetMicro: null, warning: null };
  const s = String(raw).trim();
  if (s === '') return { budgetMicro: null, warning: null };
  if (!BUDGET_RE.test(s)) {
    return { budgetMicro: null, warning: `::warning::CI Ledger ignored invalid budget_usd '${raw}'` };
  }
  return { budgetMicro: toMicroBig(s), warning: null };
}

// ---------------------------------------------------------------- report model

function computeReport(jobs, rates, budgetMicro) {
  const rows = [];
  let inProgress = 0;
  let totalMicro = 0;
  let totalMinutes = 0;
  const list = Array.isArray(jobs) ? jobs : [];
  for (let i = 0; i < list.length; i++) {
    const j = list[i] || {};
    const st = j.status;
    if (st != null && st !== 'completed') {
      inProgress++;
      continue;
    }
    const { os, cores, selfHosted } = detectRunner(j.labels);
    const minutes = jobMinutes(j.started_at, j.completed_at);
    const rateMicro = rateFor(rates, os, cores, selfHosted);
    const costMicro = minutes * rateMicro;
    totalMicro += costMicro;
    totalMinutes += minutes;
    rows.push({ name: j.name == null ? '?' : String(j.name), os, cores, selfHosted, minutes, rateMicro, costMicro });
  }
  rows.sort((a, b) => b.costMicro - a.costMicro || b.minutes - a.minutes); // V8 sort is stable
  const overBudget = budgetMicro != null && BigInt(totalMicro) > budgetMicro;
  return { rows, inProgress, totalMicro, totalMinutes, jobCount: rows.length, budgetMicro, overBudget };
}

// ---------------------------------------------------------------- §9 markdown

function escapeName(name) {
  return String(name).replace(/\|/g, '\\|').replace(/[\r\n]/g, ' ');
}

function runnerLabel(os, cores, selfHosted) {
  return (selfHosted ? 'self-hosted ' : '') + os + (cores !== 2 ? ` ${cores}c` : '');
}

function renderMarkdown(report, { runId, server, repo, topN }) {
  const n = topN >= 1 ? topN : 15;
  const out = [
    MARKER,
    '### 💸 CI Ledger — GitHub Actions cost',
    '',
    `**Run total: ${fmt4(report.totalMicro)}** (${report.totalMinutes} billable min, ${report.jobCount} jobs) · ` +
      `[run ${runId}](${server}/${repo}/actions/runs/${runId})`,
    '',
  ];
  if (report.budgetMicro != null) {
    out.push(`**Budget ${fmt2(report.budgetMicro)}/run: ${report.overBudget ? '🔴 OVER' : '🟢 ok'}**`, '');
  }
  out.push('| Job | Runner | Min | Cost |', '|---|---|--:|--:|');
  const rows = report.rows;
  const shown = Math.min(rows.length, n);
  for (let i = 0; i < shown; i++) {
    const r = rows[i];
    out.push(`| ${escapeName(r.name)} | ${runnerLabel(r.os, r.cores, r.selfHosted)} | ${r.minutes} | ${fmt4(r.costMicro)} |`);
  }
  if (rows.length > shown) out.push(`| \u2026 +${rows.length - shown} more | | | |`);
  out.push('');
  if (report.inProgress > 0) {
    out.push(`_${report.inProgress} job(s) still in progress were not counted._`, '');
  }
  out.push(FOOTER);
  return out.join('\n');
}

// ---------------------------------------------------------------- §12 outputs

function buildJsonReport(report, { repo, runId }) {
  const usd = (m) => Number(m) / 1e6;
  return {
    repository: repo,
    run_id: String(runId),
    total_cost_usd: usd(report.totalMicro),
    total_minutes: report.totalMinutes,
    job_count: report.jobCount,
    in_progress_jobs: report.inProgress,
    budget_usd: report.budgetMicro == null ? null : usd(report.budgetMicro),
    over_budget: report.budgetMicro == null ? null : report.overBudget,
    jobs: report.rows.map((r) => ({
      name: r.name,
      os: r.os,
      cores: r.cores,
      self_hosted: r.selfHosted,
      minutes: r.minutes,
      rate_usd: usd(r.rateMicro),
      cost_usd: usd(r.costMicro),
    })),
  };
}

function outputLines(report, reportPath) {
  return (
    `total_cost=${fmt4Plain(report.totalMicro)}\n` +
    `total_minutes=${report.totalMinutes}\n` +
    `job_count=${report.jobCount}\n` +
    `over_budget=${report.overBudget ? 'true' : 'false'}\n` +
    `report_path=${reportPath}\n`
  );
}

module.exports = {
  MARKER,
  parseBool,
  stripSlash,
  parseTopN,
  detectRunner,
  parseTimestamp,
  jobMinutes,
  minutesFromSeconds,
  toMicro,
  toMicroBig,
  fmt4,
  fmt4Plain,
  fmt2,
  DEFAULT_RATES,
  buildRates,
  rateFor,
  parseBudget,
  computeReport,
  escapeName,
  runnerLabel,
  renderMarkdown,
  buildJsonReport,
  outputLines,
};
