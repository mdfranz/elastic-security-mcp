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

	goai "github.com/zendev-sh/goai"
	goaimcp "github.com/zendev-sh/goai/mcp"
	"github.com/zendev-sh/goai/provider"
	"github.com/gorilla/websocket"
	"github.com/mfranz/elastic-security-mcp/internal/util"
)

//go:embed assets/*
var assets embed.FS

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
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
	mcpClient *goaimcp.Client
	llmModel  provider.LanguageModel
	tools     []goai.Tool
	modelName string
	useMemory bool
}

func RunServer(ctx context.Context, mcpClient *goaimcp.Client, model provider.LanguageModel, tools []goai.Tool, modelName string, port int, useMemory bool) error {
	s := &Server{
		mcpClient: mcpClient,
		llmModel:  model,
		tools:     tools,
		modelName: modelName,
		useMemory: useMemory,
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

	s.sendMessage(conn, WebMessage{Type: "setup", Model: s.modelName})

	history := []provider.Message{}

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
			history = []provider.Message{}
			s.sendMessage(conn, WebMessage{Type: "system", Content: "New session started. Previous context cleared."})
			s.sendMessage(conn, WebMessage{Type: "clear_status"})
			continue
		}

		if req.Type == "user" {
			userInput := req.Content

			if userInput == "/memory" {
				if !s.useMemory {
					s.sendMessage(conn, WebMessage{Type: "system", Content: "Conversation memory is disabled."})
				} else {
					hist := renderHistoryText(history)
					if hist == "" {
						hist = "(empty)"
					}
					s.sendMessage(conn, WebMessage{Type: "system", Content: fmt.Sprintf("Conversation Memory:\n%s", hist)})
				}
				continue
			}

			s.sendMessage(conn, WebMessage{Type: "user", Content: userInput})
			history = append(history, goai.UserMessage(userInput))

			s.processConversation(r.Context(), conn, &history, userInput)
		}
	}
}

func (s *Server) processConversation(ctx context.Context, conn *websocket.Conn, history *[]provider.Message, lastUserInput string) {
	for {
		s.sendMessage(conn, WebMessage{Type: "status", Content: "Analyzing request...", Thinking: true})
		slog.Info("LLM request", "history_len", len(*history), "model", s.modelName)

		result, err := util.WithRetry(ctx, func() (*goai.TextResult, error) {
			return goai.GenerateText(ctx, s.llmModel,
				goai.WithMessages(*history...),
				goai.WithSystem(systemPrompt),
				goai.WithTools(s.tools...),
				goai.WithTemperature(0),
				goai.WithMaxOutputTokens(4096),
			)
		})

		if err != nil {
			s.sendMessage(conn, WebMessage{Type: "error", Content: fmt.Sprintf("LLM error: %v", err)})
			s.sendMessage(conn, WebMessage{Type: "clear_status"})
			return
		}

		// Append assistant turn to history.
		*history = append(*history, result.ResponseMessages...)

		respText := result.Text
		toolCalls := result.ToolCalls

		// Detect stalling (model narrates instead of calling tools).
		content := strings.ToLower(respText)
		if len(toolCalls) == 0 && (strings.Contains(content, "i will") ||
			strings.Contains(content, "let me") ||
			strings.Contains(content, "now i'll") ||
			strings.Contains(content, "searching")) {

			*history = append(*history, goai.UserMessage("Please proceed with the tool call immediately. Do not narrate your intent."))
			continue
		}

		if respText != "" && len(toolCalls) == 0 {
			s.sendMessage(conn, WebMessage{Type: "assistant", Content: respText})
		}

		if len(toolCalls) > 0 {
			s.sendMessage(conn, WebMessage{Type: "status", Content: summarizeToolCalls(toolCalls), Thinking: true})

			for i, tc := range toolCalls {
				var args map[string]any
				if len(tc.Input) > 0 {
					if err := json.Unmarshal(tc.Input, &args); err != nil {
						slog.Warn("Failed to unmarshal tool arguments", "error", err, "name", tc.Name)
						args = make(map[string]any)
					}
				}

				toolEvent := &ToolEvent{
					ID:    tc.ID,
					Seq:   i + 1,
					Name:  tc.Name,
					State: "running",
					Args:  args,
				}
				s.sendMessage(conn, WebMessage{Type: "tool", Tool: toolEvent})

				toolResp, callErr := s.mcpClient.CallTool(ctx, tc.Name, args)

				resultText := ""
				isError := callErr != nil || (toolResp != nil && toolResp.IsError)
				if callErr != nil {
					resultText = fmt.Sprintf("error: %v", callErr)
				} else {
					resultText = extractToolText(toolResp)
				}

				isCached := false
				isStored := false
				if strings.HasPrefix(resultText, "✓ ") {
					isCached = true
					resultText = strings.TrimPrefix(resultText, "✓ ")
				} else if strings.HasPrefix(resultText, "↓ ") {
					isStored = true
					resultText = strings.TrimPrefix(resultText, "↓ ")
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
						Name:     tc.Name,
						State:    finalState,
						Args:     args,
						Result:   resultText,
						IsError:  isError,
						IsCached: isCached,
						IsStored: isStored,
					},
				})

				*history = append(*history, goai.ToolMessage(tc.ID, tc.Name, resultText))
			}

			s.sendMessage(conn, WebMessage{Type: "status", Content: "Tool results received. Drafting final answer...", Thinking: true})
			continue
		}

		s.sendMessage(conn, WebMessage{Type: "clear_status"})
		break
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

func summarizeToolCalls(toolCalls []provider.ToolCall) string {
	if len(toolCalls) == 0 {
		return "Waiting for assistant response..."
	}
	names := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.Name != "" {
			names = append(names, tc.Name)
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("Running %d tool call(s)...", len(toolCalls))
	}
	return fmt.Sprintf("Running %s...", strings.Join(names, ", "))
}

func extractToolText(toolResp *goaimcp.CallToolResult) string {
	if toolResp == nil {
		return ""
	}
	var sb strings.Builder
	for _, block := range toolResp.Content {
		if tc, ok := goaimcp.ParseTextContent(block); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func renderHistoryText(history []provider.Message) string {
	var sb strings.Builder
	for _, msg := range history {
		for _, p := range msg.Content {
			if p.Type == provider.PartText && p.Text != "" {
				role := "Human"
				if msg.Role == provider.RoleAssistant {
					role = "AI"
				}
				sb.WriteString(fmt.Sprintf("%s: %s\n", role, p.Text))
			}
		}
	}
	return sb.String()
}
