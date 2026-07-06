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
	"github.com/mfranz/elastic-security-mcp/internal/agent"
	goai "github.com/zendev-sh/goai"
	goaimcp "github.com/zendev-sh/goai/mcp"
	"github.com/zendev-sh/goai/provider"
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
	engine    *agent.Engine
	modelName string
	useMemory bool
}

func RunServer(ctx context.Context, mcpClient *goaimcp.Client, model provider.LanguageModel, tools []goai.Tool, modelName string, port int, useMemory bool) error {
	s := &Server{
		engine:    agent.New(mcpClient, model, tools, modelName),
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
	toolIDs := newToolIDAssigner()

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
			toolIDs = newToolIDAssigner()
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
					hist := agent.RenderHistoryText(history)
					if hist == "" {
						hist = "(empty)"
					}
					s.sendMessage(conn, WebMessage{Type: "system", Content: fmt.Sprintf("Conversation Memory:\n%s", hist)})
				}
				continue
			}

			s.sendMessage(conn, WebMessage{Type: "user", Content: userInput})
			history = append(history, goai.UserMessage(userInput))

			s.processConversation(r.Context(), conn, &history, toolIDs)
		}
	}
}

func (s *Server) processConversation(ctx context.Context, conn *websocket.Conn, history *[]provider.Message, toolIDs *toolIDAssigner) {
	result := s.engine.Turn(ctx, *history, func(ev agent.Event) {
		s.emitEvent(conn, ev, toolIDs)
	})
	*history = result.History

	if result.Err != nil {
		s.sendMessage(conn, WebMessage{Type: "error", Content: fmt.Sprintf("LLM error: %v", result.Err)})
	}
	s.sendMessage(conn, WebMessage{Type: "clear_status"})
}

// emitEvent translates an agent.Event into the WebSocket protocol, streaming
// a "running" tool message before each call and a "completed"/"error" one
// after, so the browser sees live per-tool progress.
func (s *Server) emitEvent(conn *websocket.Conn, ev agent.Event, toolIDs *toolIDAssigner) {
	switch ev.Kind {
	case agent.EventStatus:
		s.sendMessage(conn, WebMessage{Type: "status", Content: ev.Status, Thinking: true})
	case agent.EventToolStart, agent.EventToolEnd:
		t := ev.Tool
		var id string
		if ev.Kind == agent.EventToolStart {
			id = toolIDs.start(t.Call.ID)
		} else {
			id = toolIDs.end(t.Call.ID)
		}
		s.sendMessage(conn, WebMessage{
			Type: "tool",
			Tool: &ToolEvent{
				ID:       id,
				Seq:      t.Seq,
				Name:     t.Call.Name,
				State:    t.State,
				Args:     t.Args,
				Result:   t.Result,
				IsError:  t.IsError,
				IsCached: t.IsCached,
				IsStored: t.IsStored,
			},
		})
	case agent.EventAssistant:
		s.sendMessage(conn, WebMessage{Type: "assistant", Content: ev.Text})
	}
}

// toolIDAssigner makes provider tool-call IDs unique for the lifetime of a
// Web UI session. Some providers (Gemini in particular) fabricate a
// call-index-based ID that resets to the same value on every round trip
// (e.g. "call_search_elastic_0" for every single call), so the raw ID is not
// a safe key for the browser to distinguish successive calls to the same
// tool. Each EventToolStart mints a fresh suffix; the matching EventToolEnd
// (always emitted for the same call before the next call starts, since the
// engine executes tool calls synchronously) looks it back up so the start
// and end events pair up under one card.
type toolIDAssigner struct {
	next   int
	active map[string]string
}

func newToolIDAssigner() *toolIDAssigner {
	return &toolIDAssigner{active: make(map[string]string)}
}

func (a *toolIDAssigner) start(callID string) string {
	a.next++
	id := fmt.Sprintf("%s#%d", callID, a.next)
	a.active[callID] = id
	return id
}

func (a *toolIDAssigner) end(callID string) string {
	if id, ok := a.active[callID]; ok {
		delete(a.active, callID)
		return id
	}
	return callID
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
