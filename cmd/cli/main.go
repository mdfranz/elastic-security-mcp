package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	goai "github.com/zendev-sh/goai"
	goaimcp "github.com/zendev-sh/goai/mcp"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/anthropic"
	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/goai/provider/openai"
	"github.com/mfranz/elastic-security-mcp/internal/util"
	"github.com/mfranz/elastic-security-mcp/internal/webui"
	"github.com/spf13/cobra"
)

const systemPrompt = `You are a silent Elastic Security analyst tool.
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
- list_indices: discover available indices before searching if unsure
- list_kibana_spaces: discover or list available Kibana spaces
- list_detection_rules / get_detection_rule: inspect or browse detection rules
- list_agents: check Elastic Agent / Fleet status
- lookup_domain / lookup_ip: fast DNS history lookup from cache
- search_elastic: ONLY for raw Elasticsearch JSON DSL that no other tool can express
- kibana_api_request: ONLY for Kibana API endpoints not covered by other tools`

const maxLoggedPayloadChars = 4000
const maxHistoryMessages = 15
const footerReserveLines = 9

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#005FB8")).
			MarginBottom(1)

	userStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00D7D7"))

	assistantStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#5F00FF"))

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00D787")).
			Bold(true)

	toolJSONStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#878787")).
			Italic(true)

	statusStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#B85F00"))

	dividerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#5F87AF"))

	footerLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6D7E97"))

	footerValueStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#E6EEF8"))

	footerSeparatorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#5F87AF"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF0000"))

	systemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A8A8A8"))
)

// Messages
type generateMsg struct{}
type llmResponseMsg struct {
	result *goai.TextResult
}
type executeToolsMsg struct {
	toolCalls []provider.ToolCall
}
type toolsResultMsg struct {
	results  []provider.Message
	outcomes []toolOutcome
}
type errMsg struct {
	err error
}

type toolOutcome struct {
	isCached bool
	isStored bool
	isError  bool
}

type exportMessage struct {
	role    string
	content string
}

type focusArea int

const (
	focusInput focusArea = iota
	focusOutput
)

type model struct {
	ctx       context.Context
	mcpClient *goaimcp.Client
	llmModel  provider.LanguageModel
	tools     []goai.Tool
	history   []provider.Message
	modelName string
	useMemory bool
	lastInput  string
	inputHist  []string
	histIndex  int
	histDraft  string

	viewport  viewport.Model
	textInput textinput.Model
	spinner   spinner.Model
	renderer  *glamour.TermRenderer
	isDark    bool
	focus     focusArea

	messages     []string
	conversation []exportMessage
	isThinking   bool
	statusText   string
	toolCalls    int
	cacheHits    int
	cacheMisses  int
	cacheStores  int
	toolErrors   int
	err          error
	ready        bool
}

func initialModel(ctx context.Context, mcpClient *goaimcp.Client, llmModel provider.LanguageModel, tools []goai.Tool, modelName string, useMemory bool) model {
	ti := textinput.New()
	ti.Placeholder = "Ask about security data..."
	ti.Focus()
	ti.CharLimit = 1024
	ti.Width = 80

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	isDark := lipgloss.HasDarkBackground()
	style := "light"
	if isDark {
		style = "dark"
	}

	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(0),
	)

	return model{
		ctx:       ctx,
		mcpClient: mcpClient,
		llmModel:  llmModel,
		tools:     tools,
		modelName: modelName,
		useMemory: useMemory,
		inputHist: loadHistory(),
		histIndex: -1,
		textInput: ti,
		spinner:   s,
		renderer:  renderer,
		isDark:    isDark,
		focus:     focusInput,
		history:   []provider.Message{},
		messages:  []string{},
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

func (m *model) refreshViewport(follow bool) {
	if !m.ready {
		return
	}
	shouldFollow := follow || m.viewport.AtBottom()
	m.viewport.SetContent(strings.Join(m.messages, "\n"))
	if shouldFollow {
		m.viewport.GotoBottom()
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}

	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func dividerLine(width int) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat(lipgloss.NormalBorder().Top, width)
}

func exportLabel(role string) string {
	switch role {
	case "user":
		return "You"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	default:
		return role
	}
}

func buildMarkdownExport(conversation []exportMessage, exportedAt time.Time) string {
	var b strings.Builder
	b.WriteString("# Elastic Security Investigation Export\n\n")
	b.WriteString(fmt.Sprintf("*Exported on: %s*\n\n---\n\n", exportedAt.Format(time.RFC1123)))
	for _, msg := range conversation {
		b.WriteString(fmt.Sprintf("**%s:**\n%s\n\n", exportLabel(msg.role), msg.content))
	}
	return b.String()
}

func exportFilename(now time.Time) string {
	return fmt.Sprintf("investigation-export-%s.md", now.Format("2006-01-02T15-04-05"))
}

func normalizeMarkdownForTerminal(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" {
			continue
		}

		hashes := 0
		for hashes < len(trimmed) && hashes < 6 && trimmed[hashes] == '#' {
			hashes++
		}
		if hashes == 0 || hashes >= len(trimmed) || trimmed[hashes] != ' ' {
			continue
		}

		indent := len(line) - len(trimmed)
		lines[i] = strings.Repeat(" ", indent) + strings.TrimSpace(trimmed[hashes:])
	}
	return strings.Join(lines, "\n")
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

