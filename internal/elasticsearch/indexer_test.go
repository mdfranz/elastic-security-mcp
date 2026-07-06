package elasticsearch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	typedsearch "github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
	"github.com/redis/go-redis/v9"
)

func newTestRedisClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { client.Close() })
	return client, s
}

func zeekDNSHit(dataset, domain, ts, srcIP string, resolvedIPs []string) map[string]interface{} {
	source := map[string]interface{}{
		"@timestamp":  ts,
		"data_stream": map[string]interface{}{"dataset": dataset},
		"dns": map[string]interface{}{
			"question": map[string]interface{}{"name": domain},
		},
	}
	if srcIP != "" {
		source["source"] = map[string]interface{}{"ip": srcIP}
	}
	if len(resolvedIPs) > 0 {
		ips := make([]interface{}, len(resolvedIPs))
		for i, ip := range resolvedIPs {
			ips[i] = ip
		}
		source["dns"].(map[string]interface{})["resolved_ip"] = ips
	}
	return map[string]interface{}{"_source": source}
}

func rawSearchResult(hits ...map[string]interface{}) map[string]interface{} {
	hitList := make([]interface{}, len(hits))
	for i, h := range hits {
		hitList[i] = h
	}
	return map[string]interface{}{
		"hits": map[string]interface{}{
			"hits": hitList,
		},
	}
}

func TestIndexSearchResultOnlyIndexesZeekDNSDataset(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	result := rawSearchResult(
		zeekDNSHit("zeek.dns", "example.com", "2026-01-01T00:00:00.000Z", "10.0.0.9", []string{"1.2.3.4"}),
		zeekDNSHit("aws.cloudtrail", "should-not-index.com", "2026-01-01T00:00:00.000Z", "10.0.0.9", []string{"9.9.9.9"}),
	)

	indexSearchResult(ctx, client, result)

	if !s.Exists("dns:name:example.com") {
		t.Error("expected dns:name:example.com to be indexed")
	}
	if s.Exists("dns:name:should-not-index.com") {
		t.Error("non-zeek.dns hit must not be indexed under dns:name:should-not-index.com")
	}
	if s.Exists("dns:ip:9.9.9.9") {
		t.Error("non-zeek.dns hit's resolved IP must not be indexed")
	}
}

func TestIndexZeekDNSHitNormalizesDomain(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	result := rawSearchResult(
		zeekDNSHit("zeek.dns", "Example.COM.", "2026-01-01T00:00:00.000Z", "", nil),
	)

	indexSearchResult(ctx, client, result)

	if !s.Exists("dns:name:example.com") {
		t.Error("expected domain to be normalized (lowercased, trailing dot stripped) before indexing")
	}
	if s.Exists("dns:name:Example.COM.") {
		t.Error("unnormalized domain key must not exist")
	}
}

func TestIndexZeekDNSHitPopulatesResolvedIPAndSourceIP(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	result := rawSearchResult(
		zeekDNSHit("zeek.dns", "example.com", "2026-01-01T00:00:00.000Z", "10.0.0.9", []string{"1.2.3.4", "5.6.7.8"}),
	)

	indexSearchResult(ctx, client, result)

	for _, key := range []string{"dns:ip:1.2.3.4", "dns:ip:5.6.7.8", "ip:seen:10.0.0.9"} {
		members, err := s.ZMembers(key)
		if err != nil {
			t.Fatalf("ZMembers(%s) error: %v", key, err)
		}
		if len(members) != 1 {
			t.Fatalf("ZMembers(%s) = %v, want exactly 1 member", key, members)
		}
	}

	var ipRec ipRecord
	members, _ := s.ZMembers("dns:ip:1.2.3.4")
	if err := json.Unmarshal([]byte(members[0]), &ipRec); err != nil {
		t.Fatalf("unmarshal ip record: %v", err)
	}
	if ipRec.Domain != "example.com" || ipRec.Type != "dns_answer" {
		t.Errorf("dns:ip record = %#v, want domain=example.com type=dns_answer", ipRec)
	}

	members, _ = s.ZMembers("ip:seen:10.0.0.9")
	if err := json.Unmarshal([]byte(members[0]), &ipRec); err != nil {
		t.Fatalf("unmarshal source ip record: %v", err)
	}
	if ipRec.Domain != "example.com" || ipRec.Type != "dns_query" {
		t.Errorf("ip:seen record = %#v, want domain=example.com type=dns_query", ipRec)
	}
}

