package execution

import (
	"fmt"
	domain "github.com/chaitin/agent-compose/pkg/model"
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
	ProviderEnvItems  []domain.SandboxEnvVar
	RunID             string
	Message           string
	Timeout           time.Duration
	OutputSchemaJSON  string
	Stream            AgentExecutionStream
	// Detach runs the agent behind the guest supervisor so it can outlive a
	// daemon restart. nil keeps the direct exec.
	Detach *AgentDetach
}

// AgentDetach is the detached-execution part of an agent request.
type AgentDetach struct {
	ExecutionID string
	// OnStart records the execution on the run before the task starts, with
	// the absolute deadline the run's timeout ends at (zero for none).
	OnStart func(deadline time.Time) error
}

// ResumeAgentRequest re-attaches to a detached agent run started by an
// earlier daemon and finishes its cell.
type ResumeAgentRequest struct {
	RunID       string
	CellID      string
	ExecutionID string
	Stream      AgentExecutionStream
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
