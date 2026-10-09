package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type RuntimeOperationKind string

const (
	RuntimeOperationCommand RuntimeOperationKind = "command"
	RuntimeOperationAgent   RuntimeOperationKind = "agent"
)

type RuntimeInputFrameType string

const (
	RuntimeInputStdin        RuntimeInputFrameType = "stdin"
	RuntimeInputStdinEOF     RuntimeInputFrameType = "stdin_eof"
	RuntimeInputResize       RuntimeInputFrameType = "resize"
	RuntimeInputSignal       RuntimeInputFrameType = "signal"
	RuntimeInputHumanMessage RuntimeInputFrameType = "human_message"
	RuntimeInputCancel       RuntimeInputFrameType = "cancel"
)

type RuntimeOutputFrameType string

const (
	RuntimeOutputStarted            RuntimeOutputFrameType = "started"
	RuntimeOutputStdout             RuntimeOutputFrameType = "stdout"
	RuntimeOutputStderr             RuntimeOutputFrameType = "stderr"
	RuntimeOutputAgentEvent         RuntimeOutputFrameType = "agent_event"
	RuntimeOutputAgentTurnCompleted RuntimeOutputFrameType = "agent_turn_completed"
	RuntimeOutputResult             RuntimeOutputFrameType = "result"
	RuntimeOutputError              RuntimeOutputFrameType = "error"
)

type RuntimeSignal string

const (
	RuntimeSignalInterrupt RuntimeSignal = "interrupt"
	RuntimeSignalTerminate RuntimeSignal = "terminate"
	RuntimeSignalKill      RuntimeSignal = "kill"
)

type RuntimeInteractionCapability string

const (
	RuntimeCapabilityNativeExec    RuntimeInteractionCapability = "native_exec"
	RuntimeCapabilityWrapperStream RuntimeInteractionCapability = "wrapper_stream"
	RuntimeCapabilityStdin         RuntimeInteractionCapability = "stdin"
	RuntimeCapabilityStdinEOF      RuntimeInteractionCapability = "stdin_eof"
	RuntimeCapabilityTTY           RuntimeInteractionCapability = "tty"
	RuntimeCapabilityResize        RuntimeInteractionCapability = "resize"
	RuntimeCapabilitySignal        RuntimeInteractionCapability = "signal"
	RuntimeCapabilityArtifacts     RuntimeInteractionCapability = "artifacts"
	RuntimeCapabilityAgentTurns    RuntimeInteractionCapability = "agent_turns"
)

type RuntimeCommandSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

type RuntimeAgentSpec struct {
	Provider  string            `json:"provider,omitempty"`
	Model     string            `json:"model,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type RuntimeStartSpec struct {
	OperationID string               `json:"operation_id,omitempty"`
	Kind        RuntimeOperationKind `json:"kind"`
	Origin      string               `json:"origin,omitempty"`
	Command     *RuntimeCommandSpec  `json:"command,omitempty"`
	Agent       *RuntimeAgentSpec    `json:"agent,omitempty"`
	Cwd         string               `json:"cwd,omitempty"`
	Env         map[string]string    `json:"env,omitempty"`
	AttachStdin bool                 `json:"attach_stdin,omitempty"`
	TTY         bool                 `json:"tty,omitempty"`
	Rows        uint32               `json:"rows,omitempty"`
	Cols        uint32               `json:"cols,omitempty"`
	TimeoutMs   int64                `json:"timeout_ms,omitempty"`
	ArtifactDir string               `json:"artifact_dir,omitempty"`
}

type RuntimeInputFrame struct {
	Type    RuntimeInputFrameType `json:"type"`
	Data    []byte                `json:"data,omitempty"`
	Rows    uint32                `json:"rows,omitempty"`
	Cols    uint32                `json:"cols,omitempty"`
	Signal  RuntimeSignal         `json:"signal,omitempty"`
	Message string                `json:"message,omitempty"`
}

type RuntimeAgentEvent struct {
	Type     string            `json:"type"`
	Message  string            `json:"message,omitempty"`
	Payload  []byte            `json:"payload,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type RuntimeError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type RuntimeOutputFrame struct {
	Type RuntimeOutputFrameType `json:"type"`
	// Data in stdout and stderr frames is valid UTF-8 text. Runtime byte
	// streams must complete or replace invalid rune fragments before emitting.
	Data      []byte             `json:"data,omitempty"`
	Event     *RuntimeAgentEvent `json:"event,omitempty"`
	Result    *RuntimeResult     `json:"result,omitempty"`
	Error     *RuntimeError      `json:"error,omitempty"`
	StartedAt time.Time          `json:"started_at,omitempty"`
}

type RuntimeResult struct {
	OperationID string            `json:"operation_id,omitempty"`
	ExitCode    int               `json:"exit_code"`
	Success     bool              `json:"success"`
	Error       string            `json:"error,omitempty"`
	StartedAt   time.Time         `json:"started_at,omitempty"`
	CompletedAt time.Time         `json:"completed_at,omitempty"`
	Artifacts   map[string]string `json:"artifacts,omitempty"`
	// SecurityFacts carries the lower-layer isolation failures observed during
	// this operation. It is nil when the lower layer reported nothing, so the
	// common case adds no payload.
	SecurityFacts *ExecSecurityFacts `json:"security_facts,omitempty"`
}

