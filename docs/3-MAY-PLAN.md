# 3-MAY Plan — Specialized tool + cache improvements

Derived from `elastic-mcp-server.log` review (2026-05-03 session, 420 lines).

## Evidence

- Single investigation issued **14 `search_elastic` calls all filtered on `source.ip:192.168.3.122`** across zeek.dhcp / zeek.ssl / zeek.connection / zeek.dns / zeek.http / zeek.ssh / zeek.known_hosts / suricata.eve. Same shape (5 calls) for `zeek.ssl.server.name:graph.facebook.com`.
- Cache hit ratio in that session: **1 / 34** lookups.
- `hits:10000` capped 14 times (no `track_total_hits`).
- `lookup_domain("graph.facebook.com")` returned 0 despite 4 prior zeek.ssl searches finding 165 SNI hits — `indexZeekDNSHit` (`internal/elasticsearch/indexer.go:107`) is gated to `data_stream.dataset == "zeek.dns"`, so all other hits flow through the indexer and get dropped.

---

## 1. New tool: `profile_entity`

**Goal:** collapse the entity-pivot burst into one MCP call backed by one Elasticsearch `_msearch`.

### Args
```go
type ProfileEntityArgs struct {
    Entity string `json:"entity" jsonschema:"IP, MAC, or domain to profile"`
    Kind   string `json:"kind,omitempty" jsonschema:"Optional: ip|mac|domain. Inferred if omitted."`
    Start  string `json:"start,omitempty" jsonschema:"RFC3339 lower bound (default now-24h)"`
    End    string `json:"end,omitempty" jsonschema:"RFC3339 upper bound"`
    Depth  string `json:"depth,omitempty" jsonschema:"summary|full (default summary)"`
}
```

### Bundled queries (single `_msearch`)
| Index pattern              | Aggregation / shape                                            |
|----------------------------|----------------------------------------------------------------|
| `logs-zeek.connection-*`   | top_dst_ips, dst_ports, protocols, bytes_in/out sums           |
| `logs-zeek.dns-*`          | top_queries terms                                              |
| `logs-zeek.ssl-*`          | top_sni + tls.version + ja4 + established/resumed counts       |
| `logs-zeek.http-*`         | top user_agent.original, top url.domain, status histogram      |
| `logs-zeek.ssh-*`          | client/server software, auth_attempts, success/failure         |
| `logs-suricata.eve-*`      | event.type=alert: rule.category, rule.name, severity buckets   |
| `logs-zeek.dhcp-*`         | host.hostname, mac, fingerprint (when kind=ip|mac)             |
| `logs-zeek.known_hosts-*`  | first/last seen                                                |

For `kind=domain`, swap `source.ip` filters for SNI/DNS/URL term filters.

### Output shape
```json
{
  "entity": "192.168.3.122",
  "kind": "ip",
  "window": {"start": "...", "end": "..."},
  "connection": {...},
  "dns": {...},
  "ssl": {...},
  "http": {...},
  "ssh": {...},
  "alerts": {...},
  "dhcp": {...},
  "known_hosts": {...}
}
```

### Cache
- Key: SHA256 of `("profile_entity", entity_normalized, kind, bucketed_window)` where relative anchors (`now-24h`) are bucketed to 5-min floors.
- TTL: reuse `SearchSecurityEventsTTL()` (600s).

### Files to touch
- New: `internal/elasticsearch/profile_entity.go`
- Wire into `RegisterTools` (`internal/elasticsearch/tools.go:151`)
- Add MSearch helper in `internal/elasticsearch/client.go` if not present

### Acceptance
- One `profile_entity` call replaces the 14-call burst observed in the log (lines 128–197).
- Total wall-clock < 2× the slowest single component query (current p95 in log: 3.7s on connection rollup at line 146 — concerning, investigate).

---

## 2. New tool: `search_alerts`

Typed analogue of `search_security_events`, scoped to `logs-suricata.eve-*` with `event.type=alert` always applied.

### Args
```go
type SearchAlertsArgs struct {
    Index    string `json:"index,omitempty" jsonschema:"Default logs-suricata.eve-*"`
    Severity int    `json:"severity,omitempty"`
    Category string `json:"category,omitempty"`
    Rule     string `json:"rule,omitempty"`
    SrcIP    string `json:"src_ip,omitempty"`
    DstIP    string `json:"dst_ip,omitempty"`
    Start    string `json:"start,omitempty"`
    End      string `json:"end,omitempty"`
    Size     int    `json:"size,omitempty"`
}
```

