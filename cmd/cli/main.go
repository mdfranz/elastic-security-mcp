package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/tmc/langchaingo/memory"
	"google.golang.org/api/googleapi"
)

const systemPrompt = `You are a silent Elastic Security analyst tool.
YOUR ONLY JOB IS TO CALL TOOLS.
NEVER explain what you are doing.
NEVER say "I will search" or "Let me check" or "Now I'll".
IF YOU NEED DATA, CALL search_elastic OR list_indices IMMEDIATELY.
DO NOT PROVIDE ANY TEXT UNTIL YOU HAVE THE RESULTS.
ALWAYS use Markdown tables for tabular data.`

const maxLoggedPayloadChars = 4000

func logLevelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCP_LOG_LEVEL"))) {
	case "", "info":
		return slog.LevelInfo
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func payloadLoggingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCP_LOG_PAYLOADS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

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
			Foreground(lipgloss.Color("#878787")).
			Italic(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF0000"))

	systemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A8A8A8"))
)

// Messages
type generateMsg struct{}
type llmResponseMsg struct {
	resp *llms.ContentResponse
}
type executeToolsMsg struct {
	toolCalls []llms.ToolCall
}
type toolsResultMsg struct {
	results []llms.ContentPart
}
type errMsg struct {
	err error
}

type model struct {
	ctx        context.Context
	mcpSession *mcp.ClientSession
	llmClient  llms.Model
	lcTools    []llms.Tool
	history    []llms.MessageContent
	modelName  string
	mem        *memory.ConversationBuffer
	lastInput  string

	viewport  viewport.Model
	textInput textinput.Model
	spinner   spinner.Model
	renderer  *glamour.TermRenderer
	isDark    bool

	messages   []string
	isThinking bool
	err        error
	ready      bool
}

