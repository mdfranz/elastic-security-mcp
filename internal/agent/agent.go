// Package agent implements the LLM + MCP tool-calling loop shared by the CLI
// TUI (cmd/cli) and the Web UI (internal/webui). Both front ends drive an
// Engine and translate the Events it emits into their own UI representation
// — a Bubble Tea message chain for the TUI, a WebSocket message for the Web
// UI — rather than each re-implementing the loop itself.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	goai "github.com/zendev-sh/goai"
	goaimcp "github.com/zendev-sh/goai/mcp"
	"github.com/zendev-sh/goai/provider"

	"github.com/mfranz/elastic-security-mcp/internal/llmobs"
	"github.com/mfranz/elastic-security-mcp/internal/util"
)

// SystemPrompt steers tool selection and response style for every front end.
const SystemPrompt = `You are a silent Elastic Security analyst tool.
YOUR ONLY JOB IS TO CALL TOOLS.
NEVER explain what you are doing.
NEVER say "I will search" or "Let me check" or "Now I'll".
IF YOU NEED DATA, CALL THE APPROPRIATE SEARCH OR LOOKUP TOOL IMMEDIATELY.
DO NOT PROVIDE ANY TEXT UNTIL YOU HAVE THE RESULTS.
ALWAYS use Markdown tables for tabular data.

DO ASK FOLLOW-UP QUESTIONS IF GUIDANCE IS NOT CLEAR.
DO NOT JUST START SEARCHING IF YOU ARE NOT GIVEN CLEAR GUIDANCE.

TOOL SELECTION GUIDE — call the right tool immediately:
- search_security_alerts: detection alerts from Elastic Security rules
- search_processes: endpoint process events (automatically searches logs-endpoint.events.process-*)
- search_security_events: network and endpoint events — use index logs-zeek.*-* for Zeek, logs-suricata.*-* for Suricata, packetbeat-* for Packetbeat, logs-endpoint.events.network-* or logs-endpoint.events.file-* for endpoint
- search_security_stats: prefer this for ONE bounded telemetry question — top values (terms), an event-rate timeline (date_histogram), or an approximate unique count (cardinality) — instead of search_elastic. Always pass an explicit RFC3339 start/end window; use a coarser interval (e.g. 1h instead of 1m) if the requested timeline is too granular for the window. Use aggregatable fields and add .keyword for analyzed text (e.g. dns.question.name.keyword)
- list_indices: discover available indices before searching if unsure
- list_kibana_spaces: discover or list available Kibana spaces
- list_detection_rules / get_detection_rule: inspect or browse detection rules
- list_agents: check Elastic Agent / Fleet status
- lookup_domain / lookup_ip: fast DNS history lookup from cache
- search_elastic: ONLY for raw Elasticsearch JSON DSL that no other tool can express — including multiple, nested, or otherwise raw aggregations that search_security_stats can't (it only runs one terms/date_histogram/cardinality aggregation at a time). Prefer exact term filters on process.executable, process.name, and process.args; avoid leading wildcards on command lines or executable paths. Constrain unavoidable wildcards by host and time. Combine related metrics into one filters aggregation rather than repeatedly scanning the same range. Aggregation-only size=0 searches default to track_total_hits=false; request exact totals only when needed
- kibana_api_request: ONLY for Kibana API endpoints not covered by other tools`

const maxLoggedPayloadChars = 4000

// stallCorrection is appended to history (invisibly to the user) when the
// model narrates instead of acting, to nudge it toward calling a tool.
const stallCorrection = "Please proceed with the tool call immediately. Do not narrate your intent."

// EventKind identifies what an Event carries.
type EventKind int

const (
	EventStatus EventKind = iota
	EventToolStart
	EventToolEnd
	EventAssistant
)

// ToolCallEvent describes one tool call's lifecycle. It is sent once with
// State == "running" before the call, and once more with the final state
// ("completed" or "error") after the call returns.
type ToolCallEvent struct {
	Call     provider.ToolCall
	Seq      int
	Args     map[string]any
	State    string
	Result   string
	IsError  bool
	IsCached bool
	IsStored bool
}

// Event is emitted by Engine.Turn as it drives one user turn — possibly
// spanning several tool-calling round trips — to completion.
type Event struct {
	Kind   EventKind
	Status string
	Tool   *ToolCallEvent
	Text   string
}

// TurnResult is returned once Engine.Turn stops looping.
type TurnResult struct {
	History []provider.Message
	Err     error
}

