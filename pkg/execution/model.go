package execution

import (
	"fmt"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	domain "github.com/chaitin/agent-compose/pkg/model"
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

// AgentJournalResume is a committed journal cursor with an optional terminal
// result already consumed before a daemon restart.
type AgentJournalResume struct {
	Offset     uint64
	Decoder    AgentDecoderState
	ResultJSON string
	Exit       *domain.JournalExit
}

type AgentExecutionStream struct {
	// OnRuntimeOutput receives original runtime chunks before cell transcript
	// rendering. When set, lifecycle consumers use it instead of OnFrame.
	OnRuntimeOutput func(domain.ExecChunk) error
	// OnRuntimeEnd flushes a final stdout line without a newline after process exit.
	OnRuntimeEnd func() error

	OnStart func(domain.NotebookCell) error
	OnChunk func(string, domain.ExecChunk) error
	// OnFrame receives normalized runtime frames, including turn and execution
	// completion. When set, it takes precedence over OnAgentEvent.
	OnFrame func(cellID string, frame AgentFrame) error
	// OnAgentEvent receives the JSON of each neutral agent event the guest
	// runtime reported (native frames or normalized legacy lines), separated from the output
	// OnChunk sees.
	OnAgentEvent func(cellID string, event []byte) error
}

// AgentHistoryMode selects the owner of execution history. The zero value
// keeps notebook persistence for pre-run-log callers and detached legacy runs.
type AgentHistoryMode uint8

const (
	AgentHistoryNotebook AgentHistoryMode = iota
	AgentHistoryRunLog
)

type ExecuteAgentRequest struct {
	History           AgentHistoryMode
	Agent             string
	AgentDefinitionID string
	Model             string
	ProviderEnvItems  []domain.SandboxEnvVar
	RunID             string
	Message           string
	// Input is the ordered multimodal turn. Empty keeps Message as the turn.
	Input            []agentinput.Part
	Timeout          time.Duration
	OutputSchemaJSON string
	Stream           AgentExecutionStream
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
	History       AgentHistoryMode
	Agent         string
	Message       string
	JournalResume *AgentJournalResume
	RunID         string
	CellID        string
	ExecutionID   string
	Stream        AgentExecutionStream
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
