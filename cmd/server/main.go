package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/mfranz/elastic-security-mcp/internal/elasticsearch"
	"github.com/mfranz/elastic-security-mcp/internal/util"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// 1. Locking Setup
	lockFile := util.ServerLockFile()

	lf, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open lock file %s: %v\n", lockFile, err)
		os.Exit(1)
	}
	defer lf.Close()

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pidData, _ := os.ReadFile(lockFile)
		pidStr := strings.TrimSpace(string(pidData))
		if pidStr != "" {
			if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					if err := proc.Signal(syscall.Signal(0)); err == nil {
						fmt.Fprintf(os.Stderr, "Error: elastic-mcp-server (PID %d) is already running.\n", pid)
						os.Exit(1)
					}
				}
			}
		}
		fmt.Fprintf(os.Stderr, "Error: elastic-mcp-server is already running (lock on %s).\n", lockFile)
		os.Exit(1)
	}

	// We have the lock; ensure we cleanup on exit
	defer os.Remove(lockFile)

	// Write PID to lock file
	lf.Truncate(0)
	lf.Seek(0, 0)
	fmt.Fprintf(lf, "%d\n", os.Getpid())
	lf.Sync()

	// 2. Logging Setup
	logFile := util.ServerLogFile()

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", logFile, err)
		os.Exit(1)
	}
	defer f.Close()

	// Initialize slog to write to the file so it doesn't corrupt stdio MCP transport
	logger := slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: util.ServerLogLevel(),
	}))
	slog.SetDefault(logger)

	slog.Info("Server acquired lock", "file", lockFile, "pid", os.Getpid())

	// 2. Environment Variables
	elasticURL := os.Getenv("ELASTIC_URL")
	elasticKey := os.Getenv("ELASTIC_KEY")

	if elasticURL == "" || elasticKey == "" {
		slog.Error("ELASTIC_URL and ELASTIC_KEY environment variables must be set")
		os.Exit(1)
	}

	// 2. Initialize Elasticsearch Client
	es, err := elasticsearch.NewClient(elasticURL, elasticKey)
	if err != nil {
		slog.Error("Error creating the elasticsearch client", "error", err)
		os.Exit(1)
	}

	// Skip connectivity check in this version for simplicity,
	// or make it optional. For now, let's keep it but handle it gracefully.
	res, err := es.Raw.Info()
	if err != nil {
		slog.Warn("Could not connect to Elasticsearch", "error", err)
	} else {
		res.Body.Close()
	}

	slog.Info("Starting elastic-mcp-server", "url", elasticURL)

	// 3. Create MCP Server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "elastic-mcp-server",
			Version: "1.0.0",
		},
		nil,
	)

	// 4. Register Tools
	elasticsearch.RegisterTools(server, es)

	// 5. Run Server over Stdio
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("Server listening on stdio")
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		slog.Error("server run error", "error", err)
		os.Exit(1)
	}
}
