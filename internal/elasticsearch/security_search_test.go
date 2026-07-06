package elasticsearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	esv8 "github.com/elastic/go-elasticsearch/v9"
	typedsearch "github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
)

func TestNormalizeSecuritySearchArgsRequiresConstraint(t *testing.T) {
	_, err := normalizeSecuritySearchArgs(SearchSecurityEventsArgs{Index: "logs-*"})
	if err == nil {
		t.Fatal("expected constraint validation error")
	}
}

func TestBuildSecuritySearchRequestTextAndFilters(t *testing.T) {
	req := buildSecuritySearchRequest(SearchSecurityEventsArgs{
		Index:   "logs-*",
		Text:    "example.org 1.2.3.4",
		Start:   "2026-01-01T00:00:00Z",
		End:     "2026-01-02T00:00:00Z",
		IP:      "1.2.3.4",
		Domain:  "example.org",
		Dataset: "zeek.dns",
		Size:    7,
	})

	if req.Size == nil || *req.Size != 7 {
		t.Fatalf("unexpected size: %#v", req.Size)
	}
	if req.Query == nil || req.Query.Bool == nil {
		t.Fatal("expected bool query")
	}
	if len(req.Query.Bool.Must) != 1 {
		t.Fatalf("expected one must clause, got %d", len(req.Query.Bool.Must))
	}
	if req.Query.Bool.Must[0].MultiMatch == nil {
		t.Fatal("expected multi_match in must clause")
	}
	if got := req.Query.Bool.Must[0].MultiMatch.Fields[0]; got != "dns.question.name^10" {
		t.Fatalf("unexpected top boost field: %s", got)
	}
	if len(req.Query.Bool.Filter) != 4 {
		t.Fatalf("expected four filters, got %d", len(req.Query.Bool.Filter))
	}

	rangeFilter := req.Query.Bool.Filter[0]
	dateRange, ok := rangeFilter.Range["@timestamp"].(*types.DateRangeQuery)
	if !ok {
		t.Fatalf("expected date range filter, got %#v", rangeFilter.Range["@timestamp"])
	}
	if dateRange.Gte == nil || *dateRange.Gte != "2026-01-01T00:00:00Z" {
		t.Fatalf("unexpected gte: %#v", dateRange.Gte)
	}
	if dateRange.Lte == nil || *dateRange.Lte != "2026-01-02T00:00:00Z" {
		t.Fatalf("unexpected lte: %#v", dateRange.Lte)
	}

	if len(req.Sort) != 2 {
		t.Fatalf("expected score and timestamp sorts, got %d", len(req.Sort))
	}
	if req.Highlight == nil || len(req.Highlight.Fields) != len(highlightFields) {
		t.Fatalf("unexpected highlight config: %#v", req.Highlight)
	}
}

func TestBuildSecuritySearchRequestFilterOnlySort(t *testing.T) {
	req := buildSecuritySearchRequest(SearchSecurityEventsArgs{
		Index: "logs-*",
		IP:    "1.2.3.4",
	})

	if req.Query == nil || req.Query.Bool == nil {
		t.Fatal("expected bool query")
	}
	if len(req.Query.Bool.Must) != 0 {
		t.Fatalf("expected no must clauses, got %d", len(req.Query.Bool.Must))
	}
	if len(req.Sort) != 1 {
		t.Fatalf("expected one sort, got %d", len(req.Sort))
	}
}

func TestShapeSecuritySearchResponsePrefersHighlights(t *testing.T) {
	score := types.Float64(42.5)
	id := "abc123"
	resp := &typedsearch.Response{
		Took: 12,
		Hits: types.HitsMetadata{
			Total: &types.TotalHits{Value: 1},
			Hits: []types.Hit{
				{
					Index_: "logs-zeek.dns-default",
					Id_:    &id,
					Score_: &score,
					Highlight: map[string][]string{
						"dns.question.name": {"matched <em>example.org</em> lookup"},
					},
					Source_: mustJSON(t, map[string]any{
						"@timestamp": "2026-01-01T10:00:00Z",
						"event": map[string]any{
							"dataset": "zeek.dns",
						},
						"message": "fallback message",
						"dns": map[string]any{
							"question": map[string]any{"name": "example.org"},
							"answers":  map[string]any{"data": []any{"1.2.3.4"}},
						},
						"source": map[string]any{"ip": "10.0.0.5"},
					}),
				},
			},
		},
	}

	out, err := shapeSecuritySearchResponse(resp, SearchSecurityEventsArgs{})
	if err != nil {
		t.Fatalf("shapeSecuritySearchResponse error: %v", err)
	}

	hits, ok := out["hits"].([]interface{})
	if !ok || len(hits) != 1 {
		t.Fatalf("unexpected hits payload: %#v", out["hits"])
	}
	hit := hits[0].(map[string]interface{})
	if got := hit["summary"]; got != "matched example.org lookup" {
		t.Fatalf("unexpected summary: %#v", got)
	}
	source := hit["source"].(map[string]interface{})
	if _, ok := source["source"].(map[string]interface{})["ip"]; !ok {
		t.Fatalf("expected projected source.ip, got %#v", source)
	}
	if _, found := source["unrelated"]; found {
		t.Fatalf("did not expect unrelated fields: %#v", source)
	}
}

