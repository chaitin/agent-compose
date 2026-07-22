package tui

import "regexp"

const maxLogEntries = 50

// layerProgressPattern matches docker's per-layer progress lines, e.g.
// "9319a554cac0 Downloading [====>      ]  9.437MB/32.5MB".
var layerProgressPattern = regexp.MustCompile(`^([0-9a-f]{6,64}) (.+)$`)

// terminalLayerStatuses end a layer's progress. Docker reports progress by
// repainting every active layer on each tick, and the command writer turns
// those repaints into fresh lines, so without merging a single pull floods the
// log with near-identical lines.
var terminalLayerStatuses = map[string]bool{
	"Pull complete":  true,
	"Already exists": true,
}

// logEntry is one rendered line. A non-empty key means the line is still live
// and later updates for the same layer overwrite it in place; clearing the key
// freezes the line as history so completed layers accumulate visibly.
type logEntry struct {
	key  string
	text string
}

func progressKey(message string) (string, bool) {
	match := layerProgressPattern.FindStringSubmatch(message)
	if match == nil {
		return "", false
	}
	return match[1], terminalLayerStatuses[match[2]]
}

func (m *model) appendEvent(message string) {
	if message == "" {
		return
	}
	key, terminal := progressKey(message)
	if key != "" {
		for i := range m.events {
			if m.events[i].key != key {
				continue
			}
			m.events[i].text = message
			if terminal {
				m.events[i].key = ""
			}
			return
		}
	}
	if terminal {
		key = ""
	}
	m.events = append(m.events, logEntry{key: key, text: message})
	if len(m.events) > maxLogEntries {
		m.events = append([]logEntry(nil), m.events[len(m.events)-maxLogEntries:]...)
	}
}

func (m *model) visibleEvents() []string {
	limit := m.height - 14
	if m.width < 80 {
		limit = m.height - 7
	}
	if limit < 3 {
		limit = 3
	}
	if limit > 12 {
		limit = 12
	}
	entries := m.events
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	lines := make([]string, len(entries))
	for i, entry := range entries {
		lines[i] = entry.text
	}
	return lines
}
