package util

import (
	"testing"
)

func TestNormalizeJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "minify",
			input:    `{  "query":   { "match_all":  {} } }`,
			expected: `{"query":{"match_all":{}}}`,
		},
		{
			name:     "invalid json",
			input:    `{ invalid }`,
			expected: `{ invalid }`,
		},
		{
			name:     "consistent key order",
			input:    `{"b": 1, "a": 2}`,
			expected: `{"a":2,"b":1}`,
		},
		{
			name:     "fix quoted timestamp key",
			input:    `{"query":{"range":{"\"@timestamp\"":{"gte":"now-24h"}}}}`,
			expected: `{"query":{"range":{"@timestamp":{"gte":"now-24h"}}}}`,
		},
		{
			name:     "fix quoted key nested in sort",
			input:    `{"sort":[{"\"@timestamp\"":{"order":"desc"}}]}`,
			expected: `{"sort":[{"@timestamp":{"order":"desc"}}]}`,
		},
		{
			name:     "normal keys unchanged",
			input:    `{"query":{"match":{"host.name":"myhost"}}}`,
			expected: `{"query":{"match":{"host.name":"myhost"}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeJSON(tt.input); got != tt.expected {
				t.Errorf("NormalizeJSON() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNormalizeDomain(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "lowercase",
			input:    "EXAMPLE.COM",
			expected: "example.com",
		},
		{
			name:     "trim space",
			input:    "  example.com  ",
			expected: "example.com",
		},
		{
			name:     "remove trailing dot",
			input:    "example.com.",
			expected: "example.com",
		},
		{
			name:     "all combined",
			input:    "  EXAMPLE.ORG.  ",
			expected: "example.org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeDomain(tt.input); got != tt.expected {
				t.Errorf("NormalizeDomain() = %v, want %v", got, tt.expected)
			}
		})
	}
}
