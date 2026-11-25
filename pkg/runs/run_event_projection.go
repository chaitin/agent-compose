package runs

import (
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type agentTurnProjection struct {
	Transcript      string
	FinalText       string
	FinalTextSource domain.AgentFinalTextSource
	Provider        string
	StopReason      string
}

func attachedAgentTurnEvents(run domain.ProjectRunRecord, sequence uint64, frame []byte, turn agentTurnProjection) []domain.ProjectRunEventRecord {
	finalText := projectableFinalText(turn)
	if finalText == "" {
		return nil
	}
	return []domain.ProjectRunEventRecord{{
		ID:         attachedAgentEventID(run.RunID, sequence, frame),
		RunID:      run.RunID,
		Kind:       domain.ProjectRunEventKindAgentMessage,
		Text:       finalText,
		Agent:      firstNonEmpty(turn.Provider, run.AgentName),
		Success:    true,
		StopReason: turn.StopReason,
	}}
}

func terminalPromptTurnEvents(run domain.ProjectRunRecord, turn agentTurnProjection) []domain.ProjectRunEventRecord {
	finalText := projectableFinalText(turn)
	if finalText == "" {
		return nil
	}
	return []domain.ProjectRunEventRecord{{
		ID:         terminalAgentEventID(run.RunID),
		RunID:      run.RunID,
		Kind:       domain.ProjectRunEventKindAgentMessage,
		Text:       finalText,
		Agent:      firstNonEmpty(turn.Provider, run.AgentName),
		Success:    true,
		StopReason: turn.StopReason,
	}}
}

func projectableFinalText(turn agentTurnProjection) string {
	finalText := strings.TrimSpace(turn.FinalText)
	if turn.FinalTextSource == domain.AgentFinalTextSourceProviderMessage {
		return finalText
	}
	if turn.FinalTextSource != "" || finalText == "" {
		return ""
	}
	if transcript := strings.TrimSpace(turn.Transcript); transcript != "" && transcript == finalText {
		return ""
	}
	return finalText
}
