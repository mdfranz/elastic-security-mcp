package main

import (
	"strings"
	"testing"
)

func TestValidateEndpointURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "https", value: "https://elastic.example.com"},
		{name: "http with port", value: "http://localhost:9200"},
		{name: "unresolved placeholder", value: "${ELASTIC_URL}", wantErr: "unresolved"},
		{name: "missing scheme", value: "elastic.example.com", wantErr: "http or https"},
		{name: "unsupported scheme", value: "ftp://elastic.example.com", wantErr: "http or https"},
		{name: "missing host", value: "https:///search", wantErr: "include a host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEndpointURL("ELASTIC_URL", tt.value)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateEndpointURL() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateEndpointURL() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
