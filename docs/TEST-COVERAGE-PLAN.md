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

## Phase 1: shared agent engine

Target file: `internal/agent/agent_test.go`

Existing coverage already covers cache-prefix stripping, tool-call summaries,
`extractToolText(nil)`, stalling phrase detection, and basic history rendering.
Extend it with the cheapest high-signal cases:

- `TestExtractToolTextJoinsTextBlocks`: construct a `CallToolResult` with two
  text content blocks and assert newline joining.
- `TestNormalizeToolResultTextRequiresExactPrefix`: assert `"✓result"` and
  `"↓result"` are not treated as cache markers without the following space.
- `TestRenderHistoryTextAssistantRole`: include an assistant message and assert
  it renders as `AI:`.
- `TestSummarizeHistoryForLog`: cover text, tool-call, and tool-result parts
  and assert the JSON summary includes role, part type, name/id, and truncation.
- `TestIsStallingCaseInsensitive`: make the case-insensitivity explicit.

Follow-up after these pass: introduce a bounded stall retry or max turn count in
`Engine.Turn`, then add a test for a persistently narrating model response. This
requires either a controllable model fake or a small internal generation
function seam, so keep it separate from the low-risk helper tests.

## Phase 2: Elasticsearch request builders and shapers

Target files:

- `internal/elasticsearch/process_search_test.go`
- `internal/elasticsearch/security_alerts_test.go`
- small additions to `internal/elasticsearch/security_search_test.go`

### `search_processes`

Current `runProcessSearch` builds its typed request inline before executing it.
The most useful tests need to inspect that request without always going through
an HTTP round trip. Quick helper extraction:

- Extract `buildProcessSearchRequest(args SearchProcessesArgs) (*typedsearch.Request, int)`.
- Keep `runProcessSearch` responsible for timeout, execution, DNS indexing,
  response shaping, and error wrapping.

Planned tests:

- default `Size` is 20; `Size > 100` caps to 100.
- `From > 0` is set; `From == 0` is omitted.
- executable, process name, parent name, user, host, hash, PID, and parent PID
  create term filters.
- `CommandLine` creates a match query, not a term query.
- non-positive PID values do not create PID filters.
- timestamp bounds create an `@timestamp` range filter.
- sort is always `@timestamp` descending.
- source includes contain the current process/parent/user/host/event allowlist.
- `shapeProcessResults` skips nil or malformed sources and returns a non-nil
  empty slice.
- `runProcessSearch` wraps `"all shards failed"` errors with the Elastic Agent
  endpoint-collection hint using a fake HTTP transport.

### `search_security_alerts`

Most alert behavior is already split into helpers, so avoid refactoring.

Planned tests:

- `normalizeSecurityAlertsArgs` trims strings, defaults size to 10, caps at 50.
- `buildSecurityAlertsRequest` enables `TrackTotalHits`, copies
  `alertsSourceIncludes`, and sorts `@timestamp` descending.
- severity creates a term filter.
- wildcard rule names create a wildcard filter through `buildTermQuery`.
- host creates a should-style filter over `host.name` and `host.name.keyword`.
- free-text query creates a `query_string` must clause.
- `shapeSecurityAlertsResponse` projects only alert source fields and surfaces
  `_id`, timestamp, rule name, severity, and message.
- malformed hit source returns a useful decode error.

### Existing security search helpers

Planned additions:

- `escapeQueryStringValue` covers all Elasticsearch query-string special
  characters.
- `truncateSummary` UTF-8 behavior is documented with a regression test before
  any truncation fix is attempted.
- `buildSecuritySearchRequest` includes MAC, URL, src IP, and dst IP filters.

## Phase 3: Kibana tool wrappers

Target file: `internal/kibana/tools_test.go`

The existing tests cover `Client.pathWithSpace` and `Client.DoRequest`; they do
not directly cover tool wrapper behavior. Handler registration through
`mcp.AddTool` may be awkward to call directly, so start with pure helpers where
necessary.

Recommended helper extraction:

