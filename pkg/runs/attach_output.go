package runs

import (
	"time"

	domain "agent-compose/pkg/model"
)

type RunAttachOutputKind string

const (
	RunAttachOutputStarted            RunAttachOutputKind = "started"
	RunAttachOutputData               RunAttachOutputKind = "output"
	RunAttachOutputAgentEvent         RunAttachOutputKind = "agent_event"
	RunAttachOutputAgentTurnCompleted RunAttachOutputKind = "agent_turn_completed"
	RunAttachOutputResult             RunAttachOutputKind = "result"
	RunAttachOutputError              RunAttachOutputKind = "error"
)

type RunAttachOutput struct {
	Kind        RunAttachOutputKind
	CreatedAt   time.Time
	Run         domain.ProjectRunRecord
	SandboxID   string
	Warnings    []string
	Data        []byte
	Stream      domain.StdioStream
	TTY         bool
	Name        string
	Text        string
	PayloadJSON string
	ResultJSON  string
	Output      string
	Error       string
	Code        string
	ExitCode    int
	Success     bool
	Terminal    bool
}

type RunAttachSender func(RunAttachOutput) error