Reuses helpers in `internal/elasticsearch/security_search.go` (`buildAnyTermFilter`, `buildTimestampFilter`).

---

## 3. Indexer: cover the rest of the datasets

Currently `indexZeekDNSHit` (`internal/elasticsearch/indexer.go:102`) is the only writer. Dispatch on `data_stream.dataset`.

### New Redis keys (sorted sets, score = `@timestamp` ms, capped via `ZRemRangeByRank`, TTL 24h)

| Source                   | Key                            | Member JSON                                              |
|--------------------------|--------------------------------|----------------------------------------------------------|
| `zeek.ssl`               | `tls:sni:<server_name>`        | `{ts, src, dst, ja4, version, established, resumed}`     |
| `zeek.ssl`               | `tls:ja4:<hash>`               | `{ts, sni, dst, src}`                                    |
| `zeek.connection`        | `flow:src:<ip>`                | `{ts, dst, dport, proto, bytes_in, bytes_out, state}`    |
| `zeek.connection`        | `flow:dst:<ip>`                | `{ts, src, sport, proto}`                                |
| `suricata.eve` (alert)   | `alert:rule:<rule.name>`       | `{ts, src, dst, severity, category}`                     |
| `suricata.eve` (alert)   | `alert:src:<ip>`               | `{ts, rule, severity}`                                   |
| `zeek.dhcp`              | `dhcp:mac:<mac>`               | `{ts, hostname, assigned_ip, fingerprint}`               |

### New lookup tools (cheap to add once index is populated)
- `lookup_sni(name)` → recent flows for SNI
- `lookup_ja4(hash)` → recent SNIs + dst IPs for a TLS fingerprint
- `lookup_alerts(ip|rule)` → recent Suricata alerts

### Files
- `internal/elasticsearch/indexer.go` — add `indexZeekSSLHit`, `indexZeekConnectionHit`, `indexSuricataAlertHit`, `indexZeekDHCPHit`; update entrypoint to dispatch.
- `internal/elasticsearch/cache.go` — add `LookupSNI`, `LookupJA4`, `LookupAlerts` methods.
- `internal/elasticsearch/tools.go` — register new MCP tools.

---

## 4. Cache: stabilize keys

### 4a. Bucket relative time anchors
Two consecutive cache misses in the log (`d2015aa4` vs `c6fe8739`) are the same intent expressed once with explicit RFC3339 and once with `now-24h`. Pre-key transform:

```go
// in cache.go cacheKey()
// Replace `now-Nh` / `now-Nm` with floor(now, 5min) - duration, RFC3339, before hashing.
```

Apply only to `search_security_events` and `profile_entity` — `search_elastic` query bodies are user-provided JSON, leave them.

### 4b. Sort JSON keys recursively in `util.NormalizeJSON`
Verify it does this (a single un-sorted nested object kills the hit rate). If not, add recursive marshal-with-sorted-keys.

### 4c. Move cache marker out of the payload
`cache.go:163` and `cache.go:178` prepend `✓ ` / `↓ ` to the text content. This mutates the JSON returned to the model and any downstream parser sees garbage at offset 0. Move to a result extra-field or strip on read.

---

## 5. Cache: store mappings

### `describe_index` tool
- Calls `_field_caps` for an index pattern, returns `{field, type, searchable, aggregatable}` triples.
- Cache TTL: 24h (mappings are stable).
- Solves the `event.type` vs `suricata.eve.event_type` confusion seen across earlier sessions.

### `datasets:available`
Derived list extracted from `list_indices` results, written on every successful list call. Lets `profile_entity` skip an index discovery step.

---

## 6. Quality nits found while reading

- `track_total_hits` — set to `true` by default in `search_security_events` (already done) and in `profile_entity`. Document for `search_elastic` users in the tool description so totals beyond 10 000 are honest.
- 3.7 s `zeek.connection-*` aggregation at log line 146 — investigate index size / shard count. Likely needs a runtime metric, not a code fix yet.
- `ensureToolTimeout` (`tools.go:53`) discards the `cancel` func with `_ = cancel` — leaks the timer until the deadline fires. Hold the cancel and `defer cancel()` in callers, or return both.

---

## Order of work

1. Indexer dispatch + new keys (Section 3) — small, unblocks lookup-style tools and improves cache utility immediately.
2. `profile_entity` (Section 1) — biggest user-visible win.
3. Cache key stabilization (Section 4a, 4c) — unblocks measurable hit-rate improvement on top of #2.
4. `describe_index` + `datasets:available` (Section 5).
5. `search_alerts` (Section 2).
6. Quality nits (Section 6).
