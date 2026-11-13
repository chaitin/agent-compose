package runs

import (
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type RunAttachOutputKind string

const (
	RunAttachOutputStarted            RunAttachOutputKind = "started"
	RunAttachOutputInputReady         RunAttachOutputKind = "input_ready"
	RunAttachOutputData               RunAttachOutputKind = "output"
	RunAttachOutputAgentEvent         RunAttachOutputKind = "agent_event"
	RunAttachOutputAgentTurnCompleted RunAttachOutputKind = "agent_turn_completed"
	RunAttachOutputResult             RunAttachOutputKind = "result"
	RunAttachOutputError              RunAttachOutputKind = "error"
)

type RunAttachOutput struct {
	Input       *domain.RunInputPreparation
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

func runAttachStartedResponse(run domain.ProjectRunRecord, sandbox *domain.Sandbox, warnings []string, startedAt time.Time) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputStarted, CreatedAt: startedAt, Run: run, SandboxID: sandbox.Summary.ID, Warnings: append([]string(nil), warnings...)}
}

func runAttachOutputResponse(data []byte, stream domain.StdioStream, tty bool) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputData, CreatedAt: time.Now().UTC(), Data: append([]byte(nil), data...), Stream: stream, TTY: tty}
}

func runAttachAgentEventResponse(name, text, payloadJSON string) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputAgentEvent, CreatedAt: time.Now().UTC(), Name: name, Text: text, PayloadJSON: payloadJSON}
}

func runAttachAgentTurnCompletedResponse(run domain.ProjectRunRecord, resultJSON string, warnings []string) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputAgentTurnCompleted, CreatedAt: time.Now().UTC(), Run: run, ResultJSON: resultJSON, Warnings: append([]string(nil), warnings...)}
}

func runAttachResultResponse(run domain.ProjectRunRecord, transition TransitionRequest, success bool) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputResult, CreatedAt: time.Now().UTC(), Run: run, ExitCode: transition.ExitCode, Success: success, Output: transition.Output, ResultJSON: transition.ResultJSON, Error: transition.Error}
}

func runAttachErrorResponse(code, message string, terminal bool) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputError, CreatedAt: time.Now().UTC(), Code: code, Error: message, Terminal: terminal}
}

func driverOutputStreamToRun(frameType driverpkg.RuntimeOutputFrameType) domain.StdioStream {
	if frameType == driverpkg.RuntimeOutputStderr {
		return domain.StdioStderr
	}
	return domain.StdioStdout
}

func warningsFromRun(run domain.ProjectRunRecord) []string {
	return append([]string(nil), run.Warnings...)
}
