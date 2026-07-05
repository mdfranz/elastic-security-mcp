# Known Issues / Observations

## Test coverage improvement plan from cmd/ and internal/ implementation notes

**Status:** Planned.

Reviewing `cmd/IMPL.md` and `internal/IMPL.md` shows several high-value areas
where targeted tests would reduce regression risk without needing broad
integration coverage. The common theme is that important behavior is currently
documented in implementation notes but either untested, only indirectly tested,
or duplicated across packages.

### Priority 1: shared agent loop behavior

The CLI TUI (`cmd/cli/main.go`) and Web UI (`internal/webui/server.go`) now
drive the shared `internal/agent.Engine`. Add focused tests around the engine
contract and the two UI adapters so future prompt or event-flow changes do not
regress either front end:

- `Engine.Turn` stalling behavior: verify the `"i will"`, `"let me"`,
  `"now i'll"`, and `"searching"` heuristics trigger a corrective user message
  only when there are no tool calls, and add an iteration cap or testable guard
  before changing this path.
- Cache-marker parsing: verify `"✓ "` and `"↓ "` prefixes are stripped and
  converted into cached/stored flags on `agent.ToolCallEvent`.
- Tool text extraction: extend the current nil test to cover multiple MCP text
  blocks joined with newlines and mixed non-text content.
- History rendering/summarization: verify user, assistant, and tool messages are
  rendered consistently so `/memory`, logs, and UI state do not drift.
- UI event adapters: test Web UI `emitEvent` and CLI `handleAgentEvent` mapping
  from `agent.Event` to protocol/TUI state without invoking an LLM or MCP server.

### Priority 2: untested Elasticsearch tool handlers

Several tool handlers encode important query semantics but have no dedicated
tests. Add table-driven unit tests using request-building helpers where possible
and `httptest` only where response/error behavior matters:

- `search_processes`: exact-match filters for executable/process/parent names,
  analyzed `command_line` match behavior, PID filters only when positive, size
  default/cap, pagination metadata, and the `"all shards failed"` hint.
- `search_security_alerts`: size default/cap, severity/rule/host filters,
  wildcard rule-name behavior through `buildTermQuery`, query-string `must`
  clause, fixed timestamp sort, and shaped response fields.
- `search_elastic`: invalid JSON validation, default `match_all`, warning path
  for index `"*"`, collapse-removal retry behavior, and response pruning before
  truncation/caching.
- `lookup_domain` / `lookup_ip`: confirm input normalization expectations and
  Redis miss text, especially domain case/whitespace/trailing-dot behavior.

### Priority 3: Redis DNS entity indexer

`internal/elasticsearch/indexer.go` has no dedicated tests even though it feeds
`lookup_domain` and `lookup_ip` as a side effect of searches. Add focused tests
with a Redis-compatible fake or miniredis-style dependency:

- Only `data_stream.dataset == "zeek.dns"` documents are indexed.
- Domain names are normalized before writing keys.
- DNS answers populate `dns:ip:<ip>` and source IPs populate `ip:seen:<ip>`.
- Invalid or missing timestamps fall back safely without skipping otherwise
  useful records.
- Sorted sets are trimmed to `maxEntityHistory` and TTLs are applied.

### Priority 4: Kibana tool wrapper semantics

`internal/kibana/client_test.go` covers the client, but the MCP tool wrappers
in `internal/kibana/tools.go` are not directly tested. Add handler-level tests
with a fake Kibana server:

- `kibana_api_request` method/path normalization, JSON body forwarding, and
  non-GET `kbn-xsrf` behavior.
- `list_detection_rules` query parameters, including the documented default/cap
  mismatch so the current behavior is explicit.
- `get_detection_rule` validation when neither `id` nor `rule_id` is supplied,
  plus URL escaping for both variants.
- `list_agents` camelCase Fleet query params and `kuery` escaping.
- `formatResponse` behavior for JSON, plain text/HTML, and HTTP 4xx/5xx text
  error prefixes.

### Priority 5: utility edge cases and timeout/retry behavior

Small utility functions carry cross-package assumptions and are cheap to test:

- `util.TruncateForLog` and `truncateSummary`: add UTF-8/rune-boundary tests to
  expose current byte-slicing behavior before changing it.
- `util.WithRetry`: retry only rate-limit-like errors, stop immediately for
  non-rate-limit errors, respect context cancellation during backoff, and confirm
  the effective attempt count.
- Timeout helpers: confirm existing deadlines are preserved and defaults are
  applied only when no caller deadline exists.
- `escapeQueryStringValue`: cover all Elasticsearch query-string special
  characters so future reorderings do not break escaping.

### Priority 6: command entrypoint pure behavior

Some command-package behavior can be covered without spawning the MCP server or
running a TUI:

- `modelProvider`: known prefixes and unsupported-prefix errors.
- Input history navigation: consecutive duplicate suppression, draft restore,
  and boundary behavior.
