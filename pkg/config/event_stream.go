package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	eventStreamEnabledName          = "EVENT_STREAM_ENABLED"
	eventStreamMaxSubscribersName   = "EVENT_STREAM_MAX_SUBSCRIBERS"
	eventStreamSubscriberBufferName = "EVENT_STREAM_SUBSCRIBER_BUFFER"
	eventStreamReplayEventsName     = "EVENT_STREAM_REPLAY_EVENTS"
	eventStreamIdleTimeoutName      = "EVENT_STREAM_IDLE_TIMEOUT"
	eventStreamHeartbeatName        = "EVENT_STREAM_HEARTBEAT"
	eventStreamMaxBatchEventsName   = "EVENT_STREAM_MAX_BATCH_EVENTS"
)

// EventStreamConfig bounds the opt-in daemon observation stream. The zero value
// is not meaningful; loadEventStreamConfig fills every field. The broker has
// its own fallback defaults for direct construction and tests.
type EventStreamConfig struct {
	Enabled          bool
	MaxSubscribers   int
	SubscriberBuffer int
	ReplayEvents     int
	IdleTimeout      time.Duration
	Heartbeat        time.Duration
	MaxBatchEvents   int
}

func defaultEventStreamConfig() EventStreamConfig {
	return EventStreamConfig{
		Enabled:          false,
		MaxSubscribers:   32,
		SubscriberBuffer: 512,
		ReplayEvents:     4096,
		IdleTimeout:      5 * time.Minute,
		Heartbeat:        15 * time.Second,
		MaxBatchEvents:   128,
	}
}

func loadEventStreamConfig() (EventStreamConfig, error) {
	cfg := defaultEventStreamConfig()
	raw := strings.TrimSpace(os.Getenv(eventStreamEnabledName))
	if raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return EventStreamConfig{}, fmt.Errorf("%s: parse %q: %w", eventStreamEnabledName, raw, err)
		}
		cfg.Enabled = enabled
	}
	var err error
	if cfg.MaxSubscribers, err = intEnvAtLeast(eventStreamMaxSubscribersName, cfg.MaxSubscribers, 1); err != nil {
		return EventStreamConfig{}, err
	}
	if cfg.SubscriberBuffer, err = intEnvAtLeast(eventStreamSubscriberBufferName, cfg.SubscriberBuffer, 1); err != nil {
		return EventStreamConfig{}, err
	}
	if cfg.ReplayEvents, err = intEnvAtLeast(eventStreamReplayEventsName, cfg.ReplayEvents, 0); err != nil {
		return EventStreamConfig{}, err
	}
	if cfg.MaxBatchEvents, err = intEnvAtLeast(eventStreamMaxBatchEventsName, cfg.MaxBatchEvents, 1); err != nil {
		return EventStreamConfig{}, err
	}
	if cfg.IdleTimeout, err = durationEnvAtLeast(eventStreamIdleTimeoutName, cfg.IdleTimeout, time.Second); err != nil {
		return EventStreamConfig{}, err
	}
	if cfg.Heartbeat, err = durationEnvAtLeast(eventStreamHeartbeatName, cfg.Heartbeat, time.Second); err != nil {
		return EventStreamConfig{}, err
	}
	return cfg, nil
}

func intEnvAtLeast(name string, defaultValue, minimum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: parse %q: %w", name, raw, err)
	}
	if value < minimum {
		return 0, fmt.Errorf("%s must be at least %d", name, minimum)
	}
	return value, nil
}

func durationEnvAtLeast(name string, defaultValue, minimum time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: parse %q: %w", name, raw, err)
	}
	if value < minimum {
		return 0, fmt.Errorf("%s must be at least %s", name, minimum)
	}
	return value, nil
}
