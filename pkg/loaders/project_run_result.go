package loaders

import (
	"encoding/json"

	domain "agent-compose/pkg/model"
)

type ProjectRunResultFields struct {
	CellID         string `json:"cellId"`
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agentSessionId"`
	StopReason     string `json:"stopReason"`
}

func AgentResultFromProjectRun(run domain.ProjectRunRecord, outputSchemaJSON string) (domain.LoaderAgentResult, error) {
	metadata := ProjectRunResultMetadata(run.ResultJSON)
	text := firstNonEmpty(run.Output, run.Error)
	jsonValue, jsonErr := JSONResult(text, outputSchemaJSON, "project run output")
	return domain.LoaderAgentResult{
		Text:           text,
		Output:         run.Output,
		FinalText:      run.Output,
		JSON:           jsonValue,
		SessionID:      run.SessionID,
		CellID:         metadata.CellID,
		Agent:          firstNonEmpty(metadata.Agent, run.AgentName),
		AgentSessionID: metadata.AgentSessionID,
		StopReason:     metadata.StopReason,
		Success:        run.Status == domain.ProjectRunStatusSucceeded,
		ExitCode:       run.ExitCode,
	}, jsonErr
}

func ProjectRunResultMetadata(resultJSON string) ProjectRunResultFields {
	var metadata ProjectRunResultFields
	_ = json.Unmarshal([]byte(resultJSON), &metadata)
	return metadata
}