// RuntimeInteractionCapabilities describes what one live runtime instance can
// do for a single operation right now: stream over a wrapper, accept stdin,
// resize a TTY, deliver a signal. It is deliberately not part of the
// capmatrix isolation matrix, which answers a different question ("what does
// the engine enforce, by what mechanism"). This surface is instance-local and
// operation-local, so folding it into the static driver declarations would
// claim facts the engine never measured or declared. The two stay separate;
// callers that need an isolation decision use capmatrix, callers that need to
// know whether an operation is supported use this.
type RuntimeInteractionCapabilities struct {
	NativeExec    bool `json:"native_exec"`
	WrapperStream bool `json:"wrapper_stream"`
	Stdin         bool `json:"stdin"`
	StdinEOF      bool `json:"stdin_eof"`
	TTY           bool `json:"tty"`
	Resize        bool `json:"resize"`
	Signal        bool `json:"signal"`
	Artifacts     bool `json:"artifacts"`
	AgentTurns    bool `json:"agent_turns"`
}

type RuntimeInteraction interface {
	Send(RuntimeInputFrame) error
	CloseSend() error
	Recv() (RuntimeOutputFrame, error)
	Wait() (RuntimeResult, error)
}

// GuardRuntimeInteractionInput serializes Send and CloseSend so an input pump
// can finish concurrently with the output loop without closing stdin twice or
// sending after it has been closed.
func GuardRuntimeInteractionInput(interaction RuntimeInteraction) RuntimeInteraction {
	if interaction == nil {
		return nil
	}
	return &guardedRuntimeInteraction{RuntimeInteraction: interaction}
}

type guardedRuntimeInteraction struct {
	RuntimeInteraction
	inputMu     sync.Mutex
	inputClosed bool
}

func (i *guardedRuntimeInteraction) Send(frame RuntimeInputFrame) error {
	i.inputMu.Lock()
	defer i.inputMu.Unlock()
	if i.inputClosed {
		return io.ErrClosedPipe
	}
	return i.RuntimeInteraction.Send(frame)
}

func (i *guardedRuntimeInteraction) CloseSend() error {
	i.inputMu.Lock()
	defer i.inputMu.Unlock()
	if i.inputClosed {
		return nil
	}
	i.inputClosed = true
	return i.RuntimeInteraction.CloseSend()
}

type RuntimeInteractor interface {
	InteractionCapabilities() RuntimeInteractionCapabilities
	OpenInteraction(context.Context, *Sandbox, VMState, RuntimeStartSpec) (RuntimeInteraction, error)
}

var ErrRuntimeInteractionUnsupported = errors.New("runtime interaction unsupported")

type RuntimeInteractionUnsupportedError struct {
	Driver     string
	Operation  RuntimeOperationKind
	Capability RuntimeInteractionCapability
	Reason     string
}

func (e *RuntimeInteractionUnsupportedError) Error() string {
	driver := strings.TrimSpace(e.Driver)
	if driver == "" {
		driver = "runtime"
	}
	capability := strings.TrimSpace(string(e.Capability))
	if capability == "" {
		capability = "interaction"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "capability is not available"
	}
	return fmt.Sprintf("%s %s %s unsupported: %s", driver, e.Operation, capability, reason)
}

func (e *RuntimeInteractionUnsupportedError) Unwrap() error {
	return ErrRuntimeInteractionUnsupported
}

func NewRuntimeInteractionUnsupportedError(driver string, spec RuntimeStartSpec, capability RuntimeInteractionCapability, reason string) error {
	return &RuntimeInteractionUnsupportedError{
		Driver:     resolveRuntimeDriver(driver),
		Operation:  normalizeRuntimeOperationKind(spec.Kind),
		Capability: capability,
		Reason:     reason,
	}
}

func (c RuntimeInteractionCapabilities) ValidateStartSpec(driver string, spec RuntimeStartSpec) error {
	kind := normalizeRuntimeOperationKind(spec.Kind)
	switch kind {
	case RuntimeOperationCommand:
		if !c.NativeExec && !c.WrapperStream {
			return NewRuntimeInteractionUnsupportedError(driver, spec, RuntimeCapabilityNativeExec, "command interactions require native exec or wrapper stream support")
		}
	case RuntimeOperationAgent:
		if !c.AgentTurns {
			return NewRuntimeInteractionUnsupportedError(driver, spec, RuntimeCapabilityAgentTurns, "agent interactions require turn support")
		}
	default:
		return NewRuntimeInteractionUnsupportedError(driver, spec, "", "unknown runtime operation kind")
	}
	if spec.AttachStdin && !c.Stdin {
		return NewRuntimeInteractionUnsupportedError(driver, spec, RuntimeCapabilityStdin, "stdin attachment is not available")
	}
	if spec.AttachStdin && !c.StdinEOF {
		return NewRuntimeInteractionUnsupportedError(driver, spec, RuntimeCapabilityStdinEOF, "stdin EOF is not available")
	}
	if spec.TTY && !c.TTY {
		return NewRuntimeInteractionUnsupportedError(driver, spec, RuntimeCapabilityTTY, "TTY allocation is not available")
	}
	if (spec.Rows != 0 || spec.Cols != 0) && !c.Resize {
		return NewRuntimeInteractionUnsupportedError(driver, spec, RuntimeCapabilityResize, "terminal resize is not available")
	}
	if strings.TrimSpace(spec.ArtifactDir) != "" && !c.Artifacts {
		return NewRuntimeInteractionUnsupportedError(driver, spec, RuntimeCapabilityArtifacts, "artifact projection is not available")
	}
	return nil
}

