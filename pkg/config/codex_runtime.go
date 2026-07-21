package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCodexRequestMaxRetries uint64 = 1
	DefaultCodexStreamMaxRetries  uint64 = 1
	MaxCodexRetries               uint64 = 100
	DefaultCodexStreamIdleTimeout        = 60 * time.Second
)

type codexRuntimeConfig struct {
	requestMaxRetries uint64
	streamMaxRetries  uint64
	streamIdleTimeout time.Duration
}

func loadCodexRuntimeConfig(logger *slog.Logger, llmTimeout time.Duration) codexRuntimeConfig {
	idleTimeout := llmTimeout
	if idleTimeout < time.Millisecond {
		idleTimeout = DefaultCodexStreamIdleTimeout
	}
	return codexRuntimeConfig{
		requestMaxRetries: codexRetryEnv(logger, "CODEX_REQUEST_MAX_RETRIES", DefaultCodexRequestMaxRetries),
		streamMaxRetries:  codexRetryEnv(logger, "CODEX_STREAM_MAX_RETRIES", DefaultCodexStreamMaxRetries),
		streamIdleTimeout: codexIdleTimeoutEnv(logger, idleTimeout),
	}
}

func codexRetryEnv(logger *slog.Logger, name string, defaultValue uint64) uint64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed > MaxCodexRetries {
		logger.Warn("invalid Codex retry limit", "name", name, "value", raw, "maximum", MaxCodexRetries, "error", err)
		return defaultValue
	}
	return parsed
}

func codexIdleTimeoutEnv(logger *slog.Logger, defaultValue time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv("CODEX_STREAM_IDLE_TIMEOUT"))
	if raw == "" {
		return defaultValue
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < time.Millisecond {
		logger.Warn("invalid Codex stream idle timeout", "value", raw, "minimum", time.Millisecond, "error", err)
		return defaultValue
	}
	return parsed
}
