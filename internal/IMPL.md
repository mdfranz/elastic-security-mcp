# internal/ Implementation Guide

Implementation-level reference for the packages under `internal/`. For architecture/data-flow diagrams see the architecture docs; this file is about concrete behavior — types, functions, gotchas — for someone about to modify the code.

## Cross-cutting patterns

- **Panic safety**: every MCP tool handler in `elasticsearch` wraps its body with `defer recoverToolPanic(toolName, &err)` (`elasticsearch/tools.go`), converting a panic into a returned error instead of crashing the server. `kibana/tools.go` handlers do **not** do this — a gap if a Kibana tool ever panics.
- **Timeout tiers**: `ensureToolTimeout`/`ensureSearchTimeout` (30s default) vs. `ensureExportTimeout` (30 min) vs. per-scroll-batch `ExportBatchTimeout` (180s). All independently configurable via env vars, and all only applied if the incoming context has no deadline already (an MCP client-supplied deadline is never overridden).
- **Response truncation**: two near-duplicate "shrink the hits array proportionally to fit under `MaxResponseChars()`" implementations exist — `truncateResults`/`truncateSlice` in `tools.go` (raw ES response shape, also strips `_index`/`_id`/`_score` per hit) and `truncateSecuritySearchResults` in `security_search.go` (already-shaped hits array). Touching truncation behavior means updating both.
- **Cache signaling contract**: `elasticsearch.WrapWithCache` prefixes tool result text with `"✓ "` (cache hit) or `"↓ "` (freshly stored) and sets `Meta["cache_status"]`. `internal/agent`'s `Engine.callTool` is the consumer — it strips the prefix into `ToolCallEvent.IsCached`/`IsStored`, which both `cmd/cli` and `internal/webui` then render. This is an implicit string-based protocol spanning packages; changing the prefixes means updating both `elasticsearch.WrapWithCache` and `agent.normalizeToolResultText`.
- **Hidden DNS indexing side effect**: `search_elastic`, `search_security_events`, and `search_processes` all opportunistically call `cache.IndexSearchResult`/`IndexTypedSearchResult` to feed the Redis DNS entity index (see `indexer.go`), even though the tool's stated purpose is unrelated. The indexer itself no-ops anything that isn't a `zeek.dns` document, so this is safe but easy to forget when adding a new search-shaped tool.
- **Rune-unsafe truncation**: `truncateSummary` (`security_search.go`) and `TruncateForLog` (`util/logging.go`) both byte-slice strings (`s[:n]`) rather than truncating on rune boundaries — a latent bug for non-ASCII payloads (could split a multi-byte UTF-8 character).

## internal/agent

### agent.go
The shared LLM + MCP tool-calling loop, driven by both `cmd/cli` (TUI and one-shot mode) and `internal/webui`. Before this package existed, each front end implemented its own copy of this loop; see `cmd/IMPL.md`'s cross-cutting section for the history.

