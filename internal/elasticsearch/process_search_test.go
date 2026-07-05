package elasticsearch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
)

func TestBuildProcessSearchRequestDefaultsAndFilters(t *testing.T) {
	req, size := buildProcessSearchRequest(SearchProcessesArgs{
		Executable:  "/usr/bin/bash",
		CommandLine: "curl example.org",
		ProcessName: "bash",
		ParentName:  "sshd",
		User:        "alice",
		PID:         123,
		ParentPID:   1,
		HashSHA256:  "ABCDEF",
		Host:        "host-1",
		Start:       "2026-01-01T00:00:00Z",
		End:         "2026-01-02T00:00:00Z",
	})

	if size != 20 {
		t.Fatalf("size = %d, want default 20", size)
	}
	if req.Size == nil || *req.Size != 20 {
		t.Fatalf("request size = %#v, want 20", req.Size)
	}
	if req.From != nil {
		t.Fatalf("From = %#v, want nil for zero offset", req.From)
	}

	body := requestBodyMap(t, req)
	queryJSON := string(mustJSON(t, body["query"]))
	for _, want := range []string{
		`"process.executable":{"value":"/usr/bin/bash"}`,
		`"process.command_line":{"query":"curl example.org"}`,
		`"process.name":{"value":"bash"}`,
		`"process.parent.name":{"value":"sshd"}`,
		`"user.name":{"value":"alice"}`,
		`"process.pid":{"value":123}`,
		`"process.parent.pid":{"value":1}`,
		`"process.hash.sha256":{"value":"abcdef"}`,
		`"host.name":{"value":"host-1"}`,
		`"@timestamp":{"gte":"2026-01-01T00:00:00Z","lte":"2026-01-02T00:00:00Z"}`,
	} {
		if !strings.Contains(queryJSON, want) {
			t.Errorf("query JSON = %s, want it to contain %s", queryJSON, want)
		}
	}

	source := body["_source"].(map[string]any)
	includes := source["includes"].([]any)
	for _, want := range []string{
		"process.name",
		"process.parent.command_line",
		"user.name",
		"host.name",
		"event.action",
	} {
		if !containsAnyString(includes, want) {
			t.Errorf("_source includes = %#v, want %q", includes, want)
		}
	}
	if !strings.Contains(string(mustJSON(t, body["sort"])), `"@timestamp"`) {
		t.Fatalf("sort does not contain @timestamp: %#v", body["sort"])
	}
}

func TestBuildProcessSearchRequestCapsSizeAndSetsFrom(t *testing.T) {
	req, size := buildProcessSearchRequest(SearchProcessesArgs{Size: 999, From: 40})

	if size != 100 {
		t.Fatalf("size = %d, want cap 100", size)
	}
	if req.Size == nil || *req.Size != 100 {
		t.Fatalf("request size = %#v, want 100", req.Size)
	}
	if req.From == nil || *req.From != 40 {
		t.Fatalf("From = %#v, want 40", req.From)
	}
}

func TestBuildProcessSearchRequestIgnoresNonPositivePIDs(t *testing.T) {
	req, _ := buildProcessSearchRequest(SearchProcessesArgs{PID: -1, ParentPID: 0})

	queryJSON := string(mustJSON(t, requestBodyMap(t, req)["query"]))
	for _, unwanted := range []string{`"process.pid"`, `"process.parent.pid"`} {
		if strings.Contains(queryJSON, unwanted) {
			t.Fatalf("query JSON = %s, did not want %s", queryJSON, unwanted)
		}
	}
}

func TestShapeProcessResultsSkipsInvalidSources(t *testing.T) {
	results := shapeProcessResults([]types.Hit{
		{},
		{Source_: []byte(`not json`)},
		{Source_: mustJSON(t, map[string]any{
			"process": map[string]any{"name": "bash"},
			"host":    map[string]any{"name": "host-1"},
		})},
	})

	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1: %#v", len(results), results)
	}
	if got := firstString(results[0], "process.name"); got != "bash" {
		t.Fatalf("process.name = %q, want bash", got)
	}

	empty := shapeProcessResults(nil)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty results = %#v, want non-nil empty slice", empty)
	}
}

func TestRunProcessSearchAllShardsFailedHint(t *testing.T) {
	client, err := newTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("all shards failed")
	}))
	if err != nil {
		t.Fatalf("newTestClient error: %v", err)
	}

	_, err = runProcessSearch(context.Background(), client, nil, SearchProcessesArgs{ProcessName: "bash"})
	if err == nil {
		t.Fatal("expected runProcessSearch error")
	}
	for _, want := range []string{
		"all shards failed",
		"ensure Elastic Agent is collecting endpoint process data",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func containsAnyString(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