- `/memory` and `/export` command routing at the pure-helper level where
  feasible.
- `formatToolCallArguments`: nested JSON-string formatting and malformed nested
  JSON fallback.
- Server lock-file messaging: isolate stale/live PID interpretation into a
  helper before adding tests, if the code is touched.

---

## Architecture document quick wins

**Status:** Planned.

`ARCHITECTURE.md` is mostly aligned with the current `internal/agent` refactor,
but a few stale or low-effort items would make it safer as a contributor guide:

- Remove stale duplication language in Design Principles. The current code uses
  `internal/agent.Engine`; the "Known duplication" bullet still says the
  agentic loop, `systemPrompt`, and helper functions are independently
  implemented in `cmd/cli` and `internal/webui`.
- Fix the shared-logic table entry for `internal/webui`, which currently says it
  implements the same agentic loop as the CLI "independently"; it should say it
  adapts `agent.Event`s to WebSocket messages.
- Reconcile `cmd/IMPL.md` and `internal/IMPL.md` with `ARCHITECTURE.md`. Both
  implementation guides still describe the pre-refactor duplicated loop and
  `webui.processConversation` as the direct tool-calling loop.
- Document `internal/agent` in `internal/IMPL.md`: `Engine.Turn`,
  `SystemPrompt`, event kinds, cache-prefix normalization, stall correction, and
  the no-iteration-cap risk.
- Update the agent-loop quick-win from "deduplicate loops" to "add a bounded
  stall retry / max turn count" since the core loop has already been unified.
- Clarify tool-call observability in architecture and IMPL docs: instrumentation
  should happen in `internal/agent.callTool`, not in `internal/webui` or
  `llmobs` tool hooks.
- Add a short "architecture drift checklist" for future changes: when tools,
  prompt text, event protocol, cache markers, or timeout semantics change,
  update `ARCHITECTURE.md`, `cmd/IMPL.md`, and `internal/IMPL.md` together.

Detailed test planning lives in `docs/TEST-COVERAGE-PLAN.md`.

---

## export_security_events: slow scroll batches on large-document indexes

**Status:** Mitigated (batch timeout raised 60s → 180s), root cause not fully diagnosed.

When exporting `logs-network_traffic.tls-*` (full TLS records, including cert
chains — much larger documents than DNS/SSL summary events), one scroll
continuation call exceeded the 60s per-batch timeout after 17 consecutive
batches had each completed in 6-8s:

```
scroll error after 17000 rows (batch 17): context deadline exceeded
```

Batches 1-16 were consistently fast (~7s each); batch 18's scroll call alone
stalled well past 60s with no other symptoms (no server error, no partial
response). Possible causes not yet ruled out:
- Elasticsearch-side scroll context churn/GC on a large working set
- Transient network slowness over the Tailscale-tunneled connection to the
  cluster
- Larger documents in that particular page (TLS docs vary a lot in size —
  SAN list length, cert chain depth)

**Mitigation applied:** raised `exportBatchTimeout` default from 60s to 180s
(`EXPORT_BATCH_TIMEOUT_SECS` env var still overrides). This gives slow
batches more headroom while still failing fast (well under the 30-minute
overall export timeout) if a scroll genuinely hangs.

**Follow-up if this recurs:** capture the timing of every batch (not just
completed ones) to see whether slow batches correlate with document size,
time-of-day/cluster load, or are randomly distributed — would help
distinguish "just slow" from "actually stuck."

---

## MCP progress notifications: sent successfully, but not surfaced to the agent

**Status:** Observed behavior, not a bug in this codebase — documenting for
future reference when working with the Claude Code MCP client.

`export_security_events` was instrumented to send `notifications/progress`
messages (via `ServerSession.NotifyProgress`) once per scroll batch, gated on
the calling client having attached a `progressToken` to the tool call
request (per MCP spec, servers should only send these if asked).

Confirmed via testing against Claude Code's MCP client:

- The client **does** attach a progress token to tool calls (observed
  `token: 2` in server logs).
- The server successfully sent 17 progress notifications over a ~2-minute
  tool call, one every ~7 seconds, with **zero send failures**
  (`NotifyProgress` never returned an error).
- **None of those 17 notifications appeared in the calling agent's
  transcript.** The agent only received the single final tool result once
  the call completed (in this case, an error after the batch-18 timeout).

So the protocol-level plumbing works end-to-end (client requests updates →
server sends updates → transport delivers them), but whatever Claude Code
does with progress notifications on the receiving end does not inject them
into the model's context mid-call. They may be used for a UI-level indicator
(e.g. a spinner or percentage) that isn't wired into the conversation, or
simply not surfaced at all in this integration.

**Implication:** don't rely on MCP progress notifications as a way to give
a Claude Code agent mid-call visibility into a long-running tool. Use
faster/smaller batch timeouts and clear error messages instead, so a stuck
call fails observably rather than hanging — which is the approach taken for
`export_security_events` (see above).
