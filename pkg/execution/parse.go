package execution

import (
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	AgentResultPrefix   = "__AGENT_RESULT__"
	CommandResultPrefix = "__COMMAND_RESULT__"
)

type agentExecResponse struct {
	Provider        string `json:"provider"`
	ThreadID        string `json:"threadId"`
	StopReason      string `json:"stopReason"`
	FinalText       string `json:"finalText"`
	FinalTextSource string `json:"finalTextSource"`
	JSON            any    `json:"json"`
	Transcript      string `json:"transcript"`
	Stderr          string `json:"stderr"`
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
	finalText := strings.TrimSpace(payload.FinalText)
	transcript := strings.TrimSpace(payload.Transcript)
	exitCode := result.ExitCode
	success := result.Success
	if strings.EqualFold(strings.TrimSpace(payload.StopReason), "cancelled") {
		exitCode = FirstNonZeroInt(exitCode, 1)
		success = false
	}
	return domain.AgentRunResult{
		Agent:           firstNonEmpty(strings.TrimSpace(payload.Provider), domain.NormalizeAgentKind(agent)),
		DisplayOutput:   humanOutput,
		FinalText:       finalText,
		FinalTextSource: normalizeFinalTextSource(payload.FinalTextSource, finalText, transcript),
		JSONText:        finalText,
		Transcript:      transcript,
		ThreadID:        strings.TrimSpace(payload.ThreadID),
		StopReason:      strings.TrimSpace(payload.StopReason),
		ExitCode:        exitCode,
		Success:         success,
	}, nil
}

func normalizeFinalTextSource(source, finalText, transcript string) domain.AgentFinalTextSource {
	switch normalized := domain.AgentFinalTextSource(strings.TrimSpace(source)); normalized {
	case domain.AgentFinalTextSourceProviderMessage:
		return domain.AgentFinalTextSourceProviderMessage
	case domain.AgentFinalTextSourceTranscriptFallback:
		return domain.AgentFinalTextSourceTranscriptFallback
	case domain.AgentFinalTextSourceNone:
		return domain.AgentFinalTextSourceNone
	case "":
		if finalText == "" {
			return domain.AgentFinalTextSourceNone
		}
		if transcript != "" && finalText == transcript {
			return domain.AgentFinalTextSourceTranscriptFallback
		}
		return domain.AgentFinalTextSourceProviderMessage
	default:
		if finalText == "" {
			return domain.AgentFinalTextSourceNone
		}
		return domain.AgentFinalTextSourceTranscriptFallback
	}
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
	runes := []rune(detail)
	if len(runes) > 240 {
		detail = string(runes[:240]) + "..."
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

func StripCommandResultPayload(raw string) string {
	idx := strings.Index(raw, CommandResultPrefix)
	if idx < 0 {
		return raw
	}
	return raw[:idx]
}

func FilterAgentStreamChunk(chunk domain.ExecChunk) (domain.ExecChunk, bool) {
	filtered := chunk
	filtered.Text = StripAgentResultPayload(chunk.Text)
	return filtered, filtered.Text != ""
}

func FilterCommandStreamChunk(chunk domain.ExecChunk) (domain.ExecChunk, bool) {
	filtered := chunk
	filtered.Text = StripCommandResultPayload(chunk.Text)
	return filtered, filtered.Text != ""
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
		if payload, ok := parseCommandExecLine(line); ok {
			return payload, true
		}
	}
	return domain.RuntimeCommandResult{}, false
}

// parseCommandExecLine locates CommandResultPrefix within a single line and
// decodes the JSON that follows it. The prefix can legitimately appear more
// than once on a line: the command's own output may quote the prefix, and
// that occurrence sits to the right of the real one once the wrapper embeds
// the captured output inside the result JSON. Trying every occurrence (and
// requiring the remainder to actually decode) avoids latching onto a quoted
// occurrence instead of the genuine marker.
func parseCommandExecLine(line string) (domain.RuntimeCommandResult, bool) {
	searchFrom := 0
	for {
		idx := strings.Index(line[searchFrom:], CommandResultPrefix)
		if idx < 0 {
			break
		}
		idx += searchFrom
		candidate := strings.TrimSpace(line[idx+len(CommandResultPrefix):])
		if strings.HasPrefix(candidate, "{") {
			var payload domain.RuntimeCommandResult
			if json.Unmarshal([]byte(candidate), &payload) == nil {
				return payload, true
			}
		}
		searchFrom = idx + len(CommandResultPrefix)
	}
	if strings.HasPrefix(line, "{") {
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
