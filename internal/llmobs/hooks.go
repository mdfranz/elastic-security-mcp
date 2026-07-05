// Package llmobs provides goai lifecycle hooks that log LLM provider
// activity (latency, token usage, finish reason) to the default slog
// logger. Tool execution is dispatched manually by callers (goai.Tool
// values here have no Execute closure), so goai's tool-level hooks
// (OnToolCall, OnToolCallStart, etc.) never fire and are not used.
package llmobs

import (
	"log/slog"

	goai "github.com/zendev-sh/goai"
)

// Hooks returns goai.Option values that log one "LLM request" line before
// each model call and one "LLM response"/"LLM response error" line after,
// including latency and token usage. Pass the result into every
// goai.GenerateText call alongside WithMessages/WithTools/etc.
func Hooks() []goai.Option {
	return []goai.Option{
		goai.WithOnRequest(func(info goai.RequestInfo) {
			slog.Info("LLM request",
				"model", info.Model,
				"message_count", info.MessageCount,
				"tool_count", info.ToolCount,
			)
		}),
		goai.WithOnResponse(func(info goai.ResponseInfo) {
			if info.Error != nil {
				slog.Error("LLM response error",
					"latency_ms", info.Latency.Milliseconds(),
					"status_code", info.StatusCode,
					"error", info.Error,
				)
				return
			}
			slog.Info("LLM response",
				"latency_ms", info.Latency.Milliseconds(),
				"finish_reason", info.FinishReason,
				"input_tokens", info.Usage.InputTokens,
				"output_tokens", info.Usage.OutputTokens,
				"total_tokens", info.Usage.TotalTokens,
				"reasoning_tokens", info.Usage.ReasoningTokens,
				"cache_read_tokens", info.Usage.CacheReadTokens,
				"cache_write_tokens", info.Usage.CacheWriteTokens,
			)
		}),
	}
}
