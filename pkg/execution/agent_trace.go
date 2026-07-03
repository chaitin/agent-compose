package execution

import (
	domain "agent-compose/pkg/model"
	"strings"
	"time"

	"github.com/google/uuid"
)

func AgentTraceEvents(transcript string, createdAt time.Time) []domain.SessionEvent {
	lines := strings.Split(transcript, "\n")
	events := make([]domain.SessionEvent, 0)
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		eventType, name, ok := ParseAgentTraceMarker(line)
		if !ok {
			continue
		}
		details, consumed := CollectAgentTraceDetails(eventType, lines[index+1:])
		index += consumed
		message := name
		if strings.TrimSpace(details) != "" {
			if message == "" {
				message = strings.TrimSpace(details)
			} else {
				message += "\n" + strings.TrimSpace(details)
			}
		}
		events = append(events, domain.SessionEvent{
			ID:        uuid.NewString(),
			Type:      eventType,
			Level:     "info",
			Message:   message,
			CreatedAt: createdAt,
		})
	}
	return events
}

func CollectAgentTraceDetails(eventType string, lines []string) (string, int) {
	details := make([]string, 0, len(lines))
	for offset, raw := range lines {
		line := strings.TrimSpace(raw)
		if _, _, marker := ParseAgentTraceMarker(line); marker {
			return strings.Join(details, "\n"), offset
		}
		if eventType != "agent.assistant" && line == "" {
			return strings.Join(details, "\n"), offset + 1
		}
		details = append(details, raw)
	}
	return strings.Join(details, "\n"), len(lines)
}

func ParseAgentTraceMarker(line string) (string, string, bool) {
	if strings.HasPrefix(line, "[tool:") && strings.HasSuffix(line, "]") {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[tool:"), "]"))
		if name != "" {
			return "agent.tool", name, true
		}
	}
	if strings.HasPrefix(line, "[hook:") && strings.HasSuffix(line, "]") {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[hook:"), "]"))
		if name != "" {
			return "agent.hook", name, true
		}
	}
	return "", "", false
}
