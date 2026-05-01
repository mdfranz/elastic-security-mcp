package util

import (
	"log/slog"
	"os"
	"strings"
)

const (
	DefaultClientLogFile = "elastic-cli.log"
	DefaultServerLogFile = "elastic-mcp-server.log"
)

func ClientLogFile() string {
	if logFile := strings.TrimSpace(os.Getenv("CLIENT_LOG_FILE")); logFile != "" {
		return logFile
	}
	return DefaultClientLogFile
}

func ServerLogFile() string {
	if logFile := strings.TrimSpace(os.Getenv("SERVER_LOG_FILE")); logFile != "" {
		return logFile
	}
	return DefaultServerLogFile
}

func ClientLogLevel() slog.Level {
	return logLevelFromEnv("CLIENT_LOG_LEVEL")
}

func ServerLogLevel() slog.Level {
	return logLevelFromEnv("SERVER_LOG_LEVEL")
}

func logLevelFromEnv(envVar string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envVar))) {
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

func TruncateForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

func ClientPayloadLoggingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLIENT_LOG_PAYLOADS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
