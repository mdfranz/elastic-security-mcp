package elasticsearch

import (
	"testing"

	"github.com/mfranz/elastic-security-mcp/internal/util"
)

func TestSearchElasticNormalization(t *testing.T) {
	tests := []struct {
		name     string
		args     SearchArgs
		expected SearchArgs
	}{
		{
			name: "index trimming and empty query",
			args: SearchArgs{
				Index: "  logs-*  ",
				Query: "",
			},
			expected: SearchArgs{
				Index: "logs-*",
				Query: `{"query":{"match_all":{}}}`,
			},
		},
		{
			name: "query minification",
			args: SearchArgs{
				Index: "logs-*",
				Query: `{
					"query": {
						"match_all": {}
					}
				}`,
			},
			expected: SearchArgs{
				Index: "logs-*",
				Query: `{"query":{"match_all":{}}}`,
			},
		},
		{
			name: "query object is stringified",
			args: SearchArgs{
				Index: "logs-*",
				Query: map[string]any{
					"size": 0,
					"query": map[string]any{
						"match_all": map[string]any{},
					},
				},
			},
			expected: SearchArgs{
				Index: "logs-*",
				Query: `{"query":{"match_all":{}},"size":0}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeSearchArgs(tt.args)

			if got.Index != tt.expected.Index {
				t.Errorf("Index = %q, want %q", got.Index, tt.expected.Index)
			}
			if got.Query != tt.expected.Query {
				t.Errorf("Query = %q, want %q", got.Query, tt.expected.Query)
			}

			// Verify cache key convergence
			key1, _ := cacheKey("search_elastic", got)
			key2, _ := cacheKey("search_elastic", tt.expected)
			if key1 != key2 {
				t.Errorf("Cache keys did not converge: %s != %s", key1, key2)
			}
		})
	}
}

func TestSearchElasticNormalizationDefaultQuery(t *testing.T) {
	got := normalizeSearchArgs(SearchArgs{Index: "logs-*", Query: ""})
	if got.Query != `{"query":{"match_all":{}}}` {
		t.Fatalf("default query normalization = %q", got.Query)
	}
}

func TestLookupDomainNormalization(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		expected string
	}{
		{
			name:     "case and dots",
			domain:   "Example.ORG.",
			expected: "example.org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDomain := util.NormalizeDomain(tt.domain)
			if gotDomain != tt.expected {
				t.Errorf("NormalizeDomain() = %v, want %v", gotDomain, tt.expected)
			}

			// Verify cache key (though lookup_domain doesn't use WrapWithCache yet,
			// it uses this string as a Redis key directly)
			key1 := "dns:name:" + gotDomain
			key2 := "dns:name:" + tt.expected
			if key1 != key2 {
				t.Errorf("Redis keys did not converge: %s != %s", key1, key2)
			}
		})
	}
}
