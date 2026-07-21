package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadCodexRuntimeConfig(t *testing.T) {
	t.Run("defaults follow LLM timeout", func(t *testing.T) {
		t.Setenv("CODEX_REQUEST_MAX_RETRIES", "")
		t.Setenv("CODEX_STREAM_MAX_RETRIES", "")
		t.Setenv("CODEX_STREAM_IDLE_TIMEOUT", "")
		got := loadCodexRuntimeConfig(slog.Default(), 7*time.Second)
		if got.requestMaxRetries != 1 || got.streamMaxRetries != 1 || got.streamIdleTimeout != 7*time.Second {
			t.Fatalf("Codex runtime defaults = %d/%d/%s", got.requestMaxRetries, got.streamMaxRetries, got.streamIdleTimeout)
		}
	})

	t.Run("accepts zero retry and bounded overrides", func(t *testing.T) {
		t.Setenv("CODEX_REQUEST_MAX_RETRIES", "0")
		t.Setenv("CODEX_STREAM_MAX_RETRIES", "100")
		t.Setenv("CODEX_STREAM_IDLE_TIMEOUT", "250ms")
		got := loadCodexRuntimeConfig(slog.Default(), time.Minute)
		if got.requestMaxRetries != 0 || got.streamMaxRetries != 100 || got.streamIdleTimeout != 250*time.Millisecond {
			t.Fatalf("Codex runtime overrides = %d/%d/%s", got.requestMaxRetries, got.streamMaxRetries, got.streamIdleTimeout)
		}
	})

	t.Run("invalid values fall back", func(t *testing.T) {
		t.Setenv("CODEX_REQUEST_MAX_RETRIES", "not-a-number")
		t.Setenv("CODEX_STREAM_MAX_RETRIES", "101")
		t.Setenv("CODEX_STREAM_IDLE_TIMEOUT", "500us")
		got := loadCodexRuntimeConfig(slog.Default(), 9*time.Second)
		if got.requestMaxRetries != 1 || got.streamMaxRetries != 1 || got.streamIdleTimeout != 9*time.Second {
			t.Fatalf("Codex runtime fallbacks = %d/%d/%s", got.requestMaxRetries, got.streamMaxRetries, got.streamIdleTimeout)
		}
	})

	t.Run("invalid inherited timeout uses safe default", func(t *testing.T) {
		t.Setenv("CODEX_REQUEST_MAX_RETRIES", "")
		t.Setenv("CODEX_STREAM_MAX_RETRIES", "")
		t.Setenv("CODEX_STREAM_IDLE_TIMEOUT", "bad")
		got := loadCodexRuntimeConfig(slog.Default(), 0)
		if got.streamIdleTimeout != time.Minute {
			t.Fatalf("Codex stream idle fallback = %s", got.streamIdleTimeout)
		}
	})
}