func initialModel(ctx context.Context, session *mcp.ClientSession, client llms.Model, tools []llms.Tool, modelName string) model {
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
		ctx:        ctx,
		mcpSession: session,
		llmClient:  client,
		lcTools:    tools,
		modelName:  modelName,
		mem:        memory.NewConversationBuffer(),
		textInput:  ti,
		spinner:    s,
		renderer:   renderer,
		isDark:     isDark,
		history: []llms.MessageContent{
			{
				Role:  llms.ChatMessageTypeSystem,
				Parts: []llms.ContentPart{llms.TextContent{Text: systemPrompt}},
			},
		},
		messages: []string{
			titleStyle.Render("Elastic Security Assistant"),
			systemStyle.Render(fmt.Sprintf("Model: %s", modelName)),
			"",
		},
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
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
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyEnter:
			input := m.textInput.Value()
			if input == "" {
				return m, nil
			}

			// Handle /memory command
			if input == "/memory" {
				m.textInput.SetValue("")
				vars, err := m.mem.LoadMemoryVariables(m.ctx, nil)
				if err != nil {
					m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Memory error: %v", err)))
				} else {
					hist, _ := vars["history"].(string)
					if hist == "" {
						hist = "(empty)"
					}
					m.messages = append(m.messages, fmt.Sprintf("%s\n%s", systemStyle.Render("Conversation Memory:"), hist))
				}
				m.viewport.SetContent(strings.Join(m.messages, "\n"))
				m.viewport.GotoBottom()
				return m, nil
			}

			// Wrap human input
			wrappedUser := lipgloss.NewStyle().Width(m.viewport.Width - 10).Render(input)
			m.messages = append(m.messages, fmt.Sprintf("%s %s", userStyle.Render("You:"), wrappedUser))

			m.history = append(m.history, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextContent{Text: input}},
			})

			m.lastInput = input
			m.textInput.SetValue("")
			m.isThinking = true
			m.viewport.SetContent(strings.Join(m.messages, "\n"))
			m.viewport.GotoBottom()

			return m, m.generateResponse()
		}

	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-4)
			m.viewport.HighPerformanceRendering = false
			m.viewport.SetContent(strings.Join(m.messages, "\n"))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - 4
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
		if msg.resp == nil || len(msg.resp.Choices) == 0 {
			m.err = errors.New("LLM returned no choices")
			m.isThinking = false
			m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
			m.viewport.SetContent(strings.Join(m.messages, "\n"))
			m.viewport.GotoBottom()
			return m, nil
		}

		choice := msg.resp.Choices[0]

		// Add assistant turn to history
		assistantParts := []llms.ContentPart{}
		if choice.Content != "" {
			assistantParts = append(assistantParts, llms.TextContent{Text: choice.Content})
		}
		for i := range choice.ToolCalls {
			// Normalize tool calls for Gemini/etc. if IDs are missing
			// Gemini often requires hexadecimal IDs
			if choice.ToolCalls[i].ID == "" {
				b := make([]byte, 8)
				if _, err := rand.Read(b); err != nil {
					m.err = fmt.Errorf("failed to create tool call ID: %w", err)
					m.isThinking = false
					m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
					m.viewport.SetContent(strings.Join(m.messages, "\n"))
					m.viewport.GotoBottom()
					return m, nil
				}
				choice.ToolCalls[i].ID = hex.EncodeToString(b)
			}
			if choice.ToolCalls[i].Type == "" {
				choice.ToolCalls[i].Type = "tool_call"
			}
			assistantParts = append(assistantParts, choice.ToolCalls[i])
		}
		m.history = append(m.history, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: assistantParts,
		})

		// Detect stalling
		content := strings.ToLower(choice.Content)
		if len(choice.ToolCalls) == 0 && (strings.Contains(content, "i will") ||
			strings.Contains(content, "let me") ||
			strings.Contains(content, "now i'll") ||
			strings.Contains(content, "searching")) {

			m.history = append(m.history, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextContent{Text: "Please proceed with the tool call immediately. Do not narrate your intent."}},
			})
			return m, m.generateResponse()
		}

		// Display content if any
		if choice.Content != "" {
			rendered, err := m.renderer.Render(choice.Content)
			if err != nil {
				rendered = choice.Content
			}
			m.messages = append(m.messages, fmt.Sprintf("%s\n%s", assistantStyle.Render("Assistant:"), rendered))
		}

		// Handle tool calls
		if len(choice.ToolCalls) > 0 {
			for _, tc := range choice.ToolCalls {
				m.messages = append(m.messages, toolStyle.Copy().Width(m.viewport.Width-4).Render(fmt.Sprintf("  [%s] args: %s", toolCallName(tc), toolCallArguments(tc))))
			}
			m.viewport.SetContent(strings.Join(m.messages, "\n"))
			m.viewport.GotoBottom()
			return m, m.executeTools(choice.ToolCalls)
		}

		// Done — save completed turn to LangChain memory
		if m.lastInput != "" {
			_ = m.mem.SaveContext(m.ctx,
				map[string]any{"input": m.lastInput},
				map[string]any{"output": choice.Content},
			)
			m.lastInput = ""
		}
		m.isThinking = false
		m.viewport.SetContent(strings.Join(m.messages, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case toolsResultMsg:
		for _, res := range msg.results {
			m.history = append(m.history, llms.MessageContent{
				Role:  llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{res},
			})
		}
		return m, m.generateResponse()

	case errMsg:
		m.err = msg.err
		m.isThinking = false
		m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
		m.viewport.SetContent(strings.Join(m.messages, "\n"))
		m.viewport.GotoBottom()
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
	s += m.viewport.View() + "\n"
	if m.isThinking {
		s += m.spinner.View() + " Thinking...\n"
	} else {
		s += "\n"
	}
	s += m.textInput.View()

	return s
}

func truncateForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

func toolCallName(tc llms.ToolCall) string {
	if tc.FunctionCall == nil || tc.FunctionCall.Name == "" {
		return "(missing)"
	}
	return tc.FunctionCall.Name
}

func toolCallArguments(tc llms.ToolCall) string {
	if tc.FunctionCall == nil {
		return "{}"
	}
	if strings.TrimSpace(tc.FunctionCall.Arguments) == "" {
		return "{}"
	}
	return tc.FunctionCall.Arguments
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

func summarizeHistoryForLog(history []llms.MessageContent) string {
	type partSummary map[string]any
	type messageSummary map[string]any

	summary := make([]messageSummary, 0, len(history))
	for i, msg := range history {
		parts := make([]partSummary, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.TextContent:
				parts = append(parts, partSummary{
					"type":    "text",
					"chars":   len(p.Text),
					"preview": truncateForLog(p.Text, 160),
				})
			case llms.ToolCall:
				parts = append(parts, partSummary{
					"type":      "tool_call",
					"name":      toolCallName(p),
					"id":        p.ID,
					"arg_chars": len(toolCallArguments(p)),
					"args":      truncateForLog(toolCallArguments(p), 240),
				})
			case llms.ToolCallResponse:
				parts = append(parts, partSummary{
					"type":         "tool_response",
					"name":         p.Name,
					"tool_call_id": p.ToolCallID,
					"chars":        len(p.Content),
					"preview":      truncateForLog(p.Content, 240),
				})
			default:
				parts = append(parts, partSummary{
					"type": fmt.Sprintf("%T", part),
				})
			}
		}
		summary = append(summary, messageSummary{
			"index": i,
			"role":  msg.Role,
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
			"tool_count", len(m.lcTools),
			"message_count", len(m.history),
			"summary", summarizeHistoryForLog(m.history),
		)
		if payloadLoggingEnabled() {
			histJSON, err := json.Marshal(m.history)
			if err == nil {
				slog.Debug("Sending history to LLM", "history", truncateForLog(string(histJSON), maxLoggedPayloadChars))
			}
		}

		resp, err := m.llmClient.GenerateContent(m.ctx, m.history,
			llms.WithTools(m.lcTools),
			llms.WithMaxTokens(4096),
		)
		if err != nil {
			var gerr *googleapi.Error
			if errors.As(err, &gerr) {
				logAttrs := []any{
					"model", m.modelName,
					"status_code", gerr.Code,
					"message", gerr.Message,
				}
				if payloadLoggingEnabled() {
					logAttrs = append(logAttrs,
						"body", truncateForLog(gerr.Body, maxLoggedPayloadChars),
						"details", fmt.Sprintf("%v", gerr.Details),
						"headers", fmt.Sprintf("%v", gerr.Header),
					)
				}
				slog.Error("Google API generation error details",
					logAttrs...,
				)
			}
			slog.Error("LLM generation error", "error", err)
			return errMsg{err}
		}

		if payloadLoggingEnabled() {
			respJSON, err := json.Marshal(resp)
			if err == nil {
				slog.Debug("Received LLM response", "response", truncateForLog(string(respJSON), maxLoggedPayloadChars))
			}
		}

		return llmResponseMsg{resp}
	}
}

func (m model) executeTools(toolCalls []llms.ToolCall) tea.Cmd {
	return func() tea.Msg {
		toolResultParts := []llms.ContentPart{}
		for _, tc := range toolCalls {
			name := toolCallName(tc)
			argsJSON := toolCallArguments(tc)
			slog.Info("Executing tool", "name", name, "arg_chars", len(argsJSON), "id", tc.ID)
			if payloadLoggingEnabled() {
				slog.Debug("Tool arguments", "name", name, "args", truncateForLog(argsJSON, maxLoggedPayloadChars))
			}

			if tc.FunctionCall == nil || tc.FunctionCall.Name == "" {
				toolResultParts = append(toolResultParts, llms.ToolCallResponse{
					ToolCallID: tc.ID,
					Name:       name,
					Content:    "invalid tool call: missing function name",
				})
				continue
			}

			var args map[string]any
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				toolResultParts = append(toolResultParts, llms.ToolCallResponse{
					ToolCallID: tc.ID,
					Name:       tc.FunctionCall.Name,
					Content:    fmt.Sprintf("invalid tool arguments: %v", err),
				})
				continue
			}

			toolResp, err := m.mcpSession.CallTool(m.ctx, &mcp.CallToolParams{
				Name:      tc.FunctionCall.Name,
				Arguments: args,
			})

			var resultText string
			switch {
			case err != nil:
				slog.Error("Tool call error", "name", tc.FunctionCall.Name, "error", err)
				resultText = fmt.Sprintf("error calling tool: %v", err)
			case toolResp.IsError:
				resultText = extractToolContent(toolResp)
				if strings.TrimSpace(resultText) == "" {
					resultText = "tool returned an error"
				}
				slog.Warn("Tool returned error status",
					"name", tc.FunctionCall.Name,
					"error_preview", truncateForLog(resultText, 500),
				)
			default:
				resultText = extractToolContent(toolResp)
				slog.Debug("Tool execution successful",
					"name", tc.FunctionCall.Name,
					"result_len", len(resultText),
					"result_preview", truncateForLog(resultText, 500),
				)
			}

			toolResultParts = append(toolResultParts, llms.ToolCallResponse{
				ToolCallID: tc.ID,
				Name:       tc.FunctionCall.Name,
				Content:    resultText,
			})
		}
		return toolsResultMsg{toolResultParts}
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
		case "ctrl+c", "q":
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

	rootCmd := &cobra.Command{
		Use:   "elastic-cli",
		Short: "Elastic Security Assistant CLI",
		Run: func(cmd *cobra.Command, args []string) {
			runApp(modelFlag)
		},
	}

	rootCmd.Flags().StringVarP(&modelFlag, "model", "m", "", "Model ID to use (e.g. gpt-5, claude-3-7-sonnet-latest)")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runApp(modelFlag string) {
	// 1. Logging Setup (keep slog for background details)
	logFile := os.Getenv("MCP_LOG_FILE")
	if logFile == "" {
		logFile = "elastic-cli.log"
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", logFile, err)
		os.Exit(1)
	}
	defer f.Close()

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: logLevelFromEnv()}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Server path
	serverPath := os.Getenv("ELASTIC_MCP_SERVER")
	if serverPath == "" {
		serverPath = "./elastic-mcp-server"
	}

	// LLM Setup
	var modelName string
	var llmClient llms.Model

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
			fmt.Fprintln(os.Stderr, "Error: No API keys found (OPENAI_API_KEY, ANTHROPIC_API_KEY, or GEMINI_API_KEY).")
			os.Exit(1)
		}

		// Only ask for provider if more than one is available
		selectedProvider := ""
		if len(providerItems) > 1 {
			l := list.New(providerItems, list.NewDefaultDelegate(), 40, 10)
			l.Title = "Select Provider"
			l.SetShowStatusBar(false)
			l.SetFilteringEnabled(false)
			l.Styles.Title = titleStyle

			m := modelSelector{list: l}
			p := tea.NewProgram(m)
			out, err := p.Run()
			if err != nil {
				fmt.Printf("Error running provider selector: %v", err)
				os.Exit(1)
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
				item{title: "gpt-5", desc: ""},
				item{title: "gpt-5-mini", desc: ""},
				item{title: "gpt-5-nano", desc: ""},
				item{title: "Custom...", desc: ""},
			}
		case "Anthropic":
			modelItems = []list.Item{
				item{title: "claude-sonnet-4-6", desc: ""},
				item{title: "claude-haiku-4-5", desc: ""},
				item{title: "claude-opus-4-6", desc: ""},
				item{title: "Custom...", desc: ""},
			}
		case "Gemini":
			modelItems = []list.Item{
				item{title: "gemini-3-flash-preview", desc: ""},
				item{title: "gemini-3.1-pro-preview", desc: ""},
				item{title: "Custom...", desc: ""},
			}
		}

		l := list.New(modelItems, list.NewDefaultDelegate(), 40, 12)
		l.Title = fmt.Sprintf("Select %s Model", selectedProvider)
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(false)
		l.Styles.Title = titleStyle

		m := modelSelector{list: l}
		p := tea.NewProgram(m)
		out, err := p.Run()
		if err != nil {
			fmt.Printf("Error running model selector: %v", err)
			os.Exit(1)
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
			slog.Error("OPENAI_API_KEY is required for the selected model", "model", modelName)
			os.Exit(1)
		}
		llmClient, err = openai.New(openai.WithModel(modelName))
	case "anthropic":
		if anthropicKey == "" {
			slog.Error("ANTHROPIC_API_KEY is required for the selected model", "model", modelName)
			os.Exit(1)
		}
		llmClient, err = anthropic.New(anthropic.WithModel(modelName))
	case "gemini":
		if geminiKey == "" {
			slog.Error("GEMINI_API_KEY is required for the selected model", "model", modelName)
			os.Exit(1)
		}
		llmClient = newGeminiModel(geminiKey, modelName, nil)
	default:
		slog.Error("Unsupported model prefix", "model", modelName)
		os.Exit(1)
	}

	if err != nil {
		slog.Error("Failed to create LLM client", "error", err)
		os.Exit(1)
	}

	// MCP Setup
	cmd := exec.Command(serverPath)
	transport := &mcp.CommandTransport{Command: cmd}
	client := mcp.NewClient(&mcp.Implementation{Name: "elastic-cli", Version: "1.0.0"}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		slog.Error("Failed to connect to MCP server", "path", serverPath, "error", err)
		os.Exit(1)
	}
	defer session.Close()

	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		slog.Error("Failed to list tools", "error", err)
		os.Exit(1)
	}

	lcTools := make([]llms.Tool, 0, len(toolsResult.Tools))
	for _, t := range toolsResult.Tools {
		lcTools = append(lcTools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	// Run Bubble Tea
	p := tea.NewProgram(initialModel(ctx, session, llmClient, lcTools, modelName), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}
