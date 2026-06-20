package util

import (
	"encoding/json"
	"fmt"
	"strings"
)

// NormalizeJSON minifies a JSON string and ensures consistent formatting.
// It also fixes LLM-generated queries where field names are wrapped in extra
// quotes (e.g. "\"@timestamp\"" → "@timestamp").
// It returns the original string if it is not valid JSON.
func NormalizeJSON(s string) string {
	var j interface{}
	if err := json.Unmarshal([]byte(s), &j); err != nil {
		return s
	}
	j = fixQuotedKeys(j)
	b, err := json.Marshal(j)
	if err != nil {
		return s
	}
	return string(b)
}

// fixQuotedKeys recursively walks a decoded JSON value and strips surrounding
// double-quotes from object keys (e.g. the key `"@timestamp"` becomes `@timestamp`).
// This corrects a common LLM mistake where field names are double-escaped.
func fixQuotedKeys(v interface{}) interface{} {
	switch obj := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(obj))
		for k, val := range obj {
			if len(k) > 2 && strings.HasPrefix(k, `"`) && strings.HasSuffix(k, `"`) {
				k = k[1 : len(k)-1]
			}
			out[k] = fixQuotedKeys(val)
		}
		return out
	case []interface{}:
		for i, item := range obj {
			obj[i] = fixQuotedKeys(item)
		}
	}
	return v
}

// StringifyJSON takes an object or a string and returns a minified JSON string.
func StringifyJSON(input any) string {
	if input == nil {
		return ""
	}

	// If it's already a string, try to normalize it
	if s, ok := input.(string); ok {
		return NormalizeJSON(s)
	}

	// Otherwise, marshal it
	b, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprintf("%v", input)
	}
	return string(b)
}

// NormalizeDomain canonicalizes a domain name by lowercasing it and removing a trailing dot.
func NormalizeDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return strings.TrimSuffix(domain, ".")
}
