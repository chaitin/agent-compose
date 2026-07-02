package exec

import (
	"fmt"
	"strings"
)

func SummarizeAgentResult(result AgentRunResult) string {
	body := FirstNonEmpty(result.FinalText, result.DisplayOutput, result.Transcript)
	if strings.TrimSpace(body) == "" {
		if result.Success {
			return fmt.Sprintf("%s finished without output", result.Agent)
		}
		return fmt.Sprintf("%s failed without output", result.Agent)
	}
	return body
}

func AgentTraceEvents(transcript string) []AgentTraceEvent {
	lines := strings.Split(transcript, "\n")
	events := make([]AgentTraceEvent, 0)
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		eventType, name, ok := parseAgentTraceMarker(line)
		if !ok {
			continue
		}
		details, consumed := collectAgentTraceDetails(eventType, lines[index+1:])
		index += consumed
		message := name
		if strings.TrimSpace(details) != "" {
			if message == "" {
				message = strings.TrimSpace(details)
			} else {
				message += "\n" + strings.TrimSpace(details)
			}
		}
		events = append(events, AgentTraceEvent{
			Type:    eventType,
			Level:   "info",
			Message: message,
		})
	}
	return events
}

func collectAgentTraceDetails(eventType string, lines []string) (string, int) {
	details := make([]string, 0, len(lines))
	for offset, raw := range lines {
		line := strings.TrimSpace(raw)
		if _, _, marker := parseAgentTraceMarker(line); marker {
			return strings.Join(details, "\n"), offset
		}
		if eventType != "agent.assistant" && line == "" {
			return strings.Join(details, "\n"), offset + 1
		}
		details = append(details, raw)
	}
	return strings.Join(details, "\n"), len(lines)
}

func parseAgentTraceMarker(line string) (string, string, bool) {
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
