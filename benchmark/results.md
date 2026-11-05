# CI Ledger — benchmark results

30 timed runs per cell after 3 warmups, local mock API (`benchmark/mock_api.js`), 4 vCPU. Conformance: **PASS** across 11 scenarios.

## `golden`

| impl | median ms | mean ms | p95 ms | stdev | peak RSS MB |
|---|--:|--:|--:|--:|--:|
| go | 4.7 | 4.7 | 5.3 | 0.3 | 19.3 |
| node | 39.4 | 40.7 | 51.3 | 6.1 | 45.5 |
| bash | 54.8 | 55.6 | 58.8 | 2.4 | 19.2 |

## `large_patch`

| impl | median ms | mean ms | p95 ms | stdev | peak RSS MB |
|---|--:|--:|--:|--:|--:|
| go | 12.4 | 13.2 | 16.3 | 1.8 | 19.3 |
| node | 63.5 | 63.6 | 70.0 | 6.5 | 54.4 |
| bash | 183.4 | 185.2 | 204.5 | 10.3 | 19.3 |

## Fresh-runner total for a composite action (setup + `large_patch` median)

| impl | total ms |
|---|--:|
| node | 64 |
| bash | 183 |
| go | 9390 |

## Setup cost on a fresh runner

| impl | setup | notes |
|---|--:|---|
| bash | 0 s | curl + jq preinstalled |
| node | 0 s | node preinstalled, no deps |
| go | 9.38 s cold / 0.09 s warm | `go build`, binary 8.9 MB |

## Source size (non-blank, non-test lines)

| impl | LOC |
|---|--:|
| bash | 483 |
| go | 1049 |
| node | 545 |

Versions: GNU bash, version 5.2.21(1)-release (x86_64-pc-linux-gnu); jq-1.7; go version go1.24.7 linux/amd64; v22.23.4