func UnsupportedRuntimeInteraction(driver string, caps RuntimeInteractionCapabilities, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	if err := caps.ValidateStartSpec(driver, spec); err != nil {
		return nil, err
	}
	return nil, NewRuntimeInteractionUnsupportedError(driver, spec, "", "OpenInteraction is not implemented")
}

func ExecSpecFromRuntimeStartSpec(spec RuntimeStartSpec) ExecSpec {
	command := RuntimeCommandSpec{}
	if spec.Command != nil {
		command = *spec.Command
	}
	env := command.Env
	if len(spec.Env) > 0 {
		env = mergeStringMaps(spec.Env, command.Env)
	}
	return ExecSpec{
		Command: command.Command,
		Args:    command.Args,
		Env:     env,
		Cwd:     firstNonEmpty(command.Cwd, spec.Cwd),
	}
}

// ExecStreamInteractionRequest describes the sandbox and command to stream-exec.
type ExecStreamInteractionRequest struct {
	Session *Sandbox
	VMState VMState
	Spec    RuntimeStartSpec
}

func NewExecStreamInteraction(ctx context.Context, runtime SandboxRuntime, request ExecStreamInteractionRequest) RuntimeInteraction {
	childCtx, cancel := context.WithCancel(ctx)
	interaction := &execStreamInteraction{
		cancel: cancel,
		done:   make(chan struct{}),
		output: make(chan RuntimeOutputFrame, 16),
	}
	go interaction.run(childCtx, runtime, request.Session, request.VMState, request.Spec)
	return interaction
}

type execStreamInteraction struct {
	cancel context.CancelFunc
	done   chan struct{}
	output chan RuntimeOutputFrame
	result RuntimeResult
	err    error
}

func (i *execStreamInteraction) Send(frame RuntimeInputFrame) error {
	switch frame.Type {
	case RuntimeInputCancel:
		i.cancel()
		return nil
	default:
		return ErrRuntimeInteractionUnsupported
	}
}

func (i *execStreamInteraction) CloseSend() error {
	i.cancel()
	return nil
}

func (i *execStreamInteraction) Recv() (RuntimeOutputFrame, error) {
	frame, ok := <-i.output
	if !ok {
		return RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (i *execStreamInteraction) Wait() (RuntimeResult, error) {
	<-i.done
	return i.result, i.err
}

func (i *execStreamInteraction) run(ctx context.Context, runtime SandboxRuntime, session *Sandbox, vmState VMState, spec RuntimeStartSpec) {
	defer close(i.done)
	defer close(i.output)

	startedAt := time.Now()
	i.emit(ctx, RuntimeOutputFrame{Type: RuntimeOutputStarted, StartedAt: startedAt})
	result, err := runtime.ExecStream(ctx, session, vmState, ExecSpecFromRuntimeStartSpec(spec), func(chunk ExecChunk) {
		frameType := RuntimeOutputStdout
		if NormalizeStdioStream(chunk.Stream) == StdioStderr {
			frameType = RuntimeOutputStderr
		}
		i.emit(ctx, RuntimeOutputFrame{Type: frameType, Data: []byte(chunk.Text)})
	})

	completedAt := time.Now()
	i.result = RuntimeResult{
		OperationID:   spec.OperationID,
		ExitCode:      result.ExitCode,
		Success:       result.Success,
		StartedAt:     startedAt,
		CompletedAt:   completedAt,
		SecurityFacts: result.SecurityFacts.Pointer(),
	}
	if err != nil {
		i.err = err
		i.result.Error = err.Error()
		i.emit(ctx, RuntimeOutputFrame{Type: RuntimeOutputError, Error: &RuntimeError{Message: err.Error()}})
		return
	}
	i.emit(ctx, RuntimeOutputFrame{Type: RuntimeOutputResult, Result: &i.result})
}

func (i *execStreamInteraction) emit(ctx context.Context, frame RuntimeOutputFrame) {
	select {
	case i.output <- frame:
	case <-ctx.Done():
	}
}

func normalizeRuntimeOperationKind(kind RuntimeOperationKind) RuntimeOperationKind {
	if kind == "" {
		return RuntimeOperationCommand
	}
	return kind
}

func mergeStringMaps(first map[string]string, second map[string]string) map[string]string {
	if len(first) == 0 && len(second) == 0 {
		return nil
	}
	merged := make(map[string]string, len(first)+len(second))
	for key, value := range first {
		merged[key] = value
	}
	for key, value := range second {
		merged[key] = value
	}
	return merged
}