- `normalizeKibanaAPIRequest(args KibanaAPIRequestArgs) (method, path string, err error)`
- `buildListDetectionRulesPath(args ListDetectionRulesArgs) string`
- `buildGetDetectionRulePath(args GetDetectionRuleArgs) (string, error)`
- `buildListAgentsPath(args ListAgentsArgs) string`

Planned tests:

- `kibana_api_request` defaults method to `GET`, uppercases explicit methods,
  trims and prepends `/` to paths, and rejects empty paths.
- `list_detection_rules` includes only positive `page` and `per_page` params.
- `list_detection_rules` test name should explicitly note the current
  documented default/cap mismatch.
- `get_detection_rule` rejects empty `id` and `rule_id`.
- `get_detection_rule` URL-escapes both `id` and `rule_id`, preferring `id` if
  both are supplied.
- `list_agents` uses Fleet's `perPage` casing and URL-escapes `kuery`.
- `formatResponse` pretty-prints JSON, leaves plain text/HTML alone, and
  prepends `HTTP Error <status>:` for 4xx/5xx while returning nil Go errors.

## Phase 4: utility edge cases

Target files:

- `internal/util/retry_test.go`
- `internal/util/logging_test.go`
- additions to `internal/util/normalization_test.go`
- additions to `internal/elasticsearch/security_search_test.go`

Planned tests:

- `IsRateLimitError`: nil, non-rate-limit, `429`, `rate limit`,
  `too many requests`, and `overloaded`.
- `WithRetry`: immediate success, no retry on non-rate-limit error, retries on
  rate-limit error, and context cancellation during backoff.
- `TruncateForLog`: no truncation below limit, suffix when truncated, and
  current UTF-8 byte-slicing behavior captured before changing it.
- `ClientLogFile`, `ServerLogFile`, `ServerLockFile`, and `ClientHistoryFile`
  env override behavior using `t.Setenv`.
- `NormalizeDomain`: whitespace, case, single trailing dot, and empty input.

`WithRetry` currently sleeps real backoff intervals, so practical tests may need
a tiny injectable sleep/backoff helper before retry behavior can be tested
quickly. Keep that extraction narrowly scoped.

## Phase 5: command-package pure behavior

Target file: `cmd/cli/main_test.go`

Existing tests cover markdown export and terminal markdown normalization. Add
tests that do not start Bubble Tea, an MCP server, or a provider client:

- `modelProvider` maps `gpt-`, `o1-`, `o3-`, `claude-`, and `gemini-` prefixes
  and returns empty string for unknown prefixes.
- input history: consecutive duplicate suppression, draft preservation, top and
  bottom boundary behavior.
- `formatToolCallArguments`: nested JSON-string formatting, malformed nested
  JSON fallback, empty args, and missing tool name tolerance.
- `pruneHistory`: memory-off rolling window retains the newest
  `maxHistoryMessages`.

If `stopServer` or server lock-file behavior is changed later, first extract the
PID/live-process interpretation into a pure helper and test that helper instead
of trying to signal real processes in unit tests.

## Phase 6: Redis DNS indexer

Target file: `internal/elasticsearch/indexer_test.go`

This phase has the most dependency risk because `indexer.go` writes through
`go-redis` pipelines. Do not start here.

Options:

- Add `miniredis` or an equivalent Redis-compatible test dependency.
- Introduce a tiny internal interface for the sorted-set operations used by the
  indexer and test against an in-memory fake.

Planned behavior coverage:

- only `data_stream.dataset == "zeek.dns"` documents are indexed.
- domains are normalized before key construction.
- DNS answers populate `dns:ip:<ip>`.
- source IPs populate `ip:seen:<ip>`.
- invalid timestamps fall back safely.
- sorted sets are trimmed to `maxEntityHistory`.
- TTLs are applied/refreshed.

## Suggested Order

1. Phase 1 helper tests in `internal/agent`.
2. Phase 2 `security_alerts` tests, because helpers already exist.
3. Phase 2 `process_search` after extracting `buildProcessSearchRequest`.
4. Phase 3 Kibana path/format helpers.
5. Phase 4 utility tests, with a small retry-sleep extraction if needed.
6. Phase 5 CLI pure behavior.
7. Phase 6 Redis indexer after choosing the fake Redis strategy.