- `SystemPrompt` (exported const): the tool-selection guide steering the model (search_security_alerts, search_processes, search_security_events, list_indices, list_kibana_spaces, list_detection_rules/get_detection_rule, list_agents, lookup_domain/lookup_ip, search_elastic, kibana_api_request). Single canonical copy — previously the CLI and webui each had a slightly-drifted copy.
- `Engine{MCPClient *goaimcp.Client, Model provider.LanguageModel, Tools []goai.Tool, ModelName string}` — `ModelName` is optional, used only for the debug log line. Holds no per-conversation state, so one `Engine` can be constructed once (e.g. in `RunServer`) and reused/shared across turns/connections.
- `New(mcpClient, model, tools, modelName) *Engine`.
- `EventKind` enum: `EventStatus`, `EventToolStart`, `EventToolEnd`, `EventAssistant`. Deliberately does **not** include an `EventError` — a terminal error is only ever communicated via `TurnResult.Err` once `Turn` returns, since an error always ends the loop immediately; there's no need for callers to also handle it mid-stream.
- `ToolCallEvent{Call provider.ToolCall, Seq int, Args map[string]any, State, Result string, IsError/IsCached/IsStored bool}` — sent once with `State: "running"` before a call, once more with `"completed"`/`"error"` after.
- `Event{Kind, Status, Tool *ToolCallEvent, Text string}` — what `Turn` emits via its `emit` callback.
- `TurnResult{History []provider.Message, Err error}` — what `Turn` returns.
- **`(*Engine) Turn(ctx, history []provider.Message, emit func(Event)) TurnResult`**: the loop itself.
  - Takes a **defensive copy** of `history` on entry (`append([]provider.Message(nil), history...)`) and only ever appends to that copy — it never mutates the caller's slice/backing array. This matters because `cmd/cli` runs `Turn` in a background goroutine while `model.history` is read/written from the (different) Bubble Tea `Update` goroutine; without the copy, `append` reusing spare backing-array capacity from the caller's slice could race.
  - Each iteration: emits `EventStatus{"Analyzing request..."}`, logs a debug summary (`summarizeHistoryForLog`), calls `goai.GenerateText` wrapped in `util.WithRetry` with `llmobs.Hooks()...`, `WithSystem(SystemPrompt)`, `WithTemperature(0)`, `WithMaxOutputTokens(4096)`.
  - Defensive nil-result check: if `WithRetry` returns `(nil, nil)` (shouldn't happen, but guarded), returns `TurnResult{Err: "LLM returned no response"}` — this check previously existed only in the CLI's `Update`, now both front ends get it.
  - **Stall detection** (`isStalling`): if the response has no tool calls and the lowercased text contains `"i will"`/`"let me"`/`"now i'll"`/`"searching"`, appends a synthetic `goai.UserMessage(stallCorrection)` (not shown to the user) and loops — **no iteration cap**, so a persistently narrating model could loop indefinitely.
  - No tool calls + non-stalling text: emits `EventAssistant{Text}` (only if non-empty) and returns `TurnResult{History: hist}` — this ends the turn.
  - Tool calls present: emits `EventStatus{summarizeToolCalls(toolCalls)}`, then calls `callTool` for each **sequentially** (never in parallel), appending each returned `ToolMessage` to `hist`, then emits `EventStatus{"Tool results received. Drafting final answer..."}` and loops back to call the LLM again.
- **`callTool(ctx, seq, tc, emit) provider.Message`**: executes one tool call.
  - Emits `EventToolStart` (with `State: "running"`) **before** calling, and `EventToolEnd` (with the final state) **after** — this is what lets both front ends show live per-tool progress instead of waiting for a whole batch.
  - Validates `tc.Name != ""` and that `tc.Input` unmarshals to `map[string]any`, short-circuiting to an error `ToolMessage` (with matching `EventToolStart`/`EventToolEnd` pair) if not.
  - Calls `e.MCPClient.CallTool`, tracks latency (`time.Since`), classifies `isError` from either a Go error or `toolResp.IsError`.
  - `normalizeToolResultText` (unexported): strips the `"✓ "`/`"↓ "` cache-status prefixes into `isCached`/`isStored` booleans (see cross-cutting cache note above).
- `isStalling(text) bool`, `summarizeToolCalls(toolCalls) string` (0/1/2/N-call phrasing), `extractToolText(toolResp)` (joins `goaimcp.ParseTextContent` blocks), `summarizeHistoryForLog(history)` (JSON debug summary with truncated previews via `util.TruncateForLog`) — all unexported, internal to the loop.
- **`RenderHistoryText(history) string`** (exported): renders text parts as `"Human:"/"AI:"` lines — used by both front ends' `/memory` command, outside of `Turn`.

Tested by `agent_test.go`: `TestNormalizeToolResultText` (cache-prefix stripping), `TestSummarizeToolCalls`, `TestExtractToolText`/`TestExtractToolTextJoinsTextBlocks` (multi-block join, non-text blocks skipped, malformed JSON tolerated), `TestIsStalling`, `TestRenderHistoryText`, `TestSummarizeHistoryForLog` (role/part-type/name/truncation shape of the debug JSON).

## internal/elasticsearch

### client.go
Wraps `go-elasticsearch/v9`. `Client{Raw *elasticsearch.Client, Typed *elasticsearch.TypedClient}` — both built from the same `Config{Addresses, APIKey}` in `NewClient`. API-key auth only (no username/password path, unlike Kibana's client). `HttpError(method, res)` reads/truncates (`util.TruncateForLog`, 2048 chars) the response body into a formatted error; used wherever `esapi.Response.IsError()`.

### cache.go
Redis-backed cache for tool call results, distinct from (but cooperating with) the DNS entity indexer in `indexer.go`.

- `ToolCache{client *redis.Client, enabled bool}`. `NewToolCache()` reads `CACHE_ENABLED`/`REDIS_ADDR`, pings Redis with a 2s timeout at startup; a failed ping **disables caching silently** rather than failing startup.
- `CacheEnabled()` is opt-out: anything other than `0/false/no/off` means enabled.
- TTL getters `ListIndicesTTL()` (3600s), `SearchElasticTTL()` (600s), `SearchSecurityEventsTTL()` (600s), each overridable by env var.
- `cacheKey(toolName, args)` = SHA-256(`toolName + ":" + json.Marshal(args)`). Adding a field to an args struct silently changes cache keys (cold-starts cache, not a correctness issue).
- `Get`/`Set` are thin Redis wrappers; **any Redis error (including misses) is treated as a cache miss and falls through to live data** — cache errors never fail a tool call.
- `IndexSearchResult`/`IndexTypedSearchResult`: no-ops if disabled, else delegate to `indexer.go` to opportunistically extract Zeek DNS records from any search result.
- `LookupDomain(ctx, domain)`: `ZRevRange` on `dns:name:<domain>`, top 100. `LookupIP(ctx, ip)`: reads both `dns:ip:<ip>` and `ip:seen:<ip>`, top 100 each.
- `WrapWithCache[A any](cache, toolName, ttl, inner HandlerFunc) HandlerFunc`: the caching decorator used by `search_elastic`, `list_indices`, `search_security_events`, `search_security_alerts`. On hit, prefixes text `"✓ "`; on miss, calls `inner`, stores result, prefixes `"↓ "` (see cross-cutting note above).
- **Gotcha**: `search_processes` does *not* use `WrapWithCache` — it calls `IndexTypedSearchResult` (DNS indexing) but never caches its own result, inconsistent with the other three search tools.

### indexer.go
Redis "entity index" — a rolling window of Zeek DNS activity backing `lookup_domain`/`lookup_ip`. Runs as a side effect of every search result, not a separate poller.

- `entityTTL = 24h`, `maxEntityHistory = 500` (per sorted-set key).
- `dnsRecord{Ts, Src, Answers}` / `ipRecord{Ts, Domain, Src, Type}` — JSON-serialized as sorted-set *members* (each member is a JSON blob, not a hash).
- `indexSearchResult` (raw ES response) and `indexTypedSearchResult` (typed `search.Response`) both walk hits and call `indexZeekDNSHit` per hit inside a Redis pipeline, executed once at the end (batched).
- `indexZeekDNSHit(ctx, pipe, source) bool`: **only indexes docs where `data_stream.dataset == "zeek.dns"`**, silently skipping everything else — the entity cache is DNS-only despite generic key naming. Parses `@timestamp` (RFC3339Nano, falls back to `time.Now()`); normalizes `dns.question.name` via `util.NormalizeDomain` (skips the hit entirely if empty). Writes up to 3 sorted sets per hit: `dns:name:<domain>`, `dns:ip:<resolved_ip>` (per resolved IP), `ip:seen:<src_ip>`. Each write does `ZAdd` → `ZRemRangeByRank` (trim beyond 500) → `Expire(entityTTL)` — **TTL resets on every write**, so continuously-queried domains never expire; only stale entries roll off.

Tested by `indexer_test.go` against a real `*redis.Client` pointed at an in-process `miniredis` server (`github.com/alicebob/miniredis/v2`) rather than a fake interface — exercises the actual pipeline/`ZAdd`/`ZRemRangeByRank`/`Expire` sequence: `TestIndexSearchResultOnlyIndexesZeekDNSDataset`, `TestIndexZeekDNSHitNormalizesDomain`, `TestIndexZeekDNSHitPopulatesResolvedIPAndSourceIP`, `TestIndexZeekDNSHitInvalidTimestampFallsBackToNow`, `TestIndexZeekDNSHitTrimsToMaxEntityHistory`, `TestIndexZeekDNSHitAppliesAndRefreshesTTL` (confirms the TTL-reset-on-every-write gotcha above), `TestIndexTypedSearchResultNilResponseNoop`, `TestIndexTypedSearchResultSkipsMalformedSource`, `TestIndexTypedSearchResultIndexesValidZeekDNSHit`.

### process_search.go
Implements `search_processes` against endpoint process events.

- `SearchProcessesArgs`: `Executable`/`ProcessName`/`ParentName` are **exact-match only** (no wildcards); `CommandLine` uses an analyzed `match` query (OR of tokens, not substring).
- `RegisterProcessSearchTool`: wraps handler with `recoverToolPanic` but, unlike the other three search tools, **does not** wrap with `WrapWithCache`.
- `runProcessSearch`: calls `buildProcessSearchRequest` (below) then executes it, indexes the typed response for DNS entity extraction, shapes hits, and adds `pagination` only when `From > 0` or fewer hits returned than total. On `"all shards failed"`, appends a hint to check Elastic Agent endpoint collection.
- `buildProcessSearchRequest(args SearchProcessesArgs) (*typedsearch.Request, int)` (extracted from `runProcessSearch` specifically so tests can inspect the built query without an HTTP round trip): builds a bool filter query (each set field → `term` filter; PID fields require `>0`); timestamp range via `types.NewDateRangeQuery()`. `size` default 20, cap 100 (returned alongside the request since the response shaper needs it too). `_source` restricted to a fixed allowlist. Hardcoded index pattern `logs-endpoint.events.process-*` is applied by the caller, not this function. Sort is always `@timestamp` desc.
- `shapeProcessResults`: unmarshals each hit's `_source`, skips nil/unmarshal-error hits, always returns a non-nil (possibly empty) slice.

Tested by `process_search_test.go`: `TestBuildProcessSearchRequestDefaultsAndFilters` (default size 20, every filter field, timestamp range, source includes, sort), `TestBuildProcessSearchRequestCapsSizeAndSetsFrom` (size cap 100, `From` passthrough), `TestBuildProcessSearchRequestIgnoresNonPositivePIDs`, `TestShapeProcessResultsSkipsInvalidSources`, `TestRunProcessSearchAllShardsFailedHint` (httptest-backed, asserts the Elastic Agent hint text is appended).

### security_alerts.go
Implements `search_security_alerts` against `.alerts-security.alerts-*`.

- `SearchSecurityAlertsArgs{Query, Severity, RuleName, Host, Start, End, Size}`.
- `RegisterSecurityAlertsTool`: wrapped with `WrapWithCache(cache, "search_security_alerts", SearchSecurityEventsTTL(), ...)` — **reuses the security-events TTL getter**, no dedicated alerts TTL. Args normalized (trimmed, sized) *before* entering the cache wrapper, so cache keys are consistent across equivalent requests.
- `normalizeSecurityAlertsArgs`: `Size` default 10, max 50 (asymmetric vs. security-events' cap of 20).
- `buildSecurityAlertsRequest`: fixed source projection; **always** sorts `@timestamp` desc, even with free-text `Query` (unlike security-events, which sorts by `_score` when text is present).
- `buildSecurityAlertsQuery`: `Severity`/`RuleName` are exact `term` filters via shared `buildTermQuery` — which auto-switches to `wildcard` when the value contains `*`/`?`, so `RuleName` wildcard filtering "just works" transparently. `Host` checks both `host.name` and `host.name.keyword`. Free-text `Query` is a `query_string` in `Must` (contributes to score).
- `shapeSecurityAlertsResponse`: projects source to `alertsSourceIncludes`, builds `{_id, timestamp, rule_name, severity, message, source}` per hit.

Tested by `security_alerts_test.go`: `TestNormalizeSecurityAlertsArgs` (trimming, size default 10/cap 50), `TestBuildSecurityAlertsRequest` (size, `track_total_hits`, `_source` includes count, sort, and every filter/query field rendered into the request JSON), `TestShapeSecurityAlertsResponse` (field extraction + that unrelated source fields are dropped), `TestShapeSecurityAlertsResponseInvalidSource` (decode error path). No helper extraction was needed here — unlike `process_search.go`, the request/response building was already split into standalone functions before this test pass.

### security_search.go
Largest file — implements `search_security_events` and `export_security_events`, plus shared helpers used by `process_search.go` and `security_alerts.go`.

Key package vars: `securityTextFields` (boosted multi-match fields for free text), `highlightFields`, `summaryFallbackPaths`, `sourceIncludes`, `highlightStripper` (regex stripping `<em>`/`</em>`).

- `SearchSecurityEventsArgs{Index (required), Text, Start, End, IP, SrcIP, DstIP, MAC, Domain, URL, Dataset, Size, From}`.
- `ExportSecurityEventsArgs`: same filters + `Filepath` (required), `MaxFileMB`.
- `RegisterSecuritySearchTool`: wrapped with `WrapWithCache`. Rejects a bare index with no filters (`normalizeSecuritySearchArgs` requires ≥1 of Text/Start/End/IP/SrcIP/DstIP/MAC/Domain/URL/Dataset via `hasSecurityConstraint`) — prevents unscoped scans. `Size` default 10, cap 20. **Doc inconsistency**: the error message text omits `mac` from the listed valid constraints even though `hasSecurityConstraint` checks it.
- `RegisterExportSecurityEventsTool`: **not cached** (side-effecting file write). Uses `newProgressReporter(ctx, req)` — a no-op unless the client attached an MCP progress token, per spec.
- `buildTermQuery(field, value)` — three-way branch, used across alerts/events/process tools:
  1. IP-like field (`.ip` suffix or `ip`/`related.ip`) + value contains `/` and parses as CIDR → `query_string` query (ES `term` can't do CIDR directly).
  2. Value contains `*`/`?` → `wildcard` query.
  3. Else → plain `term` query.
- `escapeQueryStringValue`: escapes ES query_string special chars via sequential `ReplaceAll`; order-dependent (backslash first), fragile if reordered.
- `buildSecuritySort(hasText)`: `_score` + `@timestamp` desc if text present, else `@timestamp` desc only.
- `projectSource`/`lookupPath`/`assignPath`: dotted-path field projection that reconstructs a **nested** map preserving ECS structure (not a flat projection); missing paths are silently skipped.
- `buildSecuritySummary`: prefers first highlight snippet in `highlightFields` order (cleaned via `cleanSnippet`, strips `<em>` tags + collapses whitespace, 220-char cap), else falls back to `firstString` over `summaryFallbackPaths`.
- `runExportSecurityEvents`: the export implementation.
  - `ensureExportTimeout` (30 min default) for the whole op; separate `ExportBatchTimeout()` (180s default) per scroll batch, so one slow batch fails fast instead of eating the whole budget.
  - File naming: `{baseName}_{UTC YYYYMMDDTHHMMSSZ}_{seq:03d}.jsonl`, directory created via `os.MkdirAll`.
  - **Uses the ES scroll API, not `from`/`size`** — deliberately avoids `index.max_result_window` (10,000 default); `pageSize = 1000`, `scrollDuration = "2m"`.
  - **Size-based file rollover**: checked *before* writing each row (rolls when the *next* row would exceed the target size), since document sizes vary hugely by dataset.
  - Scroll cleanup (`ClearScroll`) runs in a deferred call using a **fresh `context.Background()`** (not the possibly-cancelled request ctx), so cleanup still happens after a timeout/cancellation.
  - Progress reported via the injected `reportProgress` closure before start, after each batch, and at completion.
- `buildExportSearchRequest`: distinct from the interactive request builder — no `_source` filter (full docs), no highlight, no sort (scroll doesn't need a tiebreaker).

Tested by `security_search_test.go`: constraint validation, request building (text+filters, filter-only sort, and MAC/URL/SrcIP/DstIP filters via `TestBuildSecuritySearchRequestMACURLAndDirectionalIPFilters` — confirms SrcIP/DstIP don't bleed into each other's ES fields), response shaping/highlight fallback, summary fallback, an httptest-backed end-to-end run, `buildTermQuery`'s CIDR/wildcard/plain-term branches, `escapeQueryStringValue` (`TestEscapeQueryStringValueEscapesAllSpecialChars` — every special char, plus the multi-char `&&`/`||` tokens escaped as a unit rather than per-character), and `truncateSummary`'s byte-slicing behavior (`TestTruncateSummaryUTF8Boundary` — pins the current rune-unsafe cut as a regression test rather than fixing it; a 2-byte-rune string is deliberately sized so the 217-byte cutoff lands mid-rune and produces invalid UTF-8, per the rune-unsafety gotcha above).

### tools.go
Registers `search_elastic`, `list_indices`, `cluster_health`, `lookup_domain`, `lookup_ip`; defines shared config/helpers for the whole package.

- Package config (set in `init()`, env-overridable): `maxResponseChars` (20000, `MAX_RESPONSE_CHARS`), `defaultToolTimeout` (30s, `TOOL_TIMEOUT_SECS`), `exportToolTimeout` (30 min, `EXPORT_TIMEOUT_SECS`), `exportBatchTimeout` (180s, `EXPORT_BATCH_TIMEOUT_SECS`).
- `recoverToolPanic(toolName, *error)`: standard `defer`-based panic recovery used by every handler in this package — forgetting it on a new tool means a panic there can crash the whole MCP server process.
- `normalizeSearchArgs`: for `search_elastic` — trims index, defaults freeform `Query any` to `match_all` if empty, normalizes JSON via `util.NormalizeJSON`.
- `RegisterTools(server, es)`: constructs one shared `ToolCache` and registers everything.
  - `search_elastic`: validates query JSON before sending. **Notable auto-retry**: if the ES error contains `"in order to collapse on"` (a `collapse` clause on a field without doc-values), automatically retries with `collapse` stripped (`withoutCollapse`) and adds a `note` warning about possible duplicates to the result. Also logs (not errors) a warning if `index == "*"`. Response pruned via `pruneElasticSearchResult` (strips `_shards`, `timed_out`, `max_score`, per-hit `_index`/`_type`) before caching/truncating.
  - `lookup_domain`/`lookup_ip`: Redis-only (no ES call); friendly "no history found" text if both empty.
- `pruneElasticSearchResult`, `truncateResults`/`truncateSlice`: see cross-cutting truncation note above.

Tested by `normalization_test.go` (`TestSearchElasticNormalization`, `TestSearchElasticNormalizationDefaultQuery`, `TestLookupDomainNormalization`). Note: the `lookup_domain` handler itself doesn't appear to call `util.NormalizeDomain`/`TrimSpace` on `args.Domain` before querying Redis, while the indexer normalizes domains on write — a casing/whitespace mismatch could cause false-negative lookups; worth verifying if lookups seem to miss data that was indexed.

## internal/kibana

### client.go
Minimal hand-rolled HTTP client for the Kibana REST API (no SDK).

- `Client{BaseURL, Username, Password, APIKey, Space, HTTPClient}`. `NewClient` requires non-empty `url`; **defaults `Username` to `"elastic"` if `Password` is set but `Username` isn't**. Reads `KIBANA_SPACE` env var directly (not a constructor param). Flat 30s `HTTPClient` timeout.
- `pathWithSpace(path)`: prefixes `/s/{space}` unless space is empty/`"default"`, path already starts with `/s/`, or path is a global endpoint (`/api/spaces/...`, `/api/status`).
- `DoRequest(ctx, method, path, body)`: body can be `string`, `[]byte`, or JSON-marshaled otherwise. **Sets `kbn-xsrf: true` for all non-GET/HEAD methods** (required by Kibana's CSRF protection — remember this when adding new write endpoints). Auth precedence: **API key wins over basic auth** if both set. Returns raw body + status code regardless of error status — callers must check `statusCode >= 400` themselves (different from `elasticsearch.HttpError`'s pattern of returning a Go error).

Tested by `client_test.go`: `TestPathWithSpace`, `TestDoRequest`.

### tools.go
Registers 5 thin MCP tools wrapping `Client.DoRequest`. The path/method-building logic for 4 of the 5 was extracted into standalone functions specifically to make it unit-testable without calling through `mcp.AddTool`'s handler registration.

- `kibana_api_request`: generic escape hatch — delegates normalization to `normalizeKibanaAPIRequest(args) (method, path string, err error)` (default `GET`, uppercase, trim, auto-prepend `/` to path, reject empty path), logs body at Debug level separately (keeps large bodies out of Info logs).
- `list_kibana_spaces`: `GET /api/spaces/space` (no extracted helper — no branching logic to isolate).
- `list_detection_rules`: path built by `buildListDetectionRulesPath(args) string` — `page`/`per_page` only appended if `>0`. **The jsonschema doc claims "default: 20, maximum 100" but nothing in code enforces a default or cap** — documentation oversells validation that doesn't exist; the test name (`TestBuildListDetectionRulesPathDocumentsNoDefaultOrCap`) calls this out explicitly rather than silently encoding it.
- `get_detection_rule`: path/validation built by `buildGetDetectionRulePath(args) (string, error)` — requires at least one of `Id`/`RuleId` (errors if both empty), URL-escapes the value, **`Id` wins if both are supplied**.
- `list_agents`: path built by `buildListAgentsPath(args) string` — `page`, `perPage` (camelCase — matches Fleet's actual param naming, differs from `list_detection_rules`' snake_case), `kuery` (URL-escaped KQL).
- `formatResponse(respBody, statusCode)`: pretty-prints JSON if valid, else raw bytes (e.g. HTML error pages); prepends `"HTTP Error %d:\n"` if `statusCode >= 400`. **Always returns a nil Go error at the MCP transport level, even on HTTP 4xx/5xx** — errors are only signaled via the text content prefix, unlike most `elasticsearch` tools which return an actual `error`. Different error semantics between the two tool families is a known inconsistency.

Tested by `tools_test.go` (in addition to `client_test.go`'s `Client`-level tests): `TestNormalizeKibanaAPIRequest` (defaulting/uppercasing/trimming/empty-path rejection), `TestBuildListDetectionRulesPathDocumentsNoDefaultOrCap`, `TestBuildGetDetectionRulePath` (missing-both error, per-field URL-escaping, `Id`-wins-over-`RuleId` precedence), `TestBuildListAgentsPath`, `TestFormatResponse` (JSON pretty-print, plain-text passthrough, HTTP-error prefix with nil Go error). Handler registration itself (`mcp.AddTool` wiring) remains untested — only the extracted pure helpers are.

## internal/llmobs

### hooks.go
Single-purpose observability shim over the `goai` LLM SDK.

- `Hooks() []goai.Option`: `WithOnRequest` logs `"LLM request"` (model, message_count, tool_count) before each call; `WithOnResponse` logs `"LLM response error"` or `"LLM response"` (latency, finish_reason, full token usage breakdown) after.
- **Documented gotcha (top-of-file comment)**: goai's tool-level hooks (`OnToolCall`, etc.) never fire, because the `goai.Tool` values built elsewhere have no `Execute` closure — tool dispatch happens manually in `internal/agent`'s `Engine.callTool` (direct `MCPClient.CallTool` calls). Anyone wanting tool-call observability needs to instrument that dispatch site, not this file.

Used from `internal/agent`'s `Engine.Turn` (and thus by both `cmd/cli` and `internal/webui`, which no longer call `goai.GenerateText` directly). No test file.

## internal/util

### logging.go
Shared filename/log-level config for the CLI client and MCP server binaries (in `cmd/`, outside `internal/`).

- Constants: `DefaultClientLogFile`, `DefaultServerLogFile`, `DefaultServerLockFile`, `DefaultClientHistoryFile`.
- `ClientLogFile()`/`ServerLogFile()`/`ServerLockFile()`: env-override pattern, fallback to constants. `ClientHistoryFile()`: `CLIENT_HISTORY_FILE` env override, else `$HOME/.elastic-cli-history`, else bare relative filename if home lookup fails.
- `logLevelFromEnv`: case-insensitive `info`/`debug`/`warn(ing)`/`error`, unrecognized values silently default to `Info` (no error).
- `TruncateForLog(s, max)`: byte-slice truncation with `"...(truncated)"` suffix — see rune-unsafety note above.
- `ClientPayloadLoggingEnabled()`: opt-in via `CLIENT_LOG_PAYLOADS`, default false; gates verbose Debug-level argument logging in `webui/server.go`.

No test file.

### normalization.go
JSON/domain normalization, mostly correcting common LLM-generated malformed input.

- `NormalizeJSON(s)`: fails open (returns `s` unchanged) if invalid JSON; else recursively fixes quoted keys via `fixQuotedKeys` and re-marshals (minifies).
- `fixQuotedKeys`: strips a leading/trailing escaped/literal quote from object keys — corrects LLM output like `"\"@timestamp\""` → `"@timestamp"`. Recurses into map values and array elements.
- `StringifyJSON(input)`: `nil` → `""`; string input is passed through `NormalizeJSON`; other types are `json.Marshal`ed, falling back to `fmt.Sprintf("%v", input)` on marshal error (not valid JSON in that fallback case, but unlikely to trigger since tool args are typically already JSON-decoded).
- `NormalizeDomain(domain)`: lowercase, trim, strip a single trailing dot. Used by the DNS indexer; **not** confirmed to be applied in the `lookup_domain` handler (see note in `tools.go` section above).

Tested by `util/normalization_test.go` (`TestNormalizeJSON`, `TestNormalizeDomain`) and indirectly by `elasticsearch/normalization_test.go`.

### retry.go
Generic retry-with-backoff, scoped specifically to LLM provider rate-limit errors (not ES retries).

- `IsRateLimitError(err)`: nil-safe substring match (case-insensitive) against `"429"`, `"rate limit"`, `"too many requests"`, `"overloaded"` — string-based, so a provider phrasing rate-limits differently (e.g. "quota exceeded") won't be retried.
- `WithRetry[T any](ctx, fn)`: `maxRetries = 5`, backoff starts at 2s and **doubles each retry** (2s→32s, up to ~62s total sleep). Only retries on `IsRateLimitError`; anything else returns immediately. Respects `ctx.Done()` during sleep. Makes one final attempt after exhausting retries (actual max attempts = 6).

Used by `internal/agent`'s `Engine.Turn` around every `goai.GenerateText` call (and thus by both `cmd/cli` and `internal/webui`). No dedicated test file.

## internal/webui

### server.go
WebSocket-based chat UI server. Drives `internal/agent`'s `Engine.Turn` and translates its events into `WebMessage`s over the connection; serves static assets via `embed.FS`.

- `//go:embed assets/*` bundles the frontend into the binary; served via `http.FileServer(http.FS(assetFS))` at `/`.
- `upgrader.CheckOrigin`: allows empty Origin (non-browser clients) or origins starting with `http(s)://localhost`/`http(s)://127.0.0.1` — **hardcoded local-only**; exposing this server to non-local browser origins requires a code change.
- `WebMessage{Type, Content, Model, Thinking, Tool *ToolEvent}` — protocol envelope; `Type` ∈ `setup|user|assistant|system|error|status|tool|clear_status|reset`.
- `ToolEvent{ID, Seq, Name, State, Args, Result, IsError, IsCached, IsStored}` — `State` transitions `running` → `completed`/`error`.
- `Server{engine *agent.Engine, modelName, useMemory}` — no longer holds `mcpClient`/`llmModel`/`tools` directly; those are bundled once into the `Engine` at construction.
- `RunServer(ctx, mcpClient, model, tools, modelName, port, useMemory)`: builds `agent.New(mcpClient, model, tools, modelName)` once, mounts assets at `/` and WebSocket at `/ws`; a goroutine watches `ctx.Done()` and calls `server.Shutdown` for graceful shutdown.
- `handleWebSocket`: per-connection loop. `history []provider.Message` is **scoped to the single connection** — no cross-reconnect persistence. `"reset"` clears history. `"/memory"` as literal user content is a special client-side command intercepted server-side (dumps `agent.RenderHistoryText(history)`, or a disabled message if `useMemory` is false). Disconnects (any `ReadMessage` error, no distinction between clean close and network error) just log and break.
- `processConversation(ctx, conn, history *[]provider.Message)`: now a thin wrapper — calls `s.engine.Turn(ctx, *history, func(ev agent.Event) { s.emitEvent(conn, ev) })`, writes the result back to `*history`, sends an `"error"` WebMessage if `result.Err != nil`, then always sends `"clear_status"`. The stall-detection/retry/sequential-tool-execution logic that used to live here is now in `agent.Engine.Turn` (see `internal/agent`).
- `emitEvent(conn, ev agent.Event)`: the event-to-WebMessage translator — `EventStatus` → `{"status", Thinking: true}`; `EventToolStart`/`EventToolEnd` → `{"tool", Tool: &ToolEvent{...}}` (both event kinds map to the same `ToolEvent` shape, just with `State` differing); `EventAssistant` → `{"assistant", Content}`. This runs synchronously inside the same goroutine `Turn` is already blocking in, so it can call `sendMessage` directly with no extra buffering — unlike the CLI, which needs a channel bridge (see `cmd/IMPL.md`'s cross-cutting section).

Tested by `server_test.go`: origin check, connection handshake, `WebMessage`/`ToolEvent` JSON round-trips, fresh-history-per-connection, and the `reset` command. (`summarizeToolCalls`/`extractToolText` tests moved to `internal/agent/agent_test.go` along with the functions themselves.)

### assets/
Static frontend, bundled via the `//go:embed` in `server.go`.

- `index.html`: SPA shell — status chips (connection/session/model/cache stats), "New Session" button, conversation panel, tool-call sidebar, input form. Loads `marked.min.js` from a CDN for Markdown rendering.
- `app.js` (~16KB): WebSocket client logic — connects to `/ws`, local input history in `localStorage` (`webui-history` key, up/down arrow navigation), renders `WebMessage`s (Markdown for assistant text, tool-call cards keyed by `tool.id`/`seq`, capped at `maxTools = 24` displayed).
- `style.css` (~15KB): dark-themed, CSS-custom-property-driven design system (blue/cyan/violet/amber/red/green accents) for the chat panels, status chips, and tool-call cards.
