# cmd/ Implementation Guide

Implementation-level reference for the binaries under `cmd/`. Concrete details for a developer modifying this code — types, functions, flags/env vars, gotchas — not architecture/data-flow.

## Cross-cutting: one shared agentic loop, two adapters

`cmd/cli/main.go` and `internal/webui/server.go` used to each implement their own copy of the tool-calling loop; both now drive the single implementation in `internal/agent` (`Engine.Turn` — see `internal/IMPL.md`'s `internal/agent` section for the full breakdown of the loop, stall detection, and cache-prefix handling). Each front end supplies only an `emit func(agent.Event)` callback that translates engine events into its own UI:

- **webui** (`internal/webui/server.go`): `emitEvent` calls `s.sendMessage` directly — safe to block, since `processConversation` already runs inside the per-connection goroutine and nothing else needs it meanwhile.
- **CLI** (`cmd/cli/main.go`): `startTurn` runs `Engine.Turn` in a background goroutine (Bubble Tea's `Update` must stay single-threaded and non-blocking) whose `emit` pushes each `agent.Event` onto a buffered `chan agentEvent`; a repeating `tea.Cmd` (`waitForAgentEvent`) drains one event at a time and feeds it back into `Update`. This is what gives the TUI the same per-tool-call streaming the Web UI already had — previously the CLI batched all tool calls into one `tea.Cmd` and only re-rendered once the whole round finished.

Since the loop, `SystemPrompt`, and stall-detection/cache-prefix logic now live once in `internal/agent`, a fix or prompt tweak there applies to both front ends automatically — no more manual mirroring.

## cmd/cli/main.go — Bubble Tea TUI chat client

Package `main`. Interactive terminal chat client: spawns the `elastic-mcp-server` subprocess over stdio, wires an LLM (OpenAI/Anthropic/Gemini via `goai`) into an agentic tool-calling loop against the MCP tools, and renders through a Bubble Tea/Lipgloss/Glamour TUI. Also doubles as a one-shot CLI runner and a launcher for `internal/webui`.

### Constants
- `maxHistoryMessages = 15` — cap applied by `pruneHistory` when memory is off.
- `footerReserveLines = 9` — viewport height reserved for footer/status/help/input rows.

(`SystemPrompt` and the tool-call debug-log truncation length now live in `internal/agent`, shared with webui.)

### Key types
- `model`: the Bubble Tea model. Fields include `ctx`, `engine *agent.Engine`, `events chan agentEvent`, `history []provider.Message`, `modelName`, `useMemory bool`, `lastInput`, `inputHist []string` + `histIndex`/`histDraft` (readline-style history browsing), `viewport`, `textInput`, `spinner`, `renderer *glamour.TermRenderer`, `isDark bool`, `focus focusArea`, `messages []string` (rendered viewport lines), `conversation []exportMessage` (backs `/export`), `isThinking bool`, `statusText string`, counters `toolCalls/cacheHits/cacheMisses/cacheStores/toolErrors int`, `err error`, `ready bool`.
- `agentEvent{ev agent.Event, done bool, result agent.TurnResult}` — wraps an `agent.Event` for the Bubble Tea message chain; `done`/`result` are set on the final value sent before the channel closes (see `startTurn` below).
- `exportMessage{role, content}` — backs the `/export` markdown transcript.
- `focusArea` enum: `focusInput`/`focusOutput` — Tab toggles which pane receives Up/Down.
- `item{title, desc}` implements `list.Item` (provider/model picker lists); `modelSelector` wraps `list.Model` as its own mini Bubble Tea program for interactive provider/model selection.

### REPL / TUI flow (`Update`)
- `Ctrl+C`/`Ctrl+D`/`Esc` → `tea.Quit` immediately, no confirmation.
- `Tab` toggles `focus` between input and output viewport.
- `Up`/`Down`: if input-focused, `browseHistory(-1/+1)` (readline-like recall via `inputHist`/`histIndex`/`histDraft`); if output-focused, scrolls the viewport.
- `PgUp`/`PgDown`: always scroll viewport half a page.
- `Enter` (only when `focus == focusInput`):
  - Ignored entirely while `m.isThinking` is true — a turn is already in flight against the same history, so new input (including slash commands) is dropped rather than risking overlapping turns.
  - Slash commands intercepted **before** the LLM: `/memory` (dumps `agent.RenderHistoryText(m.history)`, or "Conversation memory is disabled." if `useMemory` is false) and `/export` (`m.exportConversation()`).
  - Otherwise: pushes the line into the transcript + `m.conversation`, appends `goai.UserMessage(input)` to `m.history`, calls `m.pruneHistory()` **only if `!m.useMemory`** — counter-intuitively, "memory off" doesn't mean zero history, it means a rolling window capped at `maxHistoryMessages`. Records to `inputHist`, persists via `saveHistory`, clears input, kicks off `m.startTurn()`.
- `tea.WindowSizeMsg`: first resize sets `m.ready = true` and builds the viewport (`height - footerReserveLines`); every resize rebuilds the Glamour renderer from scratch with `WithWordWrap(msg.Width-4)` (discarded/recreated, not resized in place).
- `agentEvent`: the single case handling both incremental progress and turn completion.
  - If `msg.done`: sets `m.history = msg.result.History`, clears `isThinking`/`statusText`/`lastInput`, and if `msg.result.Err != nil` renders the error line. This is the only place `m.history` is written from an engine-driven message, and it only happens once per turn (`Engine.Turn` itself never mutates the caller's slice — see `internal/agent`).
  - Otherwise: `m.handleAgentEvent(msg.ev)` renders the event, then re-arms `waitForAgentEvent(m.events)` to keep draining the channel.

### Tool-calling / LLM plumbing
- `startTurn() tea.Cmd`: creates a buffered `chan agentEvent`, stores it on `m.events`, and launches a goroutine that calls `m.engine.Turn(ctx, m.history, emit)` where `emit` pushes each `agent.Event` onto the channel; once `Turn` returns, pushes a final `agentEvent{done: true, result: ...}` and closes the channel. Returns `waitForAgentEvent(ch)` to kick off draining. This is the bridge between Bubble Tea's single-threaded, non-blocking `Update` and the engine's blocking, multi-step `Turn` call.
- `waitForAgentEvent(ch chan agentEvent) tea.Cmd`: reads exactly one value off the channel and returns it as a `tea.Msg` (or `nil` once the channel is closed and drained).
- `handleAgentEvent(ev agent.Event)`: renders one incremental event — `EventStatus` sets the footer status text; `EventToolStart` prints `[name] args:` + pretty-printed JSON via `formatToolCallArguments`; `EventToolEnd` updates the running `toolCalls`/`cacheHits`/`cacheMisses`/`cacheStores`/`toolErrors` counters; `EventAssistant` renders the final answer via Glamour (`normalizeMarkdownForTerminal` first demotes `#` headers) and appends it to `m.conversation`. Because each event is handled and rendered as it arrives (rather than after the whole turn finishes), tool-call args and counters now update live, one tool at a time — matching what the Web UI already did.
- `formatToolCallArguments(tc provider.ToolCall) string`: pretty-prints tool call JSON args, and additionally recursively parses any string field that itself looks like embedded JSON (`{...}`/`[...]`) — e.g. `search_elastic`'s raw DSL `query` field — so nested JSON strings render structured instead of an escaped blob. Then does manual line-collapsing (merging lone closing-bracket lines back onto the previous line; collapsing single-key opens with their next closing line) to keep short nested objects compact. **Gotcha**: this is fragile string-based reformatting, not a real pretty-printer.

### Process lifecycle (spawning/killing the MCP server)
- `stopServer(mcpClient *goaimcp.Client)`: reads the PID from `util.ServerLockFile()`, sends `SIGTERM`, polls up to 20×50ms (1s total) via `proc.Signal(syscall.Signal(0))` for the process to disappear, then calls `mcpClient.Close()` regardless. **Why**: `goaimcp.StdioTransport.Close()` would otherwise `SIGKILL` the child immediately, never letting the server's own deferred cleanup (removing its lock file — see `cmd/server/main.go`) run. Called via `defer stopServer(mcpClient)` in `runApp`, `runWebUI`, and around error paths in `runSinglePrompt`.
- Both `runSinglePrompt` and `setupApp` register `transport.OnClose(func() { mcpClient.Close() })` **before** calling `mcpClient.Connect(ctx)` — deliberate ordering so that if the server subprocess dies unexpectedly, pending MCP requests are cancelled immediately rather than hanging.
- `runApp`/`runWebUI` build `ctx` via `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` for graceful shutdown propagation.

### CLI flags / env vars / entrypoint
`main()` builds a single `cobra.Command` "elastic-cli":
- `--model`/`-m` (string) — model ID (e.g. `gpt-5`, `claude-3-7-sonnet-latest`).
- `--memory` (bool, default `true`) — enable conversation memory.
- `--prompt`/`-p` (string) — run one prompt non-interactively and exit (`runSinglePrompt`).
- `--webui` (bool, default `false`) — start the web UI instead of the TUI (`runWebUI`).
- `--port` (int, default `8080`) — web UI port.
- Dispatch precedence: `--prompt` wins over `--webui`, which wins over the default TUI (`runApp`).

Env vars:
- `ELASTIC_MCP_SERVER` — path to the server binary to spawn; defaults to `./elastic-mcp-server` (relative to CWD — a gotcha if run from a different directory).
- `ELASTIC_MODEL` — fallback model if `--model` not given; further fallback is hardcoded `"gemini-2.0-flash"` **only in `runSinglePrompt`** (the interactive path has no such fallback — it opens the TUI provider/model picker instead).
- `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY` — provider credentials; which are present determines which provider(s) are offered.
- Indirectly via `internal/util`: `CLIENT_LOG_FILE` (default `elastic-cli.log`), `CLIENT_LOG_LEVEL` (default info), `CLIENT_LOG_PAYLOADS` (verbose arg/result debug logs), `CLIENT_HISTORY_FILE` (default `~/.elastic-cli-history`), `SERVER_LOCK_FILE` (default `elastic-mcp-server.lock`, read by `stopServer`).

`modelProvider(modelName string) string`: naive prefix sniffing — `gpt-`/`o1-`/`o3-` → `openai`, `claude-` → `anthropic`, `gemini-` → `gemini`, else `""`. **Gotcha**: a future model family with a different prefix silently produces an "unsupported model prefix" error.

### Three run modes
- `runSinglePrompt(modelFlag, prompt string)`: non-interactive one-shot. Opens its own log file/logger, resolves model/provider/keys, spins up its own `StdioTransport`/`goaimcp.Client` (`"elastic-cli-oneshot"`), lists tools, then builds an `agent.New(mcpClient, llmModel, tools, modelName)` and calls `eng.Turn(ctx, history, emit)` once — `emit` just prints `Calling tool: <name>` for `EventToolStart` and the final text for `EventAssistant`. Since this now goes through the same `Engine.Turn` as the interactive paths, one-shot mode gets the same retry/backoff, temperature/max-token settings, and stall-detection guard the TUI and Web UI have — previously it had none of these (a duplicated, simpler loop with its own gaps).
- `runWebUI(modelFlag string, memoryFlag bool, port int)`: builds a cancelable ctx from OS signals, calls shared `setupApp`, then `webui.RunServer(ctx, mcpClient, llmModel, tools, modelName, port, memoryFlag)`.
- `runApp(modelFlag string, memoryFlag bool)`: default TUI path — `setupApp` then `tea.NewProgram(initialModel(...), tea.WithAltScreen())`.
- `setupApp(ctx, modelFlag string) (*goaimcp.Client, provider.LanguageModel, []goai.Tool, string, error)`: shared by `runApp`/`runWebUI`. Opens the client log file/logger; if no model given via flag/env, launches an **interactive Bubble Tea picker** (`modelSelector`) — a provider list (shown only if more than one API key is present; otherwise auto-selects the sole provider), then a hardcoded model list per provider (OpenAI: gpt-5/gpt-5-mini/gpt-5-nano/Custom; Anthropic: claude-opus-4-6/claude-sonnet-4-6/claude-haiku-4-5/Custom; Gemini: gemini-3.1-pro-preview/gemini-3.5-flash/Custom). The "Custom..." option does a **blocking `fmt.Scanln(&customID)` on stdin** directly (not through the Bubble Tea program) — only works in an actual TTY, and can be surprising mid-picker-flow. Quitting any picker (`ctrl+c`/`ctrl+d`/`q`) calls `os.Exit(0)`. After model resolution, builds the `provider.LanguageModel` (openai/anthropic/google `.Chat(modelName)`), constructs `&goaimcp.StdioTransport{Command: serverPath}` and `goaimcp.NewClient("elastic-cli", "1.0.0", goaimcp.WithTransport(transport))`, registers `OnClose`, `Connect`s, then `ListTools` and converts each `mcp.Tool` into `goai.Tool{Name, Description, InputSchema}`.

### History file persistence
- `loadHistory() []string`: reads `util.ClientHistoryFile()`, splits on `\n`, trims blanks; missing file tolerated silently (only warns on non-`os.IsNotExist` errors).
- `saveHistory(input string)`: appends one line (mode `0600`) to the same file, called once per submitted prompt from `pushInputHistory`.
- `pushInputHistory`: also dedupes consecutive-identical entries and resets `histIndex`/`histDraft`.

### Export feature
- `/export` → `exportConversation()`: errors if `m.conversation` is empty; else builds a filename via `exportFilename(time.Now())` = `investigation-export-<2006-01-02T15-04-05>.md`, resolves an absolute path via `filepath.Abs` — **writes into the current working directory, not a configurable location** — writes via `buildMarkdownExport` with `os.WriteFile(path, md, 0644)`, appends a system message with the resulting path into the transcript.

### cli/main_test.go
Pure-function unit tests, no mocks needed (cache-prefix parsing now lives in `internal/agent`'s own test file):
- `TestBuildMarkdownExport` — checks title, `Exported on: <RFC1123>` line, and role-labeled sections ("You:"/"Assistant:"/"System:").
- `TestExportFilename` — checks `exportFilename` produces `investigation-export-2026-05-05T09-30-45.md` for a fixed time.
- `TestNormalizeMarkdownForTerminal` — checks `###`/`##` headers get hashes stripped (indentation preserved) while non-header lines (including bullets) pass through unchanged.

## cmd/server/main.go — MCP server entrypoint

Package `main`. Process entrypoint for `elastic-mcp-server`: acquires a singleton lock, sets up JSON file logging, builds the Elasticsearch/Kibana clients from env vars, registers all MCP tools, serves over stdio until an OS signal.

- `main()`: calls `run()`, prints `Error: %v` to stderr and `os.Exit(1)` on failure.
- `run() error` — does everything:
  1. `setParentDeathSignal()` (platform-specific — see below) so the server dies if its parent (the CLI) is killed hard rather than being orphaned.
  2. **Single-instance locking**: opens `util.ServerLockFile()` (default `elastic-mcp-server.lock`), takes an exclusive non-blocking `flock` (`syscall.Flock(fd, LOCK_EX|LOCK_NB)`). If locking fails, reads the PID from the lock file and — if that process is still alive (`proc.Signal(syscall.Signal(0))` succeeds) — returns `"elastic-mcp-server (PID %d) is already running"`; otherwise a generic "already running (lock on file)" fallback message (reached only in the unusual case of a stale lock where flock still fails). On success, `defer`s closing the fd (releases the flock) and `os.Remove`ing the lock file — this is the cleanup `cli/main.go`'s `stopServer` SIGTERM dance exists specifically to allow to run.
  3. Writes its own PID into the lock file (`Truncate(0)`, `Seek(0,0)`, `Fprintf("%d\n", pid)`, `Sync()`).
  4. **Logging setup**: opens `util.ServerLogFile()` (default `elastic-mcp-server.log`), sets `slog.SetDefault` to a `slog.NewJSONHandler` writing to that file — deliberately never stdout/stderr, since those are reserved for the MCP stdio transport.
  5. **Env vars**: `ELASTIC_URL`, `ELASTIC_KEY` required (fails with `"ELASTIC_URL and ELASTIC_KEY environment variables must be set"` if either empty); `KIBANA_URL`, `KIBANA_USER`, `KIBANA_PASS`, `KIBANA_KEY` all optional — Kibana tools are skipped entirely if `KIBANA_URL == ""`. All values pass through `os.ExpandEnv`, so `$VAR`-style interpolation inside the values themselves is supported.
  6. Builds `elasticsearch.NewClient(elasticURL, elasticKey)`; does a best-effort `es.Raw.Info()` connectivity check that only **logs a warning** on failure — doesn't abort startup, so the server can start even if ES is briefly unreachable.
  7. Creates `mcp.NewServer(&mcp.Implementation{Name: "elastic-mcp-server", Version: "1.0.0"}, nil)`.
  8. `elasticsearch.RegisterTools(server, es)` always runs; `kibana.NewClient(...)` + `kibana.RegisterTools(server, kb)` run only if `KIBANA_URL` is set.
  9. Builds a cancelable ctx via `signal.NotifyContext(..., os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)` — a superset of the CLI's signal set (adds SIGQUIT/SIGHUP).
  10. `server.Run(ctx, &mcp.StdioTransport{})` — blocks serving stdio; if it errors *and* `ctx.Err() == nil` (the error wasn't from our own cancellation) returns the error, otherwise a clean shutdown returns `nil`.

Gotchas:
- Lock-file stale-PID detection mainly works via the SIGTERM path from `stopServer` (since `flock` is auto-released by the OS even on a hard kill); the two near-identical error strings for "already running" are somewhat redundant/misleading if that assumption breaks.
- All server logs go to a JSON file, never stdout/stderr — debugging by watching the terminal shows nothing; check `elastic-mcp-server.log` (or `SERVER_LOG_FILE`).
- No graceful in-flight request draining beyond whatever `mcp.Server.Run` does internally on ctx cancellation.

## cmd/server/pdeath_linux.go (build tag `linux`)
`setParentDeathSignal()` calls `syscall.Syscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0)` directly (raw `prctl`), so the kernel sends this process `SIGTERM` when its parent thread dies. Syscall errors are discarded. **Gotcha**: `PR_SET_PDEATHSIG` fires when the calling *thread* exits (not necessarily on graceful parent exit) and doesn't fire if the process is re-parented before death in some scenarios — acceptable since it's a hard-crash safety net, not the primary shutdown path (SIGTERM from `stopServer` is primary).

## cmd/server/pdeath_other.go (build tag `!linux`)
No-op stub of `setParentDeathSignal()` for macOS/Windows/etc. — on those platforms an orphaned server relies solely on the CLI's `stopServer` SIGTERM or manual cleanup; no OS-level parent-death guarantee exists.

## cmd/test-mcp/main.go — minimal debug MCP client

Package `main`. Throwaway diagnostic tool: spawns the server, connects, lists tools, dumps the tool list as pretty JSON to stdout — a quick way to check registered tools/schemas without going through the LLM.

- Uses the *raw* `github.com/modelcontextprotocol/go-sdk/mcp` package directly (not the `goaimcp`/`goai` wrappers used elsewhere): `mcp.CommandTransport{Command: cmd}` wrapping `exec.Command(serverPath)`, `mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)`.
- `client.Connect(ctx, transport, nil)` returns a session; `defer session.Close()`.
- `session.ListTools(ctx, nil)`, then `json.MarshalIndent(toolsResult, "", "  ")` printed via `fmt.Println`.
- Same `ELASTIC_MCP_SERVER` env var (default `./elastic-mcp-server`) as the CLI.
- Uses `log.Fatalf` on any error — no cleanup of the spawned subprocess beyond what `exec.Command`/`CommandTransport` do implicitly. **No `stopServer`-style SIGTERM handling** — if this tool is killed abruptly, the child server may be left running, relying on `pdeath_linux.go`'s `PR_SET_PDEATHSIG` as the only safety net on Linux.
