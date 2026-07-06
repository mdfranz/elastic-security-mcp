package kibana

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNormalizeKibanaAPIRequest(t *testing.T) {
	tests := []struct {
		name       string
		args       KibanaAPIRequestArgs
		wantMethod string
		wantPath   string
		wantErr    bool
	}{
		{
			name:       "defaults method and prepends slash",
			args:       KibanaAPIRequestArgs{Path: "api/status"},
			wantMethod: "GET",
			wantPath:   "/api/status",
		},
		{
			name:       "uppercases and trims method and path",
			args:       KibanaAPIRequestArgs{Method: " post ", Path: " /api/saved_objects "},
			wantMethod: "POST",
			wantPath:   "/api/saved_objects",
		},
		{
			name:    "rejects empty path",
			args:    KibanaAPIRequestArgs{Method: "GET", Path: "  "},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMethod, gotPath, err := normalizeKibanaAPIRequest(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotMethod != tt.wantMethod || gotPath != tt.wantPath {
				t.Fatalf("got (%q, %q), want (%q, %q)", gotMethod, gotPath, tt.wantMethod, tt.wantPath)
			}
		})
	}
}

func TestBuildListDetectionRulesPathDocumentsNoDefaultOrCap(t *testing.T) {
	tests := []struct {
		name string
		args ListDetectionRulesArgs
		want string
	}{
		{
			name: "no params when values are not positive",
			args: ListDetectionRulesArgs{Page: 0, PerPage: -1},
			want: "/api/detection_engine/rules/_find",
		},
		{
			name: "passes positive params through without default or cap",
			args: ListDetectionRulesArgs{Page: 2, PerPage: 500},
			want: "/api/detection_engine/rules/_find?page=2&per_page=500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildListDetectionRulesPath(tt.args); got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildGetDetectionRulePath(t *testing.T) {
	if _, err := buildGetDetectionRulePath(GetDetectionRuleArgs{}); err == nil {
		t.Fatal("expected missing id/rule_id error")
	}

	got, err := buildGetDetectionRulePath(GetDetectionRuleArgs{Id: "saved object/id"})
	if err != nil {
		t.Fatalf("unexpected id error: %v", err)
	}
	if want := "/api/detection_engine/rules?id=saved+object%2Fid"; got != want {
		t.Fatalf("id path = %q, want %q", got, want)
	}

	got, err = buildGetDetectionRulePath(GetDetectionRuleArgs{RuleId: "rule/id with spaces"})
	if err != nil {
		t.Fatalf("unexpected rule_id error: %v", err)
	}
	if want := "/api/detection_engine/rules?rule_id=rule%2Fid+with+spaces"; got != want {
		t.Fatalf("rule_id path = %q, want %q", got, want)
	}

	got, err = buildGetDetectionRulePath(GetDetectionRuleArgs{Id: "id-1", RuleId: "rule-1"})
	if err != nil {
		t.Fatalf("unexpected both ids error: %v", err)
	}
	if want := "/api/detection_engine/rules?id=id-1"; got != want {
		t.Fatalf("both ids path = %q, want id to win as %q", got, want)
	}
}

func TestBuildListAgentsPath(t *testing.T) {
	got := buildListAgentsPath(ListAgentsArgs{
		Page:    3,
		PerPage: 25,
		Kuery:   `local_metadata.host.name: "host 1"`,
	})

	for _, want := range []string{
		"/api/fleet/agents?",
		"page=3",
		"perPage=25",
		"kuery=local_metadata.host.name%3A+%22host+1%22",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("path = %q, want it to contain %q", got, want)
		}
	}
}

func TestFormatResponse(t *testing.T) {
	tests := []struct {
		name            string
		body            []byte
		statusCode      int
		want            []string
		wantErrorExpect bool
		wantErrSubstr   string
	}{
		{
			name:       "pretty prints JSON",
			body:       []byte(`{"status":"ok","count":1}`),
			statusCode: 200,
			want:       []string{`"status": "ok"`, `"count": 1`},
		},
		{
			name:       "leaves plain text as-is",
			body:       []byte(`plain response`),
			statusCode: 200,
			want:       []string{`plain response`},
		},
		{
			name:            "returns Go error for HTTP errors",
			body:            []byte(`{"error":"nope"}`),
			statusCode:      404,
			wantErrorExpect: true,
			wantErrSubstr:   "HTTP 404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, extra, err := formatResponse(tt.body, tt.statusCode)
			if tt.wantErrorExpect {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.wantErrSubstr)
				}
				if res != nil {
					t.Fatalf("expected nil result on error, got %#v", res)
				}
				return
			}

			if err != nil {
				t.Fatalf("formatResponse error: %v", err)
			}
			if extra != nil {
				t.Fatalf("extra = %#v, want nil", extra)
			}
			text := toolResultText(t, res)
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Fatalf("text = %q, want it to contain %q", text, want)
				}
			}
		})
	}
}

func toolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) != 1 {
		t.Fatalf("unexpected result content: %#v", res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", res.Content[0])
	}
	return text.Text
}
