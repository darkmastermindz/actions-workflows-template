# CI Ledger — Node.js implementation

Attributes a USD cost to every job of a GitHub Actions run and reports it as a
job summary, an idempotent PR comment, step outputs and a JSON report.
Spec: [`REQUIREMENTS.md`](../../REQUIREMENTS.md).

Node >= 20, no npm dependencies (uses `node:http`/`node:https` directly for a
lighter cold start).

## Layout

- `index.js` — entry point (env parsing, side effects, exit code)
- `lib/ledger.js` — pure logic: runner detection, minutes, rates, money, markdown, JSON report
- `lib/http.js` — GitHub REST client (headers, retries, pagination, comment upsert)
- `action.yml` — composite action
- `tests/` — unit tests and end-to-end tests against a local mock API

## Run

```sh
GITHUB_TOKEN=... GITHUB_REPOSITORY=owner/repo INPUT_RUN_ID=123 node index.js
```

All configuration is via environment variables (spec §2.1). Point
`GITHUB_API_URL` at a GHES instance or a local mock for offline use.

As an action:

```yaml
- uses: ./implementations/node
  with:
    budget_usd: "0.50"
```

## Test

```sh
npm test      # or: node --test
```

`CI_LEDGER_RETRY_BASE_MS` (test-only) shortens the 1 s / 2 s retry back-off.
