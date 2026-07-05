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
	"strconv"
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
	"github.com/mfranz/elastic-security-mcp/internal/agent"
	"github.com/mfranz/elastic-security-mcp/internal/util"
	"github.com/mfranz/elastic-security-mcp/internal/webui"
	"github.com/spf13/cobra"
)

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

// agentEvent wraps an agent.Event for the Bubble Tea message chain. A
// background goroutine (started by startTurn) pushes these onto a channel as
// the shared agent.Engine progresses through a turn; Update drains one at a
// time via waitForAgentEvent so the TUI can render incremental tool-call
// progress instead of waiting for the whole turn to finish. done/result are
// set on the final value sent before the channel is closed.
type agentEvent struct {
	ev     agent.Event
	done   bool
	result agent.TurnResult
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
	engine    *agent.Engine
	events    chan agentEvent
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
		engine:    agent.New(mcpClient, llmModel, tools, modelName),
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
			// A turn is already in flight; ignore input until it settles so
			// we don't start overlapping turns against the same history.
			if m.isThinking {
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
					hist := agent.RenderHistoryText(m.history)
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

			return m, m.startTurn()
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

	case agentEvent:
		if msg.done {
			m.history = msg.result.History
			m.lastInput = ""
			m.isThinking = false
			m.statusText = ""
			if msg.result.Err != nil {
				m.err = msg.result.Err
				m.messages = append(m.messages, errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
			}
			m.refreshViewport(false)
			return m, nil
		}

		m.handleAgentEvent(msg.ev)
		return m, waitForAgentEvent(m.events)
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

// stopServer asks the spawned elastic-mcp-server subprocess to shut down
// gracefully (SIGTERM) before closing the MCP client. The goai StdioTransport's
// Close() sends SIGKILL immediately, which never gives the server a chance to
// run its deferred cleanup (removing its lock file), so we terminate it
// ourselves first and only fall back to the transport's hard kill if it
// doesn't exit in time.
func stopServer(mcpClient *goaimcp.Client) {
	if data, err := os.ReadFile(util.ServerLockFile()); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				proc.Signal(syscall.SIGTERM)
				for i := 0; i < 20; i++ {
					if proc.Signal(syscall.Signal(0)) != nil {
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}
	}
	mcpClient.Close()
}

// startTurn kicks off the shared agent.Engine in a background goroutine and
// returns a Cmd that waits for the first Event it produces. The goroutine
// pushes each Event onto m.events as it happens (tool call started, tool
// call finished, status update, final assistant text) so the TUI can render
// progress incrementally instead of waiting for the whole turn — matching
// what internal/webui/server.go already does over the WebSocket, just
// bridged through a channel since Bubble Tea's Update loop must stay
// single-threaded and a Cmd can only return one message at a time.
func (m *model) startTurn() tea.Cmd {
	ch := make(chan agentEvent, 16)
	m.events = ch

	ctx := m.ctx
	eng := m.engine
	history := m.history

	go func() {
		result := eng.Turn(ctx, history, func(ev agent.Event) {
			ch <- agentEvent{ev: ev}
		})
		ch <- agentEvent{done: true, result: result}
		close(ch)
	}()

	return waitForAgentEvent(ch)
}

func waitForAgentEvent(ch chan agentEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return ev
	}
}

// handleAgentEvent renders one incremental agent.Event. It does not touch
// m.isThinking/m.statusText's terminal state — that's handled by the
// done-branch in Update once the whole turn finishes.
func (m *model) handleAgentEvent(ev agent.Event) {
	switch ev.Kind {
	case agent.EventStatus:
		m.statusText = ev.Status
		m.refreshViewport(false)

	case agent.EventToolStart:
		header := toolStyle.Render(fmt.Sprintf("[%s] args:", ev.Tool.Call.Name))
		body := toolJSONStyle.Copy().Width(m.viewport.Width).Render(formatToolCallArguments(ev.Tool.Call))
		m.messages = append(m.messages, header+"\n"+body+"\n")
		m.refreshViewport(false)

	case agent.EventToolEnd:
		m.toolCalls++
		if ev.Tool.IsCached {
			m.cacheHits++
		} else {
			m.cacheMisses++
		}
		if ev.Tool.IsStored {
			m.cacheStores++
		}
		if ev.Tool.IsError {
			m.toolErrors++
		}
		m.refreshViewport(false)

	case agent.EventAssistant:
		rendered, err := m.renderer.Render(normalizeMarkdownForTerminal(ev.Text))
		if err != nil {
			rendered = ev.Text
		}
		m.messages = append(m.messages, fmt.Sprintf("%s\n%s", assistantStyle.Render("Assistant:"), rendered))
		m.appendConversation("assistant", ev.Text)
		m.refreshViewport(false)
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
	defer stopServer(mcpClient)

	toolsResult, err := mcpClient.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list tools: %v\n", err)
		stopServer(mcpClient)
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

	eng := agent.New(mcpClient, llmModel, tools, modelName)
	result := eng.Turn(ctx, history, func(ev agent.Event) {
		switch ev.Kind {
		case agent.EventToolStart:
			fmt.Printf("Calling tool: %s\n", ev.Tool.Call.Name)
		case agent.EventAssistant:
			fmt.Println(ev.Text)
		}
	})
	if result.Err != nil {
		fmt.Fprintf(os.Stderr, "Generation error: %v\n", result.Err)
		stopServer(mcpClient)
		os.Exit(1)
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
	defer stopServer(mcpClient)

	fmt.Printf("Web UI starting at http://localhost:%d\n", port)
	if err := webui.RunServer(ctx, mcpClient, llmModel, tools, modelName, port, memoryFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Web UI error: %v\n", err)
		stopServer(mcpClient)
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
	defer stopServer(mcpClient)

	// Run Bubble Tea
	p := tea.NewProgram(initialModel(ctx, mcpClient, llmModel, tools, modelName, memoryFlag), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		stopServer(mcpClient)
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

