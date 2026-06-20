package kibana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestPathWithSpace(t *testing.T) {
	tests := []struct {
		name     string
		space    string
		path     string
		expected string
	}{
		{
			name:     "No space configuration",
			space:    "",
			path:     "/api/saved_objects/_find",
			expected: "/api/saved_objects/_find",
		},
		{
			name:     "Default space configuration",
			space:    "default",
			path:     "/api/saved_objects/_find",
			expected: "/api/saved_objects/_find",
		},
		{
			name:     "Custom space configuration",
			space:    "marketing",
			path:     "/api/saved_objects/_find",
			expected: "/s/marketing/api/saved_objects/_find",
		},
		{
			name:     "Custom space configuration with relative path",
			space:    "marketing",
			path:     "api/saved_objects/_find",
			expected: "/s/marketing/api/saved_objects/_find",
		},
		{
			name:     "Global endpoint spaces",
			space:    "marketing",
			path:     "/api/spaces/space",
			expected: "/api/spaces/space",
		},
		{
			name:     "Already prefixed path",
			space:    "marketing",
			path:     "/s/marketing/api/saved_objects/_find",
			expected: "/s/marketing/api/saved_objects/_find",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("KIBANA_SPACE", tt.space)
			client, err := NewClient("http://localhost:5601", "elastic", "changeme", "")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			actual := client.pathWithSpace(tt.path)
			if actual != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, actual)
			}
		})
	}
	os.Unsetenv("KIBANA_SPACE")
}

func TestDoRequest(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get("kbn-xsrf") != "true" && r.Method != "GET" {
			t.Errorf("missing kbn-xsrf header on %s request", r.Method)
		}
		if r.Header.Get("Authorization") == "" {
			t.Errorf("missing Authorization header")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "elastic", "changeme", "")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Test GET request
	_, code, err := client.DoRequest(context.Background(), "GET", "/api/status", nil)
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("expected status 200, got %d", code)
	}

	// Test POST request
	_, code, err = client.DoRequest(context.Background(), "POST", "/api/saved_objects", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("POST request failed: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("expected status 200, got %d", code)
	}
}
