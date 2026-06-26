package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zendev-sh/goai/provider"
)

func setupTestServer() *Server {
	return &Server{
		mcpClient: nil,
		llmModel:  nil,
		tools:     nil,
		modelName: "test-model",
		useMemory: false,
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
		toolCalls []provider.ToolCall
		wantWords []string
	}{
		{
			name:      "Empty tool calls",
			toolCalls: []provider.ToolCall{},
			wantWords: []string{"Waiting"},
		},
		{
			name: "Single tool call",
			toolCalls: []provider.ToolCall{
				{Name: "search"},
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

func TestExtractToolText(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{
			name: "Nil response",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolText(nil)
			if result != tt.want {
				t.Errorf("extractToolText(nil) = %q, want %q", result, tt.want)
			}
		})
	}
}

func TestConversationHistoryInitialization(t *testing.T) {
	history := []provider.Message{}

	if len(history) != 0 {
		t.Errorf("Expected 0 messages in history, got %d", len(history))
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

	_ = ws.ReadJSON(&WebMessage{}) // consume setup message

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
