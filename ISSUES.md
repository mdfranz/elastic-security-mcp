# Known Issues / Observations

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
