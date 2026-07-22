package tui

import (
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/cmd/installer/internal/core"
)

func TestAppendEventMergesLayerProgressInPlace(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	// Docker repaints every active layer on each tick.
	for range 4 {
		m.appendEvent("9319a554cac0 Downloading [====>    ]  9.437MB/32.5MB")
		m.appendEvent("c3f793399f14 Downloading [========>]  39.85MB/48.87MB")
	}
	if len(m.events) != 2 {
		t.Fatalf("repeated progress produced %d lines: %#v", len(m.events), m.events)
	}

	m.appendEvent("9319a554cac0 Downloading [=======> ]  18.2MB/32.5MB")
	if len(m.events) != 2 || !strings.Contains(m.events[0].text, "18.2MB") {
		t.Fatalf("layer update did not overwrite in place: %#v", m.events)
	}
}

func TestAppendEventFreezesCompletedLayers(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	m.appendEvent("9319a554cac0 Downloading [====>    ]  9.437MB/32.5MB")
	m.appendEvent("9319a554cac0 Pull complete")
	if len(m.events) != 1 || m.events[0].text != "9319a554cac0 Pull complete" {
		t.Fatalf("terminal status did not replace the live line: %#v", m.events)
	}
	if m.events[0].key != "" {
		t.Fatalf("completed layer stayed live: %#v", m.events[0])
	}

	// A later pull of the same layer id must not overwrite the frozen history.
	m.appendEvent("9319a554cac0 Downloading [=>       ]  1MB/32.5MB")
	if len(m.events) != 2 {
		t.Fatalf("frozen line was reused: %#v", m.events)
	}
}

func TestAppendEventKeepsNonLayerLinesDistinct(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	for _, message := range []string{
		"Pulling images",
		"Starting agent-compose",
		"deadbeef Already exists",
		"not-a-layer Downloading [==>]",
	} {
		m.appendEvent(message)
	}
	if len(m.events) != 4 {
		t.Fatalf("unrelated lines were merged: %#v", m.events)
	}
	for i, entry := range m.events {
		if entry.key != "" {
			t.Fatalf("line %d became live: %#v", i, entry)
		}
	}
}

func TestAppendEventBoundsRetainedHistory(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	for i := range maxLogEntries + 10 {
		m.appendEvent(string(rune('a'+i%26)) + "-line")
	}
	if len(m.events) != maxLogEntries {
		t.Fatalf("history = %d entries, want %d", len(m.events), maxLogEntries)
	}
}