func (m *model) appendConversation(role, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	m.conversation = append(m.conversation, exportMessage{
		role:    role,
		content: content,
	})
}

func (m *model) exportConversation() error {
	if len(m.conversation) == 0 {
		return errors.New("no conversation to export")
	}

	now := time.Now()
	filename := exportFilename(now)
	path, err := filepath.Abs(filename)
	if err != nil {
		return fmt.Errorf("resolve export path: %w", err)
	}

	md := buildMarkdownExport(m.conversation, now)
	if err := os.WriteFile(path, []byte(md), 0644); err != nil {
		return fmt.Errorf("write export: %w", err)
	}

	m.messages = append(m.messages, fmt.Sprintf("%s\n%s", systemStyle.Render("Export saved:"), path))
	return nil
}

func footerMetaSegment(label string, value any) string {
	return footerLabelStyle.Render(label+": ") + footerValueStyle.Render(fmt.Sprint(value))
}

func (m model) renderFooterMetaLine(width int) string {
	session := "Ready"
	if m.isThinking {
		session = "Investigating"
	}

	memoryState := "Off"
	if m.useMemory {
		memoryState = "On"
	}

	parts := []string{
		footerMetaSegment("Session", session),
		footerMetaSegment("Model", m.modelName),
		footerMetaSegment("Memory", memoryState),
		footerMetaSegment("Tools", m.toolCalls),
		footerMetaSegment("Cache", fmt.Sprintf("%d hit / %d miss / %d store / %d error", m.cacheHits, m.cacheMisses, m.cacheStores, m.toolErrors)),
	}

	line := strings.Join(parts, footerSeparatorStyle.Render("  "))
	return lipgloss.NewStyle().MaxWidth(width).Render(line)
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

func (m *model) pushInputHistory(input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}
	if n := len(m.inputHist); n > 0 && m.inputHist[n-1] == input {
		m.histIndex = -1
		m.histDraft = ""
		return
	}
	m.inputHist = append(m.inputHist, input)
	saveHistory(input)
	m.histIndex = -1
	m.histDraft = ""
}

func (m *model) browseHistory(delta int) {
	if len(m.inputHist) == 0 {
		return
	}

	if m.histIndex == -1 {
		m.histDraft = m.textInput.Value()
		if delta < 0 {
			m.histIndex = len(m.inputHist) - 1
		} else {
			return
		}
	} else {
		m.histIndex += delta
		if m.histIndex < 0 {
			m.histIndex = 0
		}
		if m.histIndex >= len(m.inputHist) {
			m.histIndex = -1
			m.textInput.SetValue(m.histDraft)
			m.textInput.SetCursor(len([]rune(m.histDraft)))
			return
		}
	}

	m.textInput.SetValue(m.inputHist[m.histIndex])
	m.textInput.SetCursor(len([]rune(m.inputHist[m.histIndex])))
}

