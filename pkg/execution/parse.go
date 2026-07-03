package execution

import (
	"encoding/json"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

const (
	AgentResultPrefix   = "__AGENT_RESULT__"
	CommandResultPrefix = "__COMMAND_RESULT__"
)

type agentExecResponse struct {
	Provider   string `json:"provider"`
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	FinalText  string `json:"finalText"`
	JSON       any    `json:"json"`
	Transcript string `json:"transcript"`
	Stderr     string `json:"stderr"`
}

func ParseAgentExecResult(agent string, result domain.ExecResult) (domain.AgentRunResult, error) {
	raw := firstNonEmpty(result.Stdout, result.Output)
	if strings.TrimSpace(raw) == "" {
		if detail := SummarizeAgentExecFailure(result); detail != "" {
			return domain.AgentRunResult{}, fmt.Errorf("agent %s returned empty stdout: %s", agent, detail)
		}
		return domain.AgentRunResult{}, fmt.Errorf("agent %s returned empty stdout", agent)
	}
	payload, ok := findAgentExecPayload(raw)
	if !ok && strings.TrimSpace(result.Output) != strings.TrimSpace(raw) {
		payload, ok = findAgentExecPayload(result.Output)
	}
	if !ok {
		if detail := SummarizeAgentExecFailure(result); detail != "" {
			return domain.AgentRunResult{}, fmt.Errorf("decode agent result for %s: no result payload found: %s", agent, detail)
		}
		return domain.AgentRunResult{}, fmt.Errorf("decode agent result for %s: no result payload found", agent)
	}
	humanOutput := strings.TrimSpace(result.Stderr)
	if transcript := strings.TrimSpace(payload.Transcript); transcript != "" {
		humanOutput = transcript
	} else if strings.TrimSpace(humanOutput) == "" {
		humanOutput = strings.TrimSpace(payload.FinalText)
	}
	return domain.AgentRunResult{
		Agent:         firstNonEmpty(strings.TrimSpace(payload.Provider), domain.NormalizeAgentKind(agent)),
		DisplayOutput: humanOutput,
		FinalText:     strings.TrimSpace(payload.FinalText),
		JSONText:      strings.TrimSpace(payload.FinalText),
		Transcript:    strings.TrimSpace(payload.Transcript),
		SessionID:     strings.TrimSpace(payload.SessionID),
		StopReason:    strings.TrimSpace(payload.StopReason),
		ExitCode:      result.ExitCode,
		Success:       result.Success,
	}, nil
}

func ParseCommandExecResult(result domain.ExecResult) (domain.RuntimeCommandResult, error) {
	raw := firstNonEmpty(result.Stdout, result.Output)
	if strings.TrimSpace(raw) == "" {
		return domain.RuntimeCommandResult{}, fmt.Errorf("decode command result: empty stdout")
	}
	payload, ok := findCommandExecPayload(raw)
	if !ok && strings.TrimSpace(result.Output) != strings.TrimSpace(raw) {
		payload, ok = findCommandExecPayload(result.Output)
	}
	if !ok {
		return domain.RuntimeCommandResult{}, fmt.Errorf("decode command result: no result payload found")
	}
	return payload, nil
}

func SummarizeAgentExecFailure(result domain.ExecResult) string {
	detail := strings.TrimSpace(firstNonEmpty(result.Stderr, result.Output, result.Stdout))
	if detail == "" {
		return ""
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 240 {
		detail = detail[:240] + "..."
	}
	return detail
}

func StripAgentResultPayload(raw string) string {
	idx := strings.LastIndex(raw, AgentResultPrefix)
	if idx < 0 {
		return raw
	}
	return raw[:idx]
}

func SanitizeAgentExecResult(result domain.ExecResult) domain.ExecResult {
	cleaned := result
	cleaned.Stdout = StripAgentResultPayload(result.Stdout)
	cleaned.Output = StripAgentResultPayload(result.Output)
	return cleaned
}

func findAgentExecPayload(raw string) (agentExecResponse, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, AgentResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, AgentResultPrefix))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var payload agentExecResponse
		if json.Unmarshal([]byte(line), &payload) == nil {
			return payload, true
		}
	}
	return agentExecResponse{}, false
}

func findCommandExecPayload(raw string) (domain.RuntimeCommandResult, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, CommandResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, CommandResultPrefix))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var payload domain.RuntimeCommandResult
		if json.Unmarshal([]byte(line), &payload) == nil {
			return payload, true
		}
	}
	return domain.RuntimeCommandResult{}, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
