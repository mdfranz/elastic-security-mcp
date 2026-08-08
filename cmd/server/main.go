package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"

	"github.com/mfranz/elastic-security-mcp/internal/elasticsearch"
	"github.com/mfranz/elastic-security-mcp/internal/kibana"
	"github.com/mfranz/elastic-security-mcp/internal/util"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Ensure we terminate if the parent process exits (Linux only)
	setParentDeathSignal()

	// 1. Locking Setup
	lockFile := util.ServerLockFile()

	lf, err := acquireServerLock(lockFile)
	if err != nil {
		return err
	}

	// We have the lock; ensure we close the file descriptor (which releases the lock)
	// and delete the lockfile when run() exits.
	defer func() {
		lf.Close()
		_ = os.Remove(lockFile)
	}()

	// Write PID to lock file
	lf.Truncate(0)
	lf.Seek(0, 0)
	fmt.Fprintf(lf, "%d\n", os.Getpid())
	lf.Sync()

	// 2. Logging Setup
	logFile := util.ServerLogFile()

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", logFile, err)
	}
	defer f.Close()

	// Initialize slog to write to the file so it doesn't corrupt stdio MCP transport
	logger := slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: util.ServerLogLevel(),
	}))
	slog.SetDefault(logger)

	slog.Info("Server acquired lock", "file", lockFile, "pid", os.Getpid())

	// 2. Environment Variables
	elasticURL := os.ExpandEnv(os.Getenv("ELASTIC_URL"))
	elasticKey := os.ExpandEnv(os.Getenv("ELASTIC_KEY"))

	kibanaURL := os.ExpandEnv(os.Getenv("KIBANA_URL"))
	kibanaUser := os.ExpandEnv(os.Getenv("KIBANA_USER"))
	kibanaPass := os.ExpandEnv(os.Getenv("KIBANA_PASS"))
	kibanaKey := os.ExpandEnv(os.Getenv("KIBANA_KEY"))

	if elasticURL == "" || elasticKey == "" {
		slog.Error("ELASTIC_URL and ELASTIC_KEY environment variables must be set")
		return fmt.Errorf("ELASTIC_URL and ELASTIC_KEY environment variables must be set")
	}
	if err := validateEndpointURL("ELASTIC_URL", elasticURL); err != nil {
		slog.Error("Invalid Elasticsearch URL", "error", err)
		return err
	}
	if kibanaURL != "" {
		if err := validateEndpointURL("KIBANA_URL", kibanaURL); err != nil {
			slog.Error("Invalid Kibana URL", "error", err)
			return err
		}
	}

	// 2. Initialize Elasticsearch Client
	es, err := elasticsearch.NewClient(elasticURL, elasticKey)
	if err != nil {
		slog.Error("Error creating the elasticsearch client", "error", err)
		return fmt.Errorf("error creating the elasticsearch client: %w", err)
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

	if kibanaURL != "" {
		kb, err := kibana.NewClient(kibanaURL, kibanaUser, kibanaPass, kibanaKey)
		if err != nil {
			slog.Error("Error creating the kibana client", "error", err)
			return fmt.Errorf("error creating the kibana client: %w", err)
		}
		kibana.RegisterTools(server, kb)
		slog.Info("Kibana tools registered", "url", kibanaURL)
	} else {
		slog.Info("KIBANA_URL not set; skipping Kibana tools registration")
	}

	// 5. Run Server over Stdio
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	slog.Info("Server listening on stdio")
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		slog.Error("server run error", "error", err)
		return fmt.Errorf("server run error: %w", err)
	}

	return nil
}

func validateEndpointURL(name, value string) error {
	if strings.Contains(value, "${") {
		return fmt.Errorf("%s contains an unresolved environment variable placeholder", name)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", name, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use an http or https scheme", name)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must include a host", name)
	}
	return nil
}