func (m *model) pruneHistory() {
	if len(m.history) <= maxHistoryMessages {
		return
	}
	m.history = m.history[len(m.history)-maxHistoryMessages:]
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
		spCmd tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyTab:
			if m.focus == focusInput {
				m.focus = focusOutput
				m.textInput.Blur()
			} else {
				m.focus = focusInput
				m.textInput.Focus()
			}
			return m, nil

		case tea.KeyUp:
			if m.focus == focusInput {
				m.browseHistory(-1)
			} else {
				m.viewport.LineUp(1)
			}
			return m, nil

		case tea.KeyDown:
			if m.focus == focusInput {
				m.browseHistory(1)
			} else {
				m.viewport.LineDown(1)
			}
			return m, nil

		case tea.KeyPgUp:
			m.viewport.HalfViewUp()
			return m, nil

		case tea.KeyPgDown:
			m.viewport.HalfViewDown()
			return m, nil

		case tea.KeyEnter:
			if m.focus != focusInput {
				return m, nil
			}
			input := m.textInput.Value()
			if input == "" {
				return m, nil
			}

			// Handle /memory command
			if input == "/memory" {
				m.pushInputHistory(input)
				m.textInput.SetValue("")
				if !m.useMemory {
					msg := "Conversation memory is disabled."
					m.messages = append(m.messages, systemStyle.Render(msg))
					m.appendConversation("system", msg)
				} else {
					hist := renderHistoryText(m.history)
					if hist == "" {
						hist = "(empty)"
					}
					msg := fmt.Sprintf("Conversation Memory:\n%s", hist)
					m.messages = append(m.messages, fmt.Sprintf("%s\n%s", systemStyle.Render("Conversation Memory:"), hist))
					m.appendConversation("system", msg)
				}
				m.refreshViewport(true)
				return m, nil
			}

			if input == "/export" {
				m.pushInputHistory(input)
				m.textInput.SetValue("")
				if err := m.exportConversation(); err != nil {
					m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Export error: %v", err)))
				}
				m.refreshViewport(true)
				return m, nil
			}

			// Wrap human input
			wrappedUser := lipgloss.NewStyle().Width(m.viewport.Width - 10).Render(input)
			m.messages = append(m.messages, fmt.Sprintf("%s %s", userStyle.Render("You:"), wrappedUser))
			m.appendConversation("user", input)

			m.history = append(m.history, goai.UserMessage(input))

			if !m.useMemory {
				m.pruneHistory()
			}

			m.pushInputHistory(input)
			m.lastInput = input
			m.textInput.SetValue("")
			m.isThinking = true
			m.statusText = "Analyzing request..."
			m.refreshViewport(true)

			return m, m.generateResponse()
		}

	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-footerReserveLines)
			m.viewport.HighPerformanceRendering = false
			m.viewport.SetContent(strings.Join(m.messages, "\n"))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - footerReserveLines
		}
		// Update renderer width without re-querying terminal
		style := "light"
		if m.isDark {
			style = "dark"
		}
		m.renderer, _ = glamour.NewTermRenderer(
			glamour.WithStandardStyle(style),
			glamour.WithWordWrap(msg.Width-4),
		)
		return m, nil

	case spinner.TickMsg:
		m.spinner, spCmd = m.spinner.Update(msg)
		return m, spCmd

	case llmResponseMsg:
		if msg.result == nil {
			m.err = errors.New("LLM returned no response")
			m.isThinking = false
			m.statusText = ""
			m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
			m.refreshViewport(false)
			return m, nil
		}

		result := msg.result

		// Append assistant turn to history.
		m.history = append(m.history, result.ResponseMessages...)

		respText := result.Text
		toolCalls := result.ToolCalls

		// Detect stalling (model narrates instead of calling tools).
		content := strings.ToLower(respText)
		if len(toolCalls) == 0 && (strings.Contains(content, "i will") ||
			strings.Contains(content, "let me") ||
			strings.Contains(content, "now i'll") ||
			strings.Contains(content, "searching")) {

			m.history = append(m.history, goai.UserMessage("Please proceed with the tool call immediately. Do not narrate your intent."))
			return m, m.generateResponse()
		}

		// Display text content.
		if respText != "" && len(toolCalls) == 0 {
			rendered, err := m.renderer.Render(normalizeMarkdownForTerminal(respText))
			if err != nil {
				rendered = respText
			}
			m.messages = append(m.messages, fmt.Sprintf("%s\n%s", assistantStyle.Render("Assistant:"), rendered))
			m.appendConversation("assistant", respText)
		}

		// Handle tool calls.
		if len(toolCalls) > 0 {
			m.statusText = summarizeToolCalls(toolCalls) + " Tool lines above are intermediate."
			for _, tc := range toolCalls {
				header := toolStyle.Render(fmt.Sprintf("[%s] args:", tc.Name))
				body := toolJSONStyle.Copy().Width(m.viewport.Width).Render(formatToolCallArguments(tc))
				m.messages = append(m.messages, header+"\n"+body+"\n")
			}
			m.refreshViewport(false)
			return m, m.executeTools(toolCalls)
		}

		m.lastInput = ""
		m.isThinking = false
		m.statusText = ""
		m.refreshViewport(false)
		return m, nil

	case toolsResultMsg:
		for _, res := range msg.results {
			m.history = append(m.history, res)
		}
		for _, outcome := range msg.outcomes {
			m.toolCalls++
			if outcome.isCached {
				m.cacheHits++
			} else {
				m.cacheMisses++
			}
			if outcome.isStored {
				m.cacheStores++
			}
			if outcome.isError {
				m.toolErrors++
			}
		}
		m.statusText = "Tool results received. Drafting final answer..."
		m.refreshViewport(false)
		return m, m.generateResponse()

	case errMsg:
		m.err = msg.err
		m.isThinking = false
		m.statusText = ""
		m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
		m.refreshViewport(false)
		return m, nil
	}

	m.textInput, tiCmd = m.textInput.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)
	return m, tea.Batch(tiCmd, vpCmd)
}

