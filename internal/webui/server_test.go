package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tmc/langchaingo/llms"
)

func setupTestServer() *Server {
	return &Server{
		mcpSession: nil,
		llmClient:  nil,
		lcTools:    []llms.Tool{},
		modelName:  "test-model",
		useMemory:  false,
	}
}

func TestWebSocketOriginCheck(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{"Empty origin", "", true},
		{"Localhost http", "http://localhost:3000", true},
		{"Localhost https", "https://localhost:3000", true},
		{"127.0.0.1 http", "http://127.0.0.1:8080", true},
		{"127.0.0.1 https", "https://127.0.0.1:8080", true},
		{"Remote origin", "http://example.com", false},
		{"Remote origin https", "https://evil.com:443", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			result := upgrader.CheckOrigin(req)
			if result != tt.want {
				t.Errorf("CheckOrigin(%q) = %v, want %v", tt.origin, result, tt.want)
			}
		})
	}
}

func TestWebSocketConnection(t *testing.T) {
	server := setupTestServer()

	ts := httptest.NewServer(http.HandlerFunc(server.handleWebSocket))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial WebSocket: %v", err)
	}
	defer ws.Close()

	ws.SetReadDeadline(time.Now().Add(time.Second))

	var msg WebMessage
	err = ws.ReadJSON(&msg)
	if err != nil {
		t.Fatalf("Failed to read setup message: %v", err)
	}

	if msg.Type != "setup" {
		t.Errorf("Expected 'setup' message type, got %q", msg.Type)
	}
	if msg.Model != "test-model" {
		t.Errorf("Expected model 'test-model', got %q", msg.Model)
	}
}

func TestWebMessageMarshaling(t *testing.T) {
	tests := []struct {
		name string
		msg  WebMessage
	}{
		{
			name: "Simple text message",
			msg: WebMessage{
				Type:    "user",
				Content: "Hello world",
			},
		},
		{
			name: "Tool event",
			msg: WebMessage{
				Type: "tool",
				Tool: &ToolEvent{
					ID:    "tool-1",
					Seq:   1,
					Name:  "search",
					State: "running",
					Args: map[string]any{
						"query": "test",
					},
				},
			},
		},
		{
			name: "Error message",
			msg: WebMessage{
				Type:    "error",
				Content: "Something went wrong",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			var decoded WebMessage
			err = json.Unmarshal(data, &decoded)
			if err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if decoded.Type != tt.msg.Type {
				t.Errorf("Type mismatch: %q vs %q", decoded.Type, tt.msg.Type)
			}
			if decoded.Content != tt.msg.Content {
				t.Errorf("Content mismatch: %q vs %q", decoded.Content, tt.msg.Content)
			}
		})
	}
}

func TestToolEventSerialization(t *testing.T) {
	toolEvent := &ToolEvent{
		ID:       "id-123",
		Seq:      1,
		Name:     "search",
		State:    "completed",
		Args:     map[string]any{"query": "test"},
		Result:   "found results",
		IsError:  false,
		IsCached: true,
	}

	data, err := json.Marshal(toolEvent)
	if err != nil {
		t.Fatalf("Failed to marshal ToolEvent: %v", err)
	}

	var decoded ToolEvent
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Failed to unmarshal ToolEvent: %v", err)
	}

	if decoded.ID != toolEvent.ID || decoded.State != toolEvent.State || decoded.IsCached != toolEvent.IsCached {
		t.Error("ToolEvent serialization failed")
	}
}

func TestSummarizeToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		toolCalls []llms.ToolCall
		wantLen   int
		wantWords []string
	}{
		{
			name:      "Empty tool calls",
			toolCalls: []llms.ToolCall{},
			wantWords: []string{"Waiting"},
		},
		{
			name: "Single tool call",
			toolCalls: []llms.ToolCall{
				{
					FunctionCall: &llms.FunctionCall{
						Name: "search",
					},
				},
			},
			wantWords: []string{"Running", "search"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeToolCalls(tt.toolCalls)
			for _, word := range tt.wantWords {
				if !strings.Contains(result, word) {
					t.Errorf("Expected %q to contain %q", result, word)
				}
			}
		})
	}
}

func TestExtractToolContent(t *testing.T) {
	tests := []struct {
		name     string
		toolResp *mcp.CallToolResult
		want     string
	}{
		{
			name:     "Nil response",
			toolResp: nil,
			want:     "",
		},
		{
			name:     "Empty response",
			toolResp: &mcp.CallToolResult{},
			want:     "",
		},
		{
			name: "Text content",
			toolResp: &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "result text"},
				},
			},
			want: "result text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolContent(tt.toolResp)
			if result != tt.want {
				t.Errorf("extractToolContent() = %q, want %q", result, tt.want)
			}
		})
	}
}

func TestConversationHistoryInitialization(t *testing.T) {
	history := newConversationHistory()

	if len(history) != 1 {
		t.Errorf("Expected 1 message in history, got %d", len(history))
	}

	if history[0].Role != llms.ChatMessageTypeSystem {
		t.Errorf("Expected system role, got %v", history[0].Role)
	}

	if len(history[0].Parts) == 0 {
		t.Error("Expected system prompt content")
	}
}

func TestWebSocketResetCommand(t *testing.T) {
	server := setupTestServer()

	ts := httptest.NewServer(http.HandlerFunc(server.handleWebSocket))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial WebSocket: %v", err)
	}
	defer ws.Close()

	ws.SetReadDeadline(time.Now().Add(time.Second))

	_ = ws.ReadJSON(&WebMessage{}) // Read setup message

	resetMsg := WebMessage{Type: "reset"}
	if err := ws.WriteJSON(resetMsg); err != nil {
		t.Fatalf("Failed to send reset message: %v", err)
	}

	var response WebMessage
	if err := ws.ReadJSON(&response); err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	if response.Type != "system" {
		t.Errorf("Expected 'system' message on reset, got %q", response.Type)
	}

	if !strings.Contains(response.Content, "New session") {
		t.Errorf("Expected 'New session' in reset response, got %q", response.Content)
	}
}
