package execution

import (
	domain "agent-compose/pkg/model"
	"fmt"
	"strings"
	"time"
)

const (
	CellTypeShell      = "shell"
	CellTypeJavaScript = "javascript"
	CellTypePython     = "python"
	CellTypeAgent      = "agent"
)

type CellExecutionStream struct {
	OnStart func(domain.NotebookCell) error
	OnChunk func(string, domain.ExecChunk) error
}

type AgentExecutionStream struct {
	OnStart func(domain.NotebookCell) error
	OnChunk func(string, domain.ExecChunk) error
}

type ExecuteAgentRequest struct {
	Agent             string
	AgentDefinitionID string
	Model             string
	ProviderEnvItems  []domain.SessionEnvVar
	RunID             string
	Message           string
	Timeout           time.Duration
	OutputSchemaJSON  string
	Stream            AgentExecutionStream
}

func NormalizeCellType(cellType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(cellType)) {
	case "", CellTypeJavaScript:
		return CellTypeJavaScript, nil
	case CellTypeShell:
		return CellTypeShell, nil
	case CellTypePython:
		return CellTypePython, nil
	default:
		return "", fmt.Errorf("unsupported cell type %q", cellType)
	}
}