func (m model) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}

	var s string
	width := m.viewport.Width
	s += m.viewport.View() + "\n\n"
	s += dividerStyle.Render(dividerLine(width)) + "\n"
	s += m.renderFooterMetaLine(width) + "\n"

	status := m.statusText
	if status == "" {
		if m.isThinking {
			status = "Thinking..."
		} else {
			status = "Ready for the next investigation."
		}
	}
	if m.isThinking {
		prefix := m.spinner.View() + " "
		s += statusStyle.Render(prefix+truncateRunes(status, width-lipgloss.Width(prefix))) + "\n"
	} else {
		s += statusStyle.Render(truncateRunes(status, width)) + "\n"
	}

	help := "Up/Down: history  PgUp/PgDn: scroll  TAB: focus output"
	if m.focus == focusOutput {
		help = "UP/DOWN: scroll output  PgUp/PgDn: scroll  TAB: focus input"
	}
	s += systemStyle.Render(help) + "\n"
	s += m.textInput.View()

	return s
}

func formatToolCallArguments(tc provider.ToolCall) string {
	if len(tc.Input) == 0 {
		return "{}"
	}

	var parsed map[string]any
	if err := json.Unmarshal(tc.Input, &parsed); err != nil {
		return string(tc.Input)
	}

	// Expand inner JSON strings (e.g. for search_elastic query field).
	for k, v := range parsed {
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(s)
			if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
				(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
				var inner any
				if err := json.Unmarshal([]byte(s), &inner); err == nil {
					parsed[k] = inner
				}
			}
		}
	}

	formatted, err := json.MarshalIndent(parsed, "", " ")
	if err != nil {
		return string(tc.Input)
	}

	lines := strings.Split(string(formatted), "\n")
	if len(lines) <= 1 {
		return string(formatted)
	}

	var result []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if (trimmed == "}" || trimmed == "}," || trimmed == "]" || trimmed == "],") && len(result) > 0 {
			result[len(result)-1] += " " + trimmed
			continue
		}
		if (strings.HasSuffix(trimmed, "{") || strings.HasSuffix(trimmed, "[")) && i+1 < len(lines) {
			nextLine := strings.TrimSpace(lines[i+1])
			if nextLine == "}" || nextLine == "}," || nextLine == "]" || nextLine == "]," {
				result = append(result, line+" "+nextLine)
				i++
				continue
			}
			result = append(result, line+" "+nextLine)
			i++
			continue
		}
		result = append(result, line)
	}

	var final []string
	for _, line := range result {
		trimmed := strings.TrimSpace(line)
		if (trimmed == "}" || trimmed == "}," || trimmed == "]" || trimmed == "],") && len(final) > 0 {
			final[len(final)-1] += " " + trimmed
		} else {
			final = append(final, line)
		}
	}

	return strings.Join(final, "\n")
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

func (m model) generateResponse() tea.Cmd {
	return func() tea.Msg {
		slog.Debug("LLM request summary",
			"model", m.modelName,
			"tool_count", len(m.tools),
			"message_count", len(m.history),
			"summary", summarizeHistoryForLog(m.history),
		)

		result, err := util.WithRetry(m.ctx, func() (*goai.TextResult, error) {
			return goai.GenerateText(m.ctx, m.llmModel,
				goai.WithMessages(m.history...),
				goai.WithSystem(systemPrompt),
				goai.WithTools(m.tools...),
				goai.WithTemperature(0),
				goai.WithMaxOutputTokens(4096),
			)
		})
		if err != nil {
			slog.Error("LLM generation error", "error", err)
			return errMsg{err}
		}

		return llmResponseMsg{result}
	}
}

