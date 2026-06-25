package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/mfranz/elastic-security-mcp/internal/llm"
	"github.com/mfranz/elastic-security-mcp/internal/util"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	anyllm "github.com/mozilla-ai/any-llm-go"
)

//go:embed assets/*
var assets embed.FS

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // Allow requests without Origin header
		}
		return strings.HasPrefix(origin, "http://localhost") ||
		       strings.HasPrefix(origin, "https://localhost") ||
		       strings.HasPrefix(origin, "http://127.0.0.1") ||
		       strings.HasPrefix(origin, "https://127.0.0.1")
	},
}

type WebMessage struct {
	Type     string     `json:"type"`
	Content  string     `json:"content,omitempty"`
	Model    string     `json:"model,omitempty"`
	Thinking bool       `json:"thinking,omitempty"`
	Tool     *ToolEvent `json:"tool,omitempty"`
}

type ToolEvent struct {
	ID       string         `json:"id,omitempty"`
	Seq      int            `json:"seq,omitempty"`
	Name     string         `json:"name,omitempty"`
	State    string         `json:"state,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Result   string         `json:"result,omitempty"`
	IsError  bool           `json:"is_error,omitempty"`
	IsCached bool           `json:"is_cached,omitempty"`
	IsStored bool           `json:"is_stored,omitempty"`
}

type Server struct {
	mcpSession *mcp.ClientSession
	llmClient  anyllm.Provider
	anyTools   []anyllm.Tool
	modelName  string
	useMemory  bool
}

func RunServer(ctx context.Context, session *mcp.ClientSession, client anyllm.Provider, tools []anyllm.Tool, modelName string, port int, useMemory bool) error {
	s := &Server{
		mcpSession: session,
		llmClient:  client,
		anyTools:   tools,
		modelName:  modelName,
		useMemory:  useMemory,
	}

	assetFS, err := fs.Sub(assets, "assets")
	if err != nil {
		return fmt.Errorf("failed to load embedded assets: %w", err)
	}
	http.Handle("/", http.FileServer(http.FS(assetFS)))
	http.HandleFunc("/ws", s.handleWebSocket)

	slog.Info("Web UI server starting", "port", port, "url", fmt.Sprintf("http://localhost:%d", port))
	server := &http.Server{Addr: fmt.Sprintf(":%d", port)}

	go func() {
		<-ctx.Done()
		server.Shutdown(context.Background())
	}()

	return server.ListenAndServe()
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Initial setup message
	s.sendMessage(conn, WebMessage{Type: "setup", Model: s.modelName})

	history := newConversationHistory()
	connMem := llm.NewConversationBuffer()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			slog.Info("WebSocket client disconnected")
			break
		}

		var req WebMessage
		if err := json.Unmarshal(message, &req); err != nil {
			s.sendMessage(conn, WebMessage{Type: "error", Content: "Invalid message format"})
			continue
		}

		if req.Type == "reset" {
			history = newConversationHistory()
			connMem = llm.NewConversationBuffer()
			s.sendMessage(conn, WebMessage{Type: "system", Content: "New session started. Previous context cleared."})
			s.sendMessage(conn, WebMessage{Type: "clear_status"})
			continue
		}

		if req.Type == "user" {
			userInput := req.Content

			// Handle /memory command
			if userInput == "/memory" {
				if !s.useMemory {
					s.sendMessage(conn, WebMessage{Type: "system", Content: "Conversation memory is disabled."})
				} else {
					vars, err := connMem.LoadMemoryVariables(r.Context(), nil)
					if err != nil {
						s.sendMessage(conn, WebMessage{Type: "error", Content: fmt.Sprintf("Memory error: %v", err)})
					} else {
						hist, _ := vars["history"].(string)
						if hist == "" {
							hist = "(empty)"
						}
						s.sendMessage(conn, WebMessage{Type: "system", Content: fmt.Sprintf("Conversation Memory:\n%s", hist)})
					}
				}
				continue
			}

			s.sendMessage(conn, WebMessage{Type: "user", Content: userInput})

			history = append(history, anyllm.Message{
				Role:    anyllm.RoleUser,
				Content: userInput,
			})

			// Conversation loop
			s.processConversation(r.Context(), conn, &history, connMem, userInput)
		}
	}
}

func (s *Server) processConversation(ctx context.Context, conn *websocket.Conn, history *[]anyllm.Message, connMem *llm.ConversationBuffer, lastUserInput string) {
	for {
		s.sendMessage(conn, WebMessage{Type: "status", Content: "Analyzing request...", Thinking: true})
		slog.Info("LLM request", "history_len", len(*history), "model", s.modelName)
		
		var (
			tempZero      = 0.0
			maxTokens4096 = 4096
		)

		resp, err := util.WithRetry(ctx, func() (*anyllm.ChatCompletion, error) {
			return s.llmClient.Completion(ctx, anyllm.CompletionParams{
				Model:       s.modelName,
				Messages:    *history,
				Tools:       s.anyTools,
				Temperature: &tempZero,
				MaxTokens:   &maxTokens4096,
			})
		})

		if err != nil {
			s.sendMessage(conn, WebMessage{Type: "error", Content: fmt.Sprintf("LLM error: %v", err)})
			s.sendMessage(conn, WebMessage{Type: "clear_status"})
			return
		}

		if resp == nil || len(resp.Choices) == 0 {
			s.sendMessage(conn, WebMessage{Type: "error", Content: "LLM returned no choices"})
			s.sendMessage(conn, WebMessage{Type: "clear_status"})
			return
		}

		choice := resp.Choices[0]
		*history = append(*history, choice.Message)

		// Detect stalling (copy logic from main.go)
		content := strings.ToLower(choice.Message.ContentString())
		if len(choice.Message.ToolCalls) == 0 && (strings.Contains(content, "i will") ||
			strings.Contains(content, "let me") ||
			strings.Contains(content, "now i'll") ||
			strings.Contains(content, "searching")) {

			*history = append(*history, anyllm.Message{
				Role:    anyllm.RoleUser,
				Content: "Please proceed with the tool call immediately. Do not narrate your intent.",
			})
			continue
		}

		if choice.Message.ContentString() != "" && len(choice.Message.ToolCalls) == 0 {
			s.sendMessage(conn, WebMessage{Type: "assistant", Content: choice.Message.ContentString()})

			if s.useMemory && lastUserInput != "" {
				_ = connMem.SaveContext(ctx,
					map[string]any{"input": lastUserInput},
					map[string]any{"output": choice.Message.ContentString()},
				)
			}
		}

		if len(choice.Message.ToolCalls) > 0 {
			s.sendMessage(conn, WebMessage{Type: "status", Content: summarizeToolCalls(choice.Message.ToolCalls), Thinking: true})

			toolResults := []anyllm.Message{}
			for i, tc := range choice.Message.ToolCalls {
				name := tc.Function.Name
				argsJSON := tc.Function.Arguments

				var args map[string]any
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					slog.Warn("Failed to unmarshal tool arguments", "error", err, "args", argsJSON)
					args = make(map[string]any)
				}
				toolEvent := &ToolEvent{
					ID:    tc.ID,
					Seq:   i + 1,
					Name:  name,
					State: "running",
					Args:  args,
				}
				s.sendMessage(conn, WebMessage{Type: "tool", Tool: toolEvent})

				toolResp, err := s.mcpSession.CallTool(ctx, &mcp.CallToolParams{
					Name:      name,
					Arguments: args,
				})

				resultText := ""
				isError := false
				isCached := false
				isStored := false
				if err != nil {
					resultText = fmt.Sprintf("error: %v", err)
					isError = true
				} else {
					resultText = extractToolContent(toolResp)
					isError = toolResp != nil && toolResp.IsError
					if strings.HasPrefix(resultText, "✓ ") {
						isCached = true
						resultText = strings.TrimPrefix(resultText, "✓ ")
					} else if strings.HasPrefix(resultText, "↓ ") {
						isStored = true
						resultText = strings.TrimPrefix(resultText, "↓ ")
					}
				}
				finalState := "completed"
				if isError {
					finalState = "error"
				}
				s.sendMessage(conn, WebMessage{
					Type: "tool",
					Tool: &ToolEvent{
						ID:       tc.ID,
						Seq:      i + 1,
						Name:     name,
						State:    finalState,
						Args:     args,
						Result:   resultText,
						IsError:  isError,
						IsCached: isCached,
						IsStored: isStored,
					},
				})

				toolResults = append(toolResults, anyllm.Message{
					Role:       anyllm.RoleTool,
					ToolCallID: tc.ID,
					Name:       name,
					Content:    resultText,
				})
			}

			for _, res := range toolResults {
				*history = append(*history, res)
			}
			s.sendMessage(conn, WebMessage{Type: "status", Content: "Tool results received. Drafting final answer...", Thinking: true})
			continue // Loop to let LLM process tool results
		}

		s.sendMessage(conn, WebMessage{Type: "clear_status"})
		break
	}
}

func newConversationHistory() []anyllm.Message {
	return []anyllm.Message{
		{
			Role:    anyllm.RoleSystem,
			Content: systemPrompt,
		},
	}
}

func (s *Server) sendMessage(conn *websocket.Conn, msg WebMessage) {
	b, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal message", "error", err, "type", msg.Type)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		slog.Error("Failed to write message", "error", err, "type", msg.Type)
	}
}

// Helper functions (copied from main.go or slightly adapted)
const systemPrompt = `You are a silent Elastic Security analyst tool.
YOUR ONLY JOB IS TO CALL TOOLS.
NEVER explain what you are doing.
NEVER say "I will search" or "Let me check" or "Now I'll".
IF YOU NEED DATA, CALL THE APPROPRIATE SEARCH OR LOOKUP TOOL IMMEDIATELY.
DO NOT PROVIDE ANY TEXT UNTIL YOU HAVE THE RESULTS.
ALWAYS use Markdown tables for tabular data.

TOOL SELECTION GUIDE — call the right tool immediately:
- search_security_alerts: detection alerts from Elastic Security rules
- search_processes: endpoint process events (automatically searches logs-endpoint.events.process-*)
- search_security_events: network and endpoint events — use index logs-zeek.*-* for Zeek, logs-suricata.*-* for Suricata, packetbeat-* for Packetbeat, logs-endpoint.events.network-* or logs-endpoint.events.file-* for endpoint
- list_indices: discover available indices before searching if unsure
- list_kibana_spaces: discover or list available Kibana spaces
- list_detection_rules / get_detection_rule: inspect or browse detection rules
- list_agents: check Elastic Agent / Fleet status
- lookup_domain / lookup_ip: fast DNS history lookup from cache
- search_elastic: ONLY for raw Elasticsearch JSON DSL that no other tool can express
- kibana_api_request: ONLY for Kibana API endpoints not covered by other tools`

func summarizeToolCalls(toolCalls []anyllm.ToolCall) string {
	if len(toolCalls) == 0 {
		return "Waiting for assistant response..."
	}
	names := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.Function.Name != "" {
			names = append(names, tc.Function.Name)
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("Running %d tool call(s)...", len(toolCalls))
	}
	return fmt.Sprintf("Running %s...", strings.Join(names, ", "))
}

func extractToolContent(toolResp *mcp.CallToolResult) string {
	if toolResp == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range toolResp.Content {
		if txt, ok := c.(*mcp.TextContent); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(txt.Text)
		}
	}
	return sb.String()
}
