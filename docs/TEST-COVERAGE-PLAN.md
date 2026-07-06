# Test Coverage Plan

This plan expands the coverage notes in `ISSUES.md` into small implementation
slices. The goal is to raise confidence around behavior that is already encoded
in the implementation and architecture docs, without requiring live
Elasticsearch, Kibana, Redis, LLM provider calls, or an MCP subprocess for the
first pass.

## Constraints

- Prefer package-local unit tests and `httptest` transports.
- Do not require live Elastic/Kibana/Redis services in default `go test ./...`.
- Avoid broad refactors just to test behavior. Extract tiny pure helpers only
  when the current code makes an important behavior hard to observe.
- Keep new external test dependencies optional. If Redis indexer tests need a
  fake Redis server, evaluate that separately instead of mixing it into the
  first slice.

## Status

- **Phase 1 (shared agent engine) — done.** `internal/agent/agent_test.go`
  covers cache-prefix stripping, tool-call summaries, `extractToolText`
  (including multi-block join via `TestExtractToolTextJoinsTextBlocks`),
  stalling detection, history rendering, and `TestSummarizeHistoryForLog`.
  The one item that did **not** land: a bounded stall-retry / max-turn-count
  test for `Engine.Turn` — see the open item below, still not started.
- **Phase 2, `search_processes` and `search_security_alerts` — done.**
  `internal/elasticsearch/process_search_test.go` (after extracting
  `buildProcessSearchRequest`) and `internal/elasticsearch/security_alerts_test.go`
  cover both tools per the original plan.
- **Phase 2, "existing security search helpers" — done.**
  `security_search_test.go` now also covers `escapeQueryStringValue`
  (`TestEscapeQueryStringValueEscapesAllSpecialChars`), `truncateSummary`'s
  byte-slicing behavior (`TestTruncateSummaryUTF8Boundary`, a pinned
  regression test — the rune-unsafety bug itself is not fixed), and
  `buildSecuritySearchRequest`'s `MAC`/`URL`/`SrcIP`/`DstIP` filters
  (`TestBuildSecuritySearchRequestMACURLAndDirectionalIPFilters`).
- **Phase 3 (Kibana tool wrappers) — done.** `internal/kibana/tools_test.go`
  covers all four extracted path/method helpers plus `formatResponse`.
- **Phase 5 (command-package pure behavior) — done.** `cmd/cli/main_test.go`
  now covers `modelProvider`, `pushInputHistory`/`browseHistory`,
  `pruneHistory`, and `formatToolCallArguments`, via a `newTestModel()`
  helper that builds a bare `model` without Bubble Tea/MCP/LLM startup.
- **Phase 6 (Redis DNS indexer) — done.** `internal/elasticsearch/indexer_test.go`
  added `github.com/alicebob/miniredis/v2` as a test-only dependency and runs
  `indexSearchResult`/`indexTypedSearchResult`/`indexZeekDNSHit` against a
  real `*redis.Client` pointed at an in-process miniredis server — covers the
  zeek.dns-only filter, domain normalization, resolved-IP/source-IP
  population, invalid-timestamp fallback, `maxEntityHistory` trimming, and
  TTL apply/refresh.
- **Phase 4 (utility edge cases) — not started.** No `internal/util/retry_test.go`
  or `internal/util/logging_test.go` exist yet. Next up — see below.
- **Stall-retry / max-turn-count follow-up — not started.** See below.

## Remaining work

### 1. Bounded stall-retry / max-turn-count test (was Phase 1 follow-up)

`Engine.Turn` (`internal/agent/agent.go`) still has an unconditional `for {}`
loop with no iteration cap when the model keeps narrating instead of acting or
answering (`isStalling` keeps matching). Before adding a test, this needs a
small seam: either a max-turn-count parameter/constant on `Engine`, or a
fake/controllable `provider.LanguageModel` that can be driven to always return
stalling text. Keep this scoped narrowly — it's a correctness fix with a test,
not a refactor.

### 2. Phase 4: utility edge cases

Target files:

- `internal/util/retry_test.go`
- `internal/util/logging_test.go`
- additions to `internal/util/normalization_test.go` (currently only
  `TestNormalizeJSON`/`TestNormalizeDomain` exist; both already reasonably
  covered — lower priority within this phase)

Planned tests:

- `IsRateLimitError`: nil, non-rate-limit, `429`, `rate limit`,
  `too many requests`, and `overloaded`.
- `WithRetry`: immediate success, no retry on non-rate-limit error, retries on
  rate-limit error, and context cancellation during backoff.
- `TruncateForLog`: no truncation below limit, suffix when truncated, and
  current UTF-8 byte-slicing behavior captured before changing it (same
  rune-unsafety concern as `truncateSummary`, now pinned by
  `TestTruncateSummaryUTF8Boundary` — consider fixing both together once both
  are pinned by tests).
- `ClientLogFile`, `ServerLogFile`, `ServerLockFile`, and `ClientHistoryFile`
  env override behavior using `t.Setenv`.

`WithRetry` currently sleeps real backoff intervals (2s, doubling), so
practical tests need a tiny injectable sleep/backoff helper before retry
behavior can be tested quickly — scope that extraction narrowly.

If `stopServer` or server lock-file behavior is changed later, first extract
the PID/live-process interpretation into a pure helper and test that helper
instead of trying to signal real processes in unit tests.

## Suggested order

1. Utility tests (Phase 4), with a small retry-sleep extraction if needed.
2. Stall-retry/max-turn-count seam + test.