func (m model) executeTools(toolCalls []provider.ToolCall) tea.Cmd {
	return func() tea.Msg {
		toolResultMessages := []provider.Message{}
		outcomes := make([]toolOutcome, 0, len(toolCalls))

		for _, tc := range toolCalls {
			slog.Info("Executing tool", "name", tc.Name, "arg_chars", len(tc.Input), "id", tc.ID)
			if util.ClientPayloadLoggingEnabled() {
				slog.Debug("Tool arguments", "name", tc.Name, "args", util.TruncateForLog(string(tc.Input), maxLoggedPayloadChars))
			}

			if tc.Name == "" {
				toolResultMessages = append(toolResultMessages,
					goai.ToolMessage(tc.ID, tc.Name, "invalid tool call: missing function name"))
				outcomes = append(outcomes, toolOutcome{isError: true})
				continue
			}

			var args map[string]any
			if len(tc.Input) > 0 {
				if err := json.Unmarshal(tc.Input, &args); err != nil {
					toolResultMessages = append(toolResultMessages,
						goai.ToolMessage(tc.ID, tc.Name, fmt.Sprintf("invalid tool arguments: %v", err)))
					outcomes = append(outcomes, toolOutcome{isError: true})
					continue
				}
			}

			toolResp, err := m.mcpClient.CallTool(m.ctx, tc.Name, args)

			var resultText string
			isError := err != nil || (toolResp != nil && toolResp.IsError)
			switch {
			case err != nil:
				slog.Error("Tool call error", "name", tc.Name, "error", err)
				resultText = fmt.Sprintf("error calling tool: %v", err)
			case toolResp != nil && toolResp.IsError:
				resultText = extractToolText(toolResp)
				if strings.TrimSpace(resultText) == "" {
					resultText = "tool returned an error"
				}
				slog.Warn("Tool returned error status",
					"name", tc.Name,
					"error_preview", util.TruncateForLog(resultText, 500),
				)
			default:
				resultText = extractToolText(toolResp)
				slog.Debug("Tool execution successful",
					"name", tc.Name,
					"result_len", len(resultText),
					"result_preview", util.TruncateForLog(resultText, 500),
				)
			}

			resultText, isCached, isStored := normalizeToolResultText(resultText)
			outcomes = append(outcomes, toolOutcome{
				isCached: isCached,
				isStored: isStored,
				isError:  isError,
			})

			toolResultMessages = append(toolResultMessages, goai.ToolMessage(tc.ID, tc.Name, resultText))
		}

		return toolsResultMsg{results: toolResultMessages, outcomes: outcomes}
	}
}

type item struct {
	title, desc string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }
func (i item) FilterValue() string { return i.title }

type modelSelector struct {
	list     list.Model
	choice   string
	quitting bool
}

func (m modelSelector) Init() tea.Cmd {
	return nil
}

func (m modelSelector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d", "q":
			m.quitting = true
			return m, tea.Quit

		case "enter":
			i, ok := m.list.SelectedItem().(item)
			if ok {
				m.choice = i.title
			}
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m modelSelector) View() string {
	if m.choice != "" || m.quitting {
		return ""
	}
	return "\n" + m.list.View()
}

func configureSelectorList(items []list.Item, title string, width, height int) list.Model {
	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.
		Foreground(lipgloss.Color("#A8A8A8"))
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.
		Foreground(lipgloss.Color("#6D7E97"))
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Bold(true).
		Foreground(lipgloss.Color("#00D7D7")).
		BorderForeground(lipgloss.Color("#005FB8"))
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(lipgloss.Color("#8FB7D8")).
		BorderForeground(lipgloss.Color("#005FB8"))
	delegate.Styles.DimmedTitle = delegate.Styles.DimmedTitle.
		Foreground(lipgloss.Color("#6D7E97"))
	delegate.Styles.DimmedDesc = delegate.Styles.DimmedDesc.
		Foreground(lipgloss.Color("#5C6A7C"))
	delegate.Styles.FilterMatch = delegate.Styles.FilterMatch.
		Bold(true).
		Foreground(lipgloss.Color("#00D7D7"))

	l := list.New(items, delegate, width, height)
	l.Title = title
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.Styles.Title = titleStyle
	l.Styles.TitleBar = l.Styles.TitleBar.
		PaddingLeft(0).
		PaddingBottom(1)
	l.Styles.PaginationStyle = l.Styles.PaginationStyle.
		Foreground(lipgloss.Color("#6D7E97"))
	l.Styles.HelpStyle = l.Styles.HelpStyle.
		Foreground(lipgloss.Color("#6D7E97"))
	l.Styles.ActivePaginationDot = l.Styles.ActivePaginationDot.
		Foreground(lipgloss.Color("#00D7D7"))
	l.Styles.InactivePaginationDot = l.Styles.InactivePaginationDot.
		Foreground(lipgloss.Color("#5C6A7C"))
	l.Styles.DividerDot = l.Styles.DividerDot.
		Foreground(lipgloss.Color("#005FB8"))
	l.Styles.StatusEmpty = l.Styles.StatusEmpty.
		Foreground(lipgloss.Color("#6D7E97"))
	l.Styles.NoItems = l.Styles.NoItems.
		Foreground(lipgloss.Color("#6D7E97"))

	return l
}

