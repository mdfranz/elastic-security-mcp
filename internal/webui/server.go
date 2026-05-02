package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/memory"
)

//go:embed assets/*
var assets embed.FS

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all for local tool
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
	llmClient  llms.Model
	lcTools    []llms.Tool
	modelName  string
	useMemory  bool
}

func RunServer(ctx context.Context, session *mcp.ClientSession, client llms.Model, tools []llms.Tool, modelName string, port int, useMemory bool) error {
	s := &Server{
		mcpSession: session,
		llmClient:  client,
		lcTools:    tools,
		modelName:  modelName,
		useMemory:  useMemory,
	}

	assetFS, _ := fs.Sub(assets, "assets")
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
	connMem := memory.NewConversationBuffer()

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
			connMem = memory.NewConversationBuffer()
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

			history = append(history, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextContent{Text: userInput}},
			})

			// Conversation loop
			s.processConversation(r.Context(), conn, &history, connMem, userInput)
		}
	}
}

func (s *Server) processConversation(ctx context.Context, conn *websocket.Conn, history *[]llms.MessageContent, connMem *memory.ConversationBuffer, lastUserInput string) {
	for {
		s.sendMessage(conn, WebMessage{Type: "status", Content: "Analyzing request...", Thinking: true})

		resp, err := s.llmClient.GenerateContent(ctx, *history,
			llms.WithTools(s.lcTools),
			llms.WithMaxTokens(4096),
			llms.WithTemperature(0),
		)

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
		assistantParts := []llms.ContentPart{}
		if choice.Content != "" {
			assistantParts = append(assistantParts, llms.TextContent{Text: choice.Content})
		}

		for i := range choice.ToolCalls {
			if choice.ToolCalls[i].ID == "" {
				b := make([]byte, 8)
				if _, err := rand.Read(b); err != nil {
					slog.Error("Failed to generate tool call ID", "error", err)
					s.sendMessage(conn, WebMessage{Type: "error", Content: "Failed to generate tool call ID"})
					return
				}
				choice.ToolCalls[i].ID = hex.EncodeToString(b)
			}
			if choice.ToolCalls[i].Type == "" {
				choice.ToolCalls[i].Type = "tool_call"
			}
			assistantParts = append(assistantParts, choice.ToolCalls[i])
		}

		*history = append(*history, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: assistantParts,
		})

		// Detect stalling (copy logic from main.go)
		content := strings.ToLower(choice.Content)
		if len(choice.ToolCalls) == 0 && (strings.Contains(content, "i will") ||
			strings.Contains(content, "let me") ||
			strings.Contains(content, "now i'll") ||
			strings.Contains(content, "searching")) {

			*history = append(*history, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextContent{Text: "Please proceed with the tool call immediately. Do not narrate your intent."}},
			})
			continue
		}

		if choice.Content != "" && len(choice.ToolCalls) == 0 {
			s.sendMessage(conn, WebMessage{Type: "assistant", Content: choice.Content})

			if s.useMemory && lastUserInput != "" {
				_ = connMem.SaveContext(ctx,
					map[string]any{"input": lastUserInput},
					map[string]any{"output": choice.Content},
				)
			}
		}

		if len(choice.ToolCalls) > 0 {
			s.sendMessage(conn, WebMessage{Type: "status", Content: summarizeToolCalls(choice.ToolCalls), Thinking: true})

			toolResults := []llms.ContentPart{}
			for i, tc := range choice.ToolCalls {
				name := tc.FunctionCall.Name
				argsJSON := tc.FunctionCall.Arguments

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

				toolResults = append(toolResults, llms.ToolCallResponse{
					ToolCallID: tc.ID,
					Name:       name,
					Content:    resultText,
				})
			}

			for _, res := range toolResults {
				*history = append(*history, llms.MessageContent{
					Role:  llms.ChatMessageTypeTool,
					Parts: []llms.ContentPart{res},
				})
			}
			s.sendMessage(conn, WebMessage{Type: "status", Content: "Tool results received. Drafting final answer...", Thinking: true})
			continue // Loop to let LLM process tool results
		}

		s.sendMessage(conn, WebMessage{Type: "clear_status"})
		break
	}
}

func newConversationHistory() []llms.MessageContent {
	return []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextContent{Text: systemPrompt}},
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
IF YOU NEED DATA, CALL search_security_events OR list_indices IMMEDIATELY.
USE search_elastic ONLY WHEN YOU NEED RAW ELASTICSEARCH JSON DSL THAT search_security_events CANNOT EXPRESS.
DO NOT PROVIDE ANY TEXT UNTIL YOU HAVE THE RESULTS.
ALWAYS use Markdown tables for tabular data.`

func summarizeToolCalls(toolCalls []llms.ToolCall) string {
	if len(toolCalls) == 0 {
		return "Waiting for assistant response..."
	}
	names := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.FunctionCall != nil {
			names = append(names, tc.FunctionCall.Name)
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
