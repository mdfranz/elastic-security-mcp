package elasticsearch

import (
	"encoding/json"
	"strings"
	"testing"

	typedsearch "github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
)

func TestNormalizeSecurityAlertsArgs(t *testing.T) {
	got := normalizeSecurityAlertsArgs(SearchSecurityAlertsArgs{
		Query:    "  malware  ",
		Severity: " high ",
		RuleName: "  *Malware* ",
		Host:     " host-1 ",
		Start:    " 2026-01-01T00:00:00Z ",
		End:      " 2026-01-02T00:00:00Z ",
		Size:     99,
	})

	if got.Query != "malware" ||
		got.Severity != "high" ||
		got.RuleName != "*Malware*" ||
		got.Host != "host-1" ||
		got.Start != "2026-01-01T00:00:00Z" ||
		got.End != "2026-01-02T00:00:00Z" {
		t.Fatalf("unexpected normalized args: %#v", got)
	}
	if got.Size != 50 {
		t.Fatalf("Size = %d, want capped size 50", got.Size)
	}

	got = normalizeSecurityAlertsArgs(SearchSecurityAlertsArgs{Size: 0})
	if got.Size != 10 {
		t.Fatalf("default Size = %d, want 10", got.Size)
	}
}

func TestBuildSecurityAlertsRequest(t *testing.T) {
	req := buildSecurityAlertsRequest(SearchSecurityAlertsArgs{
		Query:    "malware",
		Severity: "high",
		RuleName: "*Malware*",
		Host:     "host-1",
		Start:    "2026-01-01T00:00:00Z",
		End:      "2026-01-02T00:00:00Z",
		Size:     7,
	})

	if req.Size == nil || *req.Size != 7 {
		t.Fatalf("Size = %#v, want 7", req.Size)
	}
	if len(req.Sort) != 1 {
		t.Fatalf("sort count = %d, want 1", len(req.Sort))
	}

	body := requestBodyMap(t, req)
	if got := body["size"]; got != float64(7) {
		t.Fatalf("JSON size = %#v, want 7", got)
	}
	if got := body["track_total_hits"]; got != true {
		t.Fatalf("track_total_hits = %#v, want true", got)
	}
	source := body["_source"].(map[string]any)
	includes := source["includes"].([]any)
	if len(includes) != len(alertsSourceIncludes) {
		t.Fatalf("_source includes count = %d, want %d", len(includes), len(alertsSourceIncludes))
	}
	if !strings.Contains(string(mustJSON(t, body["sort"])), `"@timestamp"`) {
		t.Fatalf("sort does not contain @timestamp: %#v", body["sort"])
	}

	queryJSON := string(mustJSON(t, body["query"]))
	for _, want := range []string{
		`"query":"malware"`,
		`"kibana.alert.rule.parameters.severity":{"value":"high"}`,
		`"kibana.alert.rule.name":{"value":"*Malware*"}`,
		`"host.name":{"value":"host-1"}`,
		`"host.name.keyword":{"value":"host-1"}`,
		`"@timestamp":{"gte":"2026-01-01T00:00:00Z","lte":"2026-01-02T00:00:00Z"}`,
	} {
		if !strings.Contains(queryJSON, want) {
			t.Errorf("query JSON = %s, want it to contain %s", queryJSON, want)
		}
	}
}

func TestShapeSecurityAlertsResponse(t *testing.T) {
	id := "alert-1"
	resp := &typedsearch.Response{
		Took: 5,
		Hits: types.HitsMetadata{
			Total: &types.TotalHits{Value: 1},
			Hits: []types.Hit{
				{
					Id_: &id,
					Source_: mustJSON(t, map[string]any{
						"@timestamp": "2026-01-01T12:00:00Z",
						"message":    "malware detected",
						"kibana": map[string]any{
							"alert": map[string]any{
								"rule": map[string]any{
									"name": "Malware rule",
									"parameters": map[string]any{
										"severity":    "high",
										"risk_score":  73,
										"description": "detects malware",
									},
								},
							},
						},
						"host":      map[string]any{"name": "host-1"},
						"unrelated": "drop me",
					}),
				},
			},
		},
	}

	out, err := shapeSecurityAlertsResponse(resp)
	if err != nil {
		t.Fatalf("shapeSecurityAlertsResponse error: %v", err)
	}
	if got := out["took"]; got != int64(5) {
		t.Fatalf("took = %#v, want 5", got)
	}

	hits, ok := out["hits"].([]interface{})
	if !ok || len(hits) != 1 {
		t.Fatalf("unexpected hits payload: %#v", out["hits"])
	}
	hit := hits[0].(map[string]interface{})
	if hit["_id"] != "alert-1" ||
		hit["timestamp"] != "2026-01-01T12:00:00Z" ||
		hit["rule_name"] != "Malware rule" ||
		hit["severity"] != "high" ||
		hit["message"] != "malware detected" {
		t.Fatalf("unexpected shaped hit: %#v", hit)
	}

	source := hit["source"].(map[string]interface{})
	if _, found := source["unrelated"]; found {
		t.Fatalf("unexpected unrelated source field: %#v", source)
	}
	if got := firstString(source, "host.name"); got != "host-1" {
		t.Fatalf("projected host.name = %q, want host-1", got)
	}
}

func TestShapeSecurityAlertsResponseInvalidSource(t *testing.T) {
	resp := &typedsearch.Response{
		Hits: types.HitsMetadata{
			Hits: []types.Hit{{Source_: json.RawMessage(`not json`)}},
		},
	}

	_, err := shapeSecurityAlertsResponse(resp)
	if err == nil || !strings.Contains(err.Error(), "failed to decode search hit source") {
		t.Fatalf("error = %v, want decode error", err)
	}
}

func requestBodyMap(t *testing.T, req *typedsearch.Request) map[string]any {
	t.Helper()

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return out
}