func modelProvider(modelName string) string {
	switch {
	case strings.HasPrefix(modelName, "gpt-"), strings.HasPrefix(modelName, "o1-"), strings.HasPrefix(modelName, "o3-"):
		return "openai"
	case strings.HasPrefix(modelName, "claude-"):
		return "anthropic"
	case strings.HasPrefix(modelName, "gemini-"):
		return "gemini"
	default:
		return ""
	}
}

func main() {
	var modelFlag string
	var memoryFlag bool
	var promptFlag string
	var webuiFlag bool
	var portFlag int

	rootCmd := &cobra.Command{
		Use:   "elastic-cli",
		Short: "Elastic Security Assistant CLI",
		Run: func(cmd *cobra.Command, args []string) {
			if promptFlag != "" {
				runSinglePrompt(modelFlag, promptFlag)
			} else if webuiFlag {
				runWebUI(modelFlag, memoryFlag, portFlag)
			} else {
				runApp(modelFlag, memoryFlag)
			}
		},
	}

	rootCmd.Flags().StringVarP(&modelFlag, "model", "m", "", "Model ID to use (e.g. gpt-5, claude-3-7-sonnet-latest)")
	rootCmd.Flags().BoolVar(&memoryFlag, "memory", true, "Enable conversation memory")
	rootCmd.Flags().StringVarP(&promptFlag, "prompt", "p", "", "Run a single prompt and exit")
	rootCmd.Flags().BoolVar(&webuiFlag, "webui", false, "Start optional Web UI")
	rootCmd.Flags().IntVar(&portFlag, "port", 8080, "Port for Web UI")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runSinglePrompt(modelFlag string, prompt string) {
	logFile := util.ClientLogFile()

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", logFile, err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: util.ClientLogLevel()}))
	slog.SetDefault(logger)
	defer f.Close()

	ctx := context.Background()

	serverPath := os.Getenv("ELASTIC_MCP_SERVER")
	if serverPath == "" {
		serverPath = "./elastic-mcp-server"
	}

	modelName := modelFlag
	if modelName == "" {
		modelName = os.Getenv("ELASTIC_MODEL")
	}
	if modelName == "" {
		modelName = "gemini-2.0-flash"
	}

	openaiKey := os.Getenv("OPENAI_API_KEY")
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")

	var llmModel provider.LanguageModel
	switch modelProvider(modelName) {
	case "openai":
		if openaiKey == "" {
			fmt.Fprintln(os.Stderr, "OPENAI_API_KEY is required for openai models")
			os.Exit(1)
		}
		llmModel = openai.Chat(modelName)
	case "anthropic":
		if anthropicKey == "" {
			fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is required for anthropic models")
			os.Exit(1)
		}
		llmModel = anthropic.Chat(modelName)
	case "gemini":
		if geminiKey == "" {
			fmt.Fprintln(os.Stderr, "GEMINI_API_KEY is required for gemini models")
			os.Exit(1)
		}
		llmModel = google.Chat(modelName)
	default:
		fmt.Fprintf(os.Stderr, "Unsupported model prefix: %s\n", modelName)
		os.Exit(1)
	}

	oneshotTransport := &goaimcp.StdioTransport{Command: serverPath}
	mcpClient := goaimcp.NewClient("elastic-cli-oneshot", "1.0.0",
		goaimcp.WithTransport(oneshotTransport),
	)
	oneshotTransport.OnClose(func() { mcpClient.Close() })
	if err := mcpClient.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to MCP server: %v\n", err)
		os.Exit(1)
	}
	defer mcpClient.Close()

	toolsResult, err := mcpClient.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list tools: %v\n", err)
		os.Exit(1)
	}

	tools := make([]goai.Tool, 0, len(toolsResult.Tools))
	toolNames := make([]string, 0, len(toolsResult.Tools))
	for _, t := range toolsResult.Tools {
		toolNames = append(toolNames, t.Name)
		tools = append(tools, goai.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	slog.Info("Discovered tools", "count", len(tools), "names", toolNames)

	history := []provider.Message{goai.UserMessage(prompt)}

	for {
		result, err := util.WithRetry(ctx, func() (*goai.TextResult, error) {
			return goai.GenerateText(ctx, llmModel,
				goai.WithMessages(history...),
				goai.WithSystem(systemPrompt),
				goai.WithTools(tools...),
			)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Generation error: %v\n", err)
			os.Exit(1)
		}

		history = append(history, result.ResponseMessages...)

		if result.Text != "" {
			fmt.Println(result.Text)
		}

		if len(result.ToolCalls) == 0 {
			break
		}

		for _, tc := range result.ToolCalls {
			fmt.Printf("Calling tool: %s\n", tc.Name)

			var args map[string]any
			if len(tc.Input) > 0 {
				if err := json.Unmarshal(tc.Input, &args); err != nil {
					history = append(history, goai.ToolMessage(tc.ID, tc.Name, fmt.Sprintf("invalid arguments: %v", err)))
					continue
				}
			}

			toolResp, err := mcpClient.CallTool(ctx, tc.Name, args)
			resultText := ""
			if err != nil {
				resultText = fmt.Sprintf("error: %v", err)
			} else {
				resultText = extractToolText(toolResp)
			}
			history = append(history, goai.ToolMessage(tc.ID, tc.Name, resultText))
		}
	}
}

func runWebUI(modelFlag string, memoryFlag bool, port int) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mcpClient, llmModel, tools, modelName, err := setupApp(ctx, modelFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Setup failed: %v\n", err)
		os.Exit(1)
	}
	defer mcpClient.Close()

	fmt.Printf("Web UI starting at http://localhost:%d\n", port)
	if err := webui.RunServer(ctx, mcpClient, llmModel, tools, modelName, port, memoryFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Web UI error: %v\n", err)
		os.Exit(1)
	}
}

func setupApp(ctx context.Context, modelFlag string) (*goaimcp.Client, provider.LanguageModel, []goai.Tool, string, error) {
	// 1. Logging Setup (keep slog for background details)
	logFile := util.ClientLogFile()

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("failed to open log file %s: %w", logFile, err)
	}

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: util.ClientLogLevel()}))
	slog.SetDefault(logger)

	// Server path
	serverPath := os.Getenv("ELASTIC_MCP_SERVER")
	if serverPath == "" {
		serverPath = "./elastic-mcp-server"
	}

	// LLM Setup
	var modelName string
	var llmModel provider.LanguageModel

	elasticModel := modelFlag
	if elasticModel == "" {
		elasticModel = os.Getenv("ELASTIC_MODEL")
	}

	openaiKey := os.Getenv("OPENAI_API_KEY")
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")

	if elasticModel == "" {
		// Phase 1: Select Provider
		providerItems := []list.Item{}
		if openaiKey != "" {
			providerItems = append(providerItems, item{title: "OpenAI", desc: "Use OpenAI models (GPT-4o, o1, etc.)"})
		}
		if anthropicKey != "" {
			providerItems = append(providerItems, item{title: "Anthropic", desc: "Use Anthropic models (Claude 3.7 Sonnet, etc.)"})
		}
		if geminiKey != "" {
			providerItems = append(providerItems, item{title: "Gemini", desc: "Use Google Gemini models (2.0 Flash, etc.)"})
		}

		if len(providerItems) == 0 {
			return nil, nil, nil, "", errors.New("no LLM API keys found (OPENAI_API_KEY, ANTHROPIC_API_KEY, or GEMINI_API_KEY)")
		}

		// Only ask for provider if more than one is available
		selectedProvider := ""
		if len(providerItems) > 1 {
			l := configureSelectorList(providerItems, "Select Provider", 40, 10)

			m := modelSelector{list: l}
			p := tea.NewProgram(m)
			out, err := p.Run()
			if err != nil {
				return nil, nil, nil, "", fmt.Errorf("error running provider selector: %w", err)
			}
			finalP := out.(modelSelector)
			if finalP.quitting || finalP.choice == "" {
				os.Exit(0)
			}
			selectedProvider = finalP.choice
		} else {
			selectedProvider = providerItems[0].(item).title
		}

		// Phase 2: Select Model ID
		modelItems := []list.Item{}
		switch selectedProvider {
		case "OpenAI":
			modelItems = []list.Item{
				item{title: "gpt-5", desc: "Most advanced OpenAI model"},
				item{title: "gpt-5-mini", desc: "Efficient OpenAI model"},
				item{title: "gpt-5-nano", desc: "Lightweight OpenAI model"},
				item{title: "Custom...", desc: ""},
			}
		case "Anthropic":
			modelItems = []list.Item{
				item{title: "claude-opus-4-6", desc: "Most capable Claude model"},
				item{title: "claude-sonnet-4-6", desc: "Balanced performance and speed"},
				item{title: "claude-haiku-4-5", desc: "Fastest Claude model"},
				item{title: "Custom...", desc: ""},
			}
		case "Gemini":
			modelItems = []list.Item{
				item{title: "gemini-3.1-pro-preview", desc: "Preferred Gemini Pro model"},
				item{title: "gemini-3.5-flash", desc: "Fast Gemini Flash model"},
				item{title: "Custom...", desc: ""},
			}
		}

		l := configureSelectorList(modelItems, fmt.Sprintf("Select %s Model", selectedProvider), 40, 12)

		m := modelSelector{list: l}
		p := tea.NewProgram(m)
		out, err := p.Run()
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("error running model selector: %w", err)
		}

		finalM := out.(modelSelector)
		if finalM.quitting || finalM.choice == "" {
			os.Exit(0)
		}

		if finalM.choice == "Custom..." {
			fmt.Print("Enter custom model ID: ")
			var customID string
			fmt.Scanln(&customID)
			if customID == "" {
				os.Exit(0)
			}
			elasticModel = strings.TrimSpace(customID)
		} else {
			elasticModel = finalM.choice
		}
	}

	modelName = elasticModel
	switch modelProvider(modelName) {
	case "openai":
		if openaiKey == "" {
			return nil, nil, nil, "", fmt.Errorf("OPENAI_API_KEY is required for the selected model %s", modelName)
		}
		llmModel = openai.Chat(modelName)
	case "anthropic":
		if anthropicKey == "" {
			return nil, nil, nil, "", fmt.Errorf("ANTHROPIC_API_KEY is required for the selected model %s", modelName)
		}
		llmModel = anthropic.Chat(modelName)
	case "gemini":
		if geminiKey == "" {
			return nil, nil, nil, "", fmt.Errorf("GEMINI_API_KEY is required for the selected model %s", modelName)
		}
		llmModel = google.Chat(modelName)
	default:
		return nil, nil, nil, "", fmt.Errorf("unsupported model prefix: %s", modelName)
	}

	// MCP Setup — register OnClose before Connect so the client cancels pending
	// requests immediately when the server subprocess exits unexpectedly.
	transport := &goaimcp.StdioTransport{Command: serverPath}
	mcpClient := goaimcp.NewClient("elastic-cli", "1.0.0",
		goaimcp.WithTransport(transport),
	)
	transport.OnClose(func() { mcpClient.Close() })
	if err := mcpClient.Connect(ctx); err != nil {
		return nil, nil, nil, "", fmt.Errorf("failed to connect to MCP server at %s: %w", serverPath, err)
	}

	toolsResult, err := mcpClient.ListTools(ctx, nil)
	if err != nil {
		mcpClient.Close()
		return nil, nil, nil, "", fmt.Errorf("failed to list tools: %w", err)
	}

	tools := make([]goai.Tool, 0, len(toolsResult.Tools))
	toolNames := make([]string, 0, len(toolsResult.Tools))
	for _, t := range toolsResult.Tools {
		toolNames = append(toolNames, t.Name)
		tools = append(tools, goai.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	slog.Info("Discovered tools", "count", len(tools), "names", toolNames)

	return mcpClient, llmModel, tools, modelName, nil
}

func runApp(modelFlag string, memoryFlag bool) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mcpClient, llmModel, tools, modelName, err := setupApp(ctx, modelFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Setup failed: %v\n", err)
		os.Exit(1)
	}
	defer mcpClient.Close()

	// Run Bubble Tea
	p := tea.NewProgram(initialModel(ctx, mcpClient, llmModel, tools, modelName, memoryFlag), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func loadHistory() []string {
	histFile := util.ClientHistoryFile()
	data, err := os.ReadFile(histFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to read history file", "file", histFile, "error", err)
		}
		return nil
	}
	lines := strings.Split(string(data), "\n")
	var history []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			history = append(history, line)
		}
	}
	return history
}

func saveHistory(input string) {
	histFile := util.ClientHistoryFile()
	f, err := os.OpenFile(histFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		slog.Warn("failed to open history file", "file", histFile, "error", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(input + "\n"); err != nil {
		slog.Warn("failed to write to history file", "file", histFile, "error", err)
	}
}

