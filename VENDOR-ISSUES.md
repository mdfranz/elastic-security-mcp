# Vendor Issues

Bugs found in third-party dependencies that we've worked around locally but
should be reported/filed upstream.

## github.com/zendev-sh/goai (v0.8.5)

### StdioTransport.Close() sends SIGKILL instead of a graceful shutdown

**File:** `mcp/transport.go:191-206`

```go
func (t *StdioTransport) Close() error {
	...
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
	}
	return nil
}
```

`Close()` closes the child's stdin (which alone would let a well-behaved
server see EOF and exit gracefully) but immediately follows with
`cmd.Process.Kill()`, i.e. `SIGKILL`, without waiting for the child to exit on
its own. `SIGKILL` cannot be caught or deferred against, so any cleanup the
spawned server does in its own shutdown path (removing lock files, flushing
state, closing connections) never runs.

**Impact on this repo:** `cmd/server/main.go` takes an flock-based
single-instance lock and removes the lock file via `defer` on exit. Every time
`cmd/cli/main.go` closes its MCP client (normal exit, error paths, webui
shutdown), the spawned `elastic-mcp-server` subprocess was hard-killed before
its `defer` could run, leaving a stale `elastic-mcp-server.lock` file with a
dead PID after every CLI invocation.

**Workaround applied locally:** `cmd/cli/main.go` now has a `stopServer()`
helper that reads the server's PID from its lock file, sends `SIGTERM`, and
waits briefly for a graceful exit before calling the library's `Close()`
(which then no-ops since the process is already gone). All CLI shutdown paths
were switched to use it instead of calling `mcpClient.Close()` directly.

**Suggested upstream fix:** `Close()` should send `SIGTERM` first and wait
(with a bounded timeout, e.g. via `cmd.Wait()` on a goroutine with a
`select`/timer) before falling back to `SIGKILL` if the child hasn't exited.
This is the standard graceful-shutdown pattern for subprocess-based transports
and would let any MCP stdio server implementation (not just ours) run its own
cleanup on close.