// Engine drives the agentic tool-calling loop against an MCP client and an
// LLM provider. It holds no per-conversation state, so a single Engine can
// be reused (or even shared) across concurrent conversations — history flows
// in and out of Turn by value.
type Engine struct {
	MCPClient *goaimcp.Client
	Model     provider.LanguageModel
	Tools     []goai.Tool
	ModelName string // optional, used only for debug logging
}

func New(mcpClient *goaimcp.Client, model provider.LanguageModel, tools []goai.Tool, modelName string) *Engine {
	return &Engine{MCPClient: mcpClient, Model: model, Tools: tools, ModelName: modelName}
}

// Turn drives the conversation forward from the given history until the
// model produces a final, non-narrating answer with no further tool calls,
// or an error occurs. It emits Events as it goes so callers can render
// incremental progress; emit is called synchronously and must not block for
// long, since Turn will not proceed to the next step until it returns.
//
// Turn never mutates the caller's history slice — it works from an internal
// copy and returns the final result via TurnResult.History.
func (e *Engine) Turn(ctx context.Context, history []provider.Message, emit func(Event)) TurnResult {
	hist := append([]provider.Message(nil), history...)

	for {
		emit(Event{Kind: EventStatus, Status: "Analyzing request..."})

		slog.Debug("LLM request summary",
			"model", e.ModelName,
			"tool_count", len(e.Tools),
			"message_count", len(hist),
			"summary", summarizeHistoryForLog(hist),
		)

		result, err := util.WithRetry(ctx, func() (*goai.TextResult, error) {
			opts := append(llmobs.Hooks(),
				goai.WithMessages(hist...),
				goai.WithSystem(SystemPrompt),
				goai.WithTools(e.Tools...),
				goai.WithTemperature(0),
				goai.WithMaxOutputTokens(4096),
			)
			return goai.GenerateText(ctx, e.Model, opts...)
		})
		if err != nil {
			slog.Error("LLM generation error", "error", err)
			return TurnResult{History: hist, Err: err}
		}
		if result == nil {
			err := fmt.Errorf("LLM returned no response")
			return TurnResult{History: hist, Err: err}
		}

		hist = append(hist, result.ResponseMessages...)

		respText := result.Text
		toolCalls := result.ToolCalls

		if len(toolCalls) == 0 && isStalling(respText) {
			hist = append(hist, goai.UserMessage(stallCorrection))
			continue
		}

		if len(toolCalls) == 0 {
			if respText != "" {
				emit(Event{Kind: EventAssistant, Text: respText})
			}
			return TurnResult{History: hist}
		}

		emit(Event{Kind: EventStatus, Status: summarizeToolCalls(toolCalls)})

		for i, tc := range toolCalls {
			toolMsg := e.callTool(ctx, i+1, tc, emit)
			hist = append(hist, toolMsg)
		}

		emit(Event{Kind: EventStatus, Status: "Tool results received. Drafting final answer..."})
	}
}

