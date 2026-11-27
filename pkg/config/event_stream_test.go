package config

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/samber/do/v2"
)

func clearEventStreamEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		eventStreamEnabledName,
		eventStreamMaxSubscribersName,
		eventStreamSubscriberBufferName,
		eventStreamReplayEventsName,
		eventStreamIdleTimeoutName,
		eventStreamHeartbeatName,
		eventStreamMaxBatchEventsName,
	} {
		t.Setenv(name, "")
	}
}

func TestLoadEventStreamConfigDefaults(t *testing.T) {
	clearEventStreamEnv(t)
	cfg, err := loadEventStreamConfig()
	if err != nil {
		t.Fatalf("loadEventStreamConfig: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("event stream enabled by default")
	}
	if cfg.MaxSubscribers != 32 || cfg.SubscriberBuffer != 512 || cfg.ReplayEvents != 4096 ||
		cfg.IdleTimeout != 5*time.Minute || cfg.Heartbeat != 15*time.Second || cfg.MaxBatchEvents != 128 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadEventStreamConfigOverrides(t *testing.T) {
	clearEventStreamEnv(t)
	t.Setenv(eventStreamEnabledName, "true")
	t.Setenv(eventStreamMaxSubscribersName, "7")
	t.Setenv(eventStreamSubscriberBufferName, "9")
	t.Setenv(eventStreamReplayEventsName, "11")
	t.Setenv(eventStreamIdleTimeoutName, "2m")
	t.Setenv(eventStreamHeartbeatName, "3s")
	t.Setenv(eventStreamMaxBatchEventsName, "13")
	cfg, err := loadEventStreamConfig()
	if err != nil {
		t.Fatalf("loadEventStreamConfig: %v", err)
	}
	if !cfg.Enabled || cfg.MaxSubscribers != 7 || cfg.SubscriberBuffer != 9 || cfg.ReplayEvents != 11 ||
		cfg.IdleTimeout != 2*time.Minute || cfg.Heartbeat != 3*time.Second || cfg.MaxBatchEvents != 13 {
		t.Fatalf("overrides = %+v", cfg)
	}
}

func TestLoadEventStreamConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "bad bool", key: eventStreamEnabledName, value: "maybe"},
		{name: "zero subscribers", key: eventStreamMaxSubscribersName, value: "0"},
		{name: "zero buffer", key: eventStreamSubscriberBufferName, value: "0"},
		{name: "negative replay", key: eventStreamReplayEventsName, value: "-1"},
		{name: "bad duration", key: eventStreamIdleTimeoutName, value: "later"},
		{name: "short heartbeat", key: eventStreamHeartbeatName, value: "100ms"},
		{name: "zero batch", key: eventStreamMaxBatchEventsName, value: "0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEventStreamEnv(t)
			t.Setenv(test.key, test.value)
			if _, err := loadEventStreamConfig(); err == nil {
				t.Fatalf("accepted %s=%q", test.key, test.value)
			}
		})
	}
}

func TestNewConfigIncludesEventStream(t *testing.T) {
	clearEventStreamEnv(t)
	t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
	t.Setenv(eventStreamEnabledName, "true")
	t.Setenv(eventStreamMaxSubscribersName, "5")
	di := do.New()
	do.ProvideValue(di, slog.Default())
	cfg, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if !cfg.EventStream.Enabled || cfg.EventStream.MaxSubscribers != 5 {
		t.Fatalf("EventStream = %+v", cfg.EventStream)
	}
}