func TestIndexZeekDNSHitInvalidTimestampFallsBackToNow(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	before := time.Now()
	result := rawSearchResult(
		zeekDNSHit("zeek.dns", "example.com", "not-a-timestamp", "", nil),
	)
	indexSearchResult(ctx, client, result)
	after := time.Now()

	score, err := s.ZScore("dns:name:example.com", func() string {
		members, _ := s.ZMembers("dns:name:example.com")
		return members[0]
	}())
	if err != nil {
		t.Fatalf("ZScore error: %v", err)
	}

	got := time.UnixMilli(int64(score))
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("fallback timestamp score = %v, want it close to now (between %v and %v)", got, before, after)
	}
}

func TestIndexZeekDNSHitTrimsToMaxEntityHistory(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	const overflow = 20
	hits := make([]map[string]interface{}, 0, maxEntityHistory+overflow)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxEntityHistory+overflow; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		hits = append(hits, zeekDNSHit("zeek.dns", "example.com", ts, "", nil))
	}

	indexSearchResult(ctx, client, rawSearchResult(hits...))

	members, err := s.ZMembers("dns:name:example.com")
	if err != nil {
		t.Fatalf("ZMembers error: %v", err)
	}
	if len(members) != maxEntityHistory {
		t.Fatalf("len(members) = %d, want trimmed to %d", len(members), maxEntityHistory)
	}
}

func TestIndexZeekDNSHitAppliesAndRefreshesTTL(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	result := rawSearchResult(
		zeekDNSHit("zeek.dns", "example.com", "2026-01-01T00:00:00.000Z", "", nil),
	)
	indexSearchResult(ctx, client, result)

	ttl := s.TTL("dns:name:example.com")
	if ttl <= 0 || ttl > entityTTL {
		t.Fatalf("TTL = %v, want a positive value <= %v", ttl, entityTTL)
	}

	// Simulate a near-expired key, then re-index the same domain: the TTL
	// must be refreshed back up rather than left to expire, per the
	// "TTL resets on every write" behavior documented in internal/IMPL.md.
	s.SetTTL("dns:name:example.com", time.Second)
	indexSearchResult(ctx, client, result)

	refreshed := s.TTL("dns:name:example.com")
	if refreshed <= time.Second {
		t.Fatalf("TTL after re-index = %v, want it refreshed above the simulated near-expiry of 1s", refreshed)
	}
}

func TestIndexTypedSearchResultNilResponseNoop(t *testing.T) {
	client, _ := newTestRedisClient(t)
	// Must not panic on a nil response.
	indexTypedSearchResult(context.Background(), client, nil)
}

func TestIndexTypedSearchResultSkipsMalformedSource(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	id := "doc-1"
	resp := &typedsearch.Response{
		Hits: types.HitsMetadata{
			Hits: []types.Hit{
				{Id_: &id, Source_: json.RawMessage(`not json`)},
				{}, // empty source, also skipped
			},
		},
	}

	// Must not panic despite malformed/empty source documents.
	indexTypedSearchResult(ctx, client, resp)

	if len(s.Keys()) != 0 {
		t.Fatalf("keys = %v, want none written for malformed/empty sources", s.Keys())
	}
}

func TestIndexTypedSearchResultIndexesValidZeekDNSHit(t *testing.T) {
	client, s := newTestRedisClient(t)
	ctx := context.Background()

	id := "doc-1"
	source, _ := json.Marshal(map[string]interface{}{
		"@timestamp":  "2026-01-01T00:00:00.000Z",
		"data_stream": map[string]interface{}{"dataset": "zeek.dns"},
		"dns": map[string]interface{}{
			"question": map[string]interface{}{"name": "example.com"},
		},
	})
	resp := &typedsearch.Response{
		Hits: types.HitsMetadata{
			Hits: []types.Hit{
				{Id_: &id, Source_: source},
			},
		},
	}

	indexTypedSearchResult(ctx, client, resp)

	if !s.Exists("dns:name:example.com") {
		t.Error("expected typed search result to index the zeek.dns hit")
	}
}