// callTool executes a single tool call, emitting EventToolStart before and
// EventToolEnd after, and returns the ToolMessage to append to history.
func (e *Engine) callTool(ctx context.Context, seq int, tc provider.ToolCall, emit func(Event)) provider.Message {
	if tc.Name == "" {
		const errText = "invalid tool call: missing function name"
		emit(Event{Kind: EventToolStart, Tool: &ToolCallEvent{Call: tc, Seq: seq, State: "running"}})
		emit(Event{Kind: EventToolEnd, Tool: &ToolCallEvent{Call: tc, Seq: seq, State: "error", Result: errText, IsError: true}})
		return goai.ToolMessage(tc.ID, tc.Name, errText)
	}

	var args map[string]any
	if len(tc.Input) > 0 {
		if err := json.Unmarshal(tc.Input, &args); err != nil {
			errText := fmt.Sprintf("invalid tool arguments: %v", err)
			emit(Event{Kind: EventToolStart, Tool: &ToolCallEvent{Call: tc, Seq: seq, State: "running"}})
			emit(Event{Kind: EventToolEnd, Tool: &ToolCallEvent{Call: tc, Seq: seq, State: "error", Result: errText, IsError: true}})
			return goai.ToolMessage(tc.ID, tc.Name, errText)
		}
	}

	emit(Event{Kind: EventToolStart, Tool: &ToolCallEvent{Call: tc, Seq: seq, Args: args, State: "running"}})

	slog.Info("Executing tool", "name", tc.Name, "arg_chars", len(tc.Input), "id", tc.ID)
	if util.ClientPayloadLoggingEnabled() {
		slog.Debug("Tool arguments", "name", tc.Name, "args", util.TruncateForLog(string(tc.Input), maxLoggedPayloadChars))
	}

	start := time.Now()
	toolResp, callErr := e.MCPClient.CallTool(ctx, tc.Name, args)
	latencyMs := time.Since(start).Milliseconds()

	var resultText string
	isError := callErr != nil || (toolResp != nil && toolResp.IsError)
	switch {
	case callErr != nil:
		slog.Error("Tool call error", "name", tc.Name, "latency_ms", latencyMs, "error", callErr)
		resultText = fmt.Sprintf("error calling tool: %v", callErr)
	case toolResp.IsError:
		resultText = extractToolText(toolResp)
		if strings.TrimSpace(resultText) == "" {
			resultText = "tool returned an error"
		}
		slog.Warn("Tool returned error status",
			"name", tc.Name,
			"latency_ms", latencyMs,
			"error_preview", util.TruncateForLog(resultText, 500),
		)
	default:
		resultText = extractToolText(toolResp)
		slog.Debug("Tool execution successful",
			"name", tc.Name,
			"latency_ms", latencyMs,
			"result_len", len(resultText),
			"result_preview", util.TruncateForLog(resultText, 500),
		)
	}

	resultText, isCached, isStored := normalizeToolResultText(resultText)

	finalState := "completed"
	if isError {
		finalState = "error"
	}
	emit(Event{Kind: EventToolEnd, Tool: &ToolCallEvent{
		Call: tc, Seq: seq, Args: args, State: finalState, Result: resultText,
		IsError: isError, IsCached: isCached, IsStored: isStored,
	}})

	return goai.ToolMessage(tc.ID, tc.Name, resultText)
}

func isStalling(respText string) bool {
	content := strings.ToLower(respText)
	return strings.Contains(content, "i will") ||
		strings.Contains(content, "let me") ||
		strings.Contains(content, "now i'll") ||
		strings.Contains(content, "searching")
}

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

	switch len(names) {
	case 0:
		return fmt.Sprintf("Running %d tool call(s)...", len(toolCalls))
	case 1:
		return fmt.Sprintf("Running `%s`...", names[0])
	case 2:
		return fmt.Sprintf("Running `%s` and `%s`...", names[0], names[1])
	default:
		return fmt.Sprintf("Running %d tool calls (%s, %s, ...)...", len(names), names[0], names[1])
	}
}

func normalizeToolResultText(text string) (clean string, isCached bool, isStored bool) {
	switch {
	case strings.HasPrefix(text, "✓ "):
		return strings.TrimPrefix(text, "✓ "), true, false
	case strings.HasPrefix(text, "↓ "):
		return strings.TrimPrefix(text, "↓ "), false, true
	default:
		return text, false, false
	}
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

// RenderHistoryText renders the text parts of history as "Human:"/"AI:"
// lines, for the /memory debug command.
func RenderHistoryText(history []provider.Message) string {
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

func summarizeHistoryForLog(history []provider.Message) string {
	type partSummary map[string]any
	type messageSummary map[string]any

	summary := make([]messageSummary, 0, len(history))
	for i, msg := range history {
		var parts []partSummary
		for _, p := range msg.Content {
			switch p.Type {
			case provider.PartText:
				parts = append(parts, partSummary{
					"type":    "text",
					"chars":   len(p.Text),
					"preview": util.TruncateForLog(p.Text, 160),
				})
			case provider.PartToolCall:
				parts = append(parts, partSummary{
					"type":      "tool_call",
					"name":      p.ToolName,
					"id":        p.ToolCallID,
					"arg_chars": len(p.ToolInput),
					"args":      util.TruncateForLog(string(p.ToolInput), 240),
				})
			case provider.PartToolResult:
				parts = append(parts, partSummary{
					"type":         "tool_result",
					"tool_call_id": p.ToolCallID,
					"chars":        len(p.ToolOutput),
					"preview":      util.TruncateForLog(p.ToolOutput, 240),
				})
			}
		}
		summary = append(summary, messageSummary{
			"index": i,
			"role":  string(msg.Role),
			"parts": parts,
		})
	}
	b, err := json.Marshal(summary)
	if err != nil {
		return fmt.Sprintf("failed to summarize history: %v", err)
	}
	return string(b)
}
