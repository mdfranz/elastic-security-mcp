package util

import (
	"encoding/json"
	"fmt"
	"strings"
)

// NormalizeJSON minifies a JSON string and ensures consistent formatting.
// It returns the original string if it is not valid JSON.
func NormalizeJSON(s string) string {
	var j interface{}
	if err := json.Unmarshal([]byte(s), &j); err != nil {
		return s
	}
	b, err := json.Marshal(j)
	if err != nil {
		return s
	}
	return string(b)
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