func TestBuildSecuritySummaryFallsBackToSource(t *testing.T) {
	source := map[string]interface{}{
		"message": "dns query fallback",
	}
	if got := buildSecuritySummary(nil, source); got != "dns query fallback" {
		t.Fatalf("unexpected fallback summary: %q", got)
	}
}

func TestRunSecuritySearchAgainstTestServer(t *testing.T) {
	var seenBody map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/logs-*/_search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		query := seenBody["query"].(map[string]any)
		boolQuery := query["bool"].(map[string]any)
		if _, ok := boolQuery["must"]; !ok {
			t.Fatalf("expected must clause in request: %#v", seenBody)
		}

		body := mustJSON(t, map[string]any{
			"took":      7,
			"timed_out": false,
			"_shards": map[string]any{
				"total":      1,
				"successful": 1,
				"skipped":    0,
				"failed":     0,
			},
			"hits": map[string]any{
				"total": map[string]any{
					"value":    1,
					"relation": "eq",
				},
				"hits": []any{
					map[string]any{
						"_index": "logs-*",
						"_id":    "doc-1",
						"_score": 1.5,
						"_source": map[string]any{
							"@timestamp": "2026-01-01T10:00:00Z",
							"event": map[string]any{
								"dataset": "suricata.eve",
							},
							"suricata": map[string]any{
								"eve": map[string]any{
									"alert": map[string]any{
										"signature": "ET MALWARE test",
									},
								},
							},
						},
						"highlight": map[string]any{
							"suricata.eve.alert.signature": []any{"ET <em>MALWARE</em> test"},
						},
					},
				},
			},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})

	client, err := newTestClient(transport)
	if err != nil {
		t.Fatalf("newTestClient error: %v", err)
	}

	out, err := runSecuritySearch(context.Background(), client, nil, SearchSecurityEventsArgs{
		Index: "logs-*",
		Text:  "malware",
	})
	if err != nil {
		t.Fatalf("runSecuritySearch error: %v", err)
	}

	hits := out["hits"].([]interface{})
	if len(hits) != 1 {
		t.Fatalf("unexpected hit count: %d", len(hits))
	}
	hit := hits[0].(map[string]interface{})
	if got := hit["summary"]; got != "ET MALWARE test" {
		t.Fatalf("unexpected summary: %#v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newTestClient(transport http.RoundTripper) (*Client, error) {
	cfg := esv8.Config{
		Addresses: []string{"http://example.com"},
		Transport: transport,
	}
	raw, err := esv8.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	typed, err := esv8.NewTypedClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{Raw: raw, Typed: typed}, nil
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return b
}

func TestBuildTermQuerySpecialCases(t *testing.T) {
	// Test CIDR
	q := buildTermQuery("source.ip", "10.0.0.0/8")
	if q.QueryString == nil {
		t.Fatal("expected query_string for CIDR")
	}
	if q.QueryString.Query != "source.ip: \"10.0.0.0\\/8\"" {
		t.Errorf("unexpected CIDR query: %s", q.QueryString.Query)
	}

	// Test Wildcard
	q = buildTermQuery("host.name", "server*")
	if q.Wildcard == nil {
		t.Fatal("expected wildcard for *")
	}
	if *q.Wildcard["host.name"].Value != "server*" {
		t.Errorf("unexpected wildcard: %s", *q.Wildcard["host.name"].Value)
	}

	// Test MAC (should be a normal term query, but let's check it doesn't break)
	q = buildTermQuery("source.mac", "00:11:22:33:44:55")
	if q.Term == nil {
		t.Fatal("expected term for MAC")
	}

	// Test MAC prefix (wildcard)
	q = buildTermQuery("source.mac", "00:11:22*")
	if q.Wildcard == nil {
		t.Fatal("expected wildcard for MAC prefix")
	}
}

func TestEscapeQueryStringValueEscapesAllSpecialChars(t *testing.T) {
	// One of each character escapeQueryStringValue claims to handle, in the
	// same order the function lists them, plus a leading backslash to check
	// that escaping it first doesn't get re-escaped by a later replacement.
	input := `\` + `"+-=&&||><!(){}[]^~:/`
	got := escapeQueryStringValue(input)

	// "&&" and "||" are escaped as whole two-char tokens (a single leading
	// backslash), not char-by-char, since escapeQueryStringValue's special
	// character list treats them as multi-char entries.
	want := `\\` + `\"\+\-\=\&&\||\>\<\!\(\)\{\}\[\]\^\~\:\/`
	if got != want {
		t.Fatalf("escapeQueryStringValue(%q) = %q, want %q", input, got, want)
	}

	// A plain value with no special characters should be returned unchanged.
	if got := escapeQueryStringValue("plain value with no special chars"); got != "plain value with no special chars" {
		t.Errorf("escapeQueryStringValue(no special chars) = %q, want unchanged", got)
	}
}

// TestTruncateSummaryUTF8Boundary pins today's byte-slicing behavior for
// truncateSummary: it cuts at a fixed byte offset (217) regardless of rune
// boundaries. With a string built entirely from 2-byte UTF-8 runes, the cut
// point lands mid-rune and produces invalid UTF-8. This test documents that
// behavior so a future rune-safe fix has a regression test to change
// (see the rune-unsafety gotcha noted in internal/IMPL.md).
func TestTruncateSummaryUTF8Boundary(t *testing.T) {
	// "é" is 2 bytes (0xC3 0xA9) in UTF-8; 150 repeats = 300 bytes, well past
	// the 220-byte threshold, with rune boundaries only at even byte offsets.
	s := strings.Repeat("é", 150)

	got := truncateSummary(s)

	if len(got) != 220 {
		t.Fatalf("len(got) = %d, want 220 (217 truncated bytes + \"...\")", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("got = %q, want it to end with \"...\"", got)
	}
	if utf8.ValidString(got) {
		t.Fatalf("got = %q is valid UTF-8; expected the current byte-offset cut (217) to split a 2-byte rune and produce invalid UTF-8 — if this now passes, truncateSummary has become rune-safe and this test should be updated to assert validity instead", got)
	}

	// Below the 220-byte threshold, the string passes through unchanged.
	short := strings.Repeat("é", 50) // 100 bytes
	if got := truncateSummary(short); got != short {
		t.Fatalf("truncateSummary(short) = %q, want unchanged %q", got, short)
	}
}

func TestBuildSecuritySearchRequestMACURLAndDirectionalIPFilters(t *testing.T) {
	req := buildSecuritySearchRequest(SearchSecurityEventsArgs{
		Index:  "logs-*",
		SrcIP:  "10.0.0.1",
		DstIP:  "10.0.0.2",
		MAC:    "00:11:22:33:44:55",
		URL:    "https://example.org/path",
		Domain: "example.org",
	})

	if req.Query == nil || req.Query.Bool == nil {
		t.Fatal("expected bool query")
	}

	queryJSON := string(mustJSON(t, req.Query))
	for _, want := range []string{
		`"source.ip":{"value":"10.0.0.1"}`,
		`"client.ip":{"value":"10.0.0.1"}`,
		`"destination.ip":{"value":"10.0.0.2"}`,
		`"server.ip":{"value":"10.0.0.2"}`,
		`"source.mac":{"value":"00:11:22:33:44:55"}`,
		`"destination.mac":{"value":"00:11:22:33:44:55"}`,
		`"host.mac":{"value":"00:11:22:33:44:55"}`,
		`"url.full":{"value":"https://example.org/path"}`,
		`"url.full.keyword":{"value":"https://example.org/path"}`,
	} {
		if !strings.Contains(queryJSON, want) {
			t.Errorf("query JSON = %s, want it to contain %s", queryJSON, want)
		}
	}

	// SrcIP/DstIP must not bleed into each other's filter fields.
	for _, unwanted := range []string{
		`"destination.ip":{"value":"10.0.0.1"}`,
		`"source.ip":{"value":"10.0.0.2"}`,
	} {
		if strings.Contains(queryJSON, unwanted) {
			t.Errorf("query JSON = %s, did not want %s", queryJSON, unwanted)
		}
	}

	// Each of SrcIP/DstIP/MAC/URL/Domain contributes its own filter clause.
	if got := len(req.Query.Bool.Filter); got != 5 {
		t.Fatalf("filter count = %d, want 5, filters: %s", got, queryJSON)
	}
}
