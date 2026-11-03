package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// attachStream is the shared shape of both attach RPCs. The Run and Exec
// services return different stream types, but the methods the command plumbing
// needs are identical.
type attachStream[Req, Resp any] interface {
	Send(Req) error
	Receive() (Resp, error)
	CloseRequest() error
}

// attachStart is the transport-neutral start frame. The two RPCs spell some of
// these fields differently, and only Exec has a prompt field.
type attachStart struct {
	mode         agentcomposev2.AttachRunMode
	attachStdin  bool
	tty          bool
	terminalSize *agentcomposev2.AttachTerminalSize
	prompt       string
}

type attachFrameKind uint8

const (
	attachFrameIgnored attachFrameKind = iota
	attachFrameStarted
	attachFrameOutput
	attachFrameAgentEvent
	attachFrameTurnCompleted
	attachFrameResult
	attachFrameError
)

// attachFrame projects the two response oneofs onto the payload messages they
// share, so a single receive loop can process both RPCs.
type attachFrame struct {
	kind       attachFrameKind
	started    *agentcomposev2.AttachStarted
	output     *agentcomposev2.AttachOutput
	agentEvent *agentcomposev2.AttachAgentEvent
	result     *agentcomposev2.AttachResult
	err        *agentcomposev2.AttachError
}

// attachWire builds request frames and decodes response frames for one attach
// RPC.
type attachWire[Req, Resp any] interface {
	StartFrame(attachStart) Req
	StdinFrame([]byte) Req
	StdinEOFFrame() Req
	HumanMessageFrame(string) Req
	ResizeFrame(*agentcomposev2.AttachTerminalSize) Req
	Frame(Resp) attachFrame
}

func newAgentRunAttachWire(request *agentcomposev2.RunAgentRequest) attachWire[*agentcomposev2.AttachAgentRunRequest, *agentcomposev2.AttachAgentRunResponse] {
	return agentRunAttachWire{request: request}
}

type agentRunAttachWire struct {
	request *agentcomposev2.RunAgentRequest
}

func (w agentRunAttachWire) StartFrame(start attachStart) *agentcomposev2.AttachAgentRunRequest {
	return &agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request:      w.request,
			Mode:         start.mode,
			AttachStdin:  start.attachStdin,
			Tty:          start.tty,
			TerminalSize: start.terminalSize,
		}},
	}
}

func (w agentRunAttachWire) StdinFrame(data []byte) *agentcomposev2.AttachAgentRunRequest {
	return &agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: data}},
	}
}

func (w agentRunAttachWire) StdinEOFFrame() *agentcomposev2.AttachAgentRunRequest {
	return &agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
	}
}

func (w agentRunAttachWire) HumanMessageFrame(text string) *agentcomposev2.AttachAgentRunRequest {
	return &agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}},
	}
}

func (w agentRunAttachWire) ResizeFrame(size *agentcomposev2.AttachTerminalSize) *agentcomposev2.AttachAgentRunRequest {
	return &agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Resize{Resize: &agentcomposev2.AttachResize{TerminalSize: size}},
	}
}

func (w agentRunAttachWire) Frame(response *agentcomposev2.AttachAgentRunResponse) attachFrame {
	switch frame := response.GetFrame().(type) {
	case *agentcomposev2.AttachAgentRunResponse_Started:
		return attachFrame{kind: attachFrameStarted, started: frame.Started}
	case *agentcomposev2.AttachAgentRunResponse_Output:
		return attachFrame{kind: attachFrameOutput, output: frame.Output}
	case *agentcomposev2.AttachAgentRunResponse_AgentEvent:
		return attachFrame{kind: attachFrameAgentEvent, agentEvent: frame.AgentEvent}
	case *agentcomposev2.AttachAgentRunResponse_AgentTurnCompleted:
		return attachFrame{kind: attachFrameTurnCompleted}
	case *agentcomposev2.AttachAgentRunResponse_Result:
		return attachFrame{kind: attachFrameResult, result: frame.Result}
	case *agentcomposev2.AttachAgentRunResponse_Error:
		return attachFrame{kind: attachFrameError, err: frame.Error}
	default:
		return attachFrame{}
	}
}

func newExecAttachWire(request *agentcomposev2.ExecRequest) attachWire[*agentcomposev2.AttachExecRequest, *agentcomposev2.AttachExecResponse] {
	return execAttachWire{request: request}
}

type execAttachWire struct {
	request *agentcomposev2.ExecRequest
}

func (w execAttachWire) StartFrame(start attachStart) *agentcomposev2.AttachExecRequest {
	return &agentcomposev2.AttachExecRequest{
		Frame: &agentcomposev2.AttachExecRequest_Start{Start: &agentcomposev2.AttachExecStart{
			Request:      w.request,
			AttachStdin:  start.attachStdin,
			Tty:          start.tty,
			TerminalSize: start.terminalSize,
			Mode:         start.mode,
			Prompt:       start.prompt,
		}},
	}
}

func (w execAttachWire) StdinFrame(data []byte) *agentcomposev2.AttachExecRequest {
	return &agentcomposev2.AttachExecRequest{
		Frame: &agentcomposev2.AttachExecRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: data}},
	}
}

func (w execAttachWire) StdinEOFFrame() *agentcomposev2.AttachExecRequest {
	return &agentcomposev2.AttachExecRequest{
		Frame: &agentcomposev2.AttachExecRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
	}
}

func (w execAttachWire) HumanMessageFrame(text string) *agentcomposev2.AttachExecRequest {
	return &agentcomposev2.AttachExecRequest{
		Frame: &agentcomposev2.AttachExecRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}},
	}
}

func (w execAttachWire) ResizeFrame(size *agentcomposev2.AttachTerminalSize) *agentcomposev2.AttachExecRequest {
	return &agentcomposev2.AttachExecRequest{
		Frame: &agentcomposev2.AttachExecRequest_Resize{Resize: &agentcomposev2.AttachResize{TerminalSize: size}},
	}
}

func (w execAttachWire) Frame(response *agentcomposev2.AttachExecResponse) attachFrame {
	switch frame := response.GetFrame().(type) {
	case *agentcomposev2.AttachExecResponse_Started:
		return attachFrame{kind: attachFrameStarted, started: frame.Started}
	case *agentcomposev2.AttachExecResponse_Output:
		return attachFrame{kind: attachFrameOutput, output: frame.Output}
	case *agentcomposev2.AttachExecResponse_AgentEvent:
		return attachFrame{kind: attachFrameAgentEvent, agentEvent: frame.AgentEvent}
	case *agentcomposev2.AttachExecResponse_AgentTurnCompleted:
		return attachFrame{kind: attachFrameTurnCompleted}
	case *agentcomposev2.AttachExecResponse_Result:
		return attachFrame{kind: attachFrameResult, result: frame.Result}
	case *agentcomposev2.AttachExecResponse_Error:
		return attachFrame{kind: attachFrameError, err: frame.Error}
	default:
		return attachFrame{}
	}
}

// attachSender serialises Send and CloseRequest: the stdin pump, the resize
// pump and the prompt reader can all write to the same stream.
type attachSender[Req any] struct {
	mu           sync.Mutex
	send         func(Req) error
	closeRequest func() error
}

func newAttachSender[Req any](send func(Req) error, closeRequest func() error) *attachSender[Req] {
	return &attachSender[Req]{send: send, closeRequest: closeRequest}
}

func (s *attachSender[Req]) Send(frame Req) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.send(frame)
}

func (s *attachSender[Req]) CloseRequest() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeRequest()
}

// attachTTY is the terminal state prepared for an interactive attach. The
// caller defers restoreAfter, which folds a restore failure into its named
// return.
type attachTTY struct {
	stdinFD     int
	stdoutFD    int
	restore     func() error
	initialSize *agentcomposev2.AttachTerminalSize
}

func prepareAttachTTY(cmd *cobra.Command, command string) (attachTTY, error) {
	stdinFD, ok := terminalFileDescriptor(cmd.InOrStdin())
	if !ok || !isTerminalFD(stdinFD) {
		return attachTTY{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s -t/--tty requires terminal stdin", command)}
	}
	stdoutFD, ok := terminalFileDescriptor(cmd.OutOrStdout())
	if !ok || !isTerminalFD(stdoutFD) {
		return attachTTY{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s -t/--tty requires terminal stdout", command)}
	}
	restore, err := makeTerminalRaw(stdinFD)
	if err != nil {
		return attachTTY{}, fmt.Errorf("enable raw terminal mode: %w", err)
	}
	return attachTTY{
		stdinFD:     stdinFD,
		stdoutFD:    stdoutFD,
		restore:     restore,
		initialSize: terminalSizeForFD(stdoutFD),
	}, nil
}

func (t attachTTY) restoreAfter(err *error) {
	if t.restore == nil {
		return
	}
	if restoreErr := t.restore(); *err == nil && restoreErr != nil {
		*err = fmt.Errorf("restore terminal mode: %w", restoreErr)
	}
}

func pumpAttachStdin[Req, Resp any](stdin io.Reader, sender *attachSender[Req], wire attachWire[Req, Resp]) (err error) {
	defer func() { err = errors.Join(err, sender.CloseRequest()) }()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := stdin.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			if err := sender.Send(wire.StdinFrame(chunk)); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return sender.Send(wire.StdinEOFFrame())
		}
		if readErr != nil {
			return readErr
		}
	}
}

// pumpPromptMessages feeds non-terminal stdin to a prompt attach one line at a
// time.
func pumpPromptMessages[Req, Resp any](input *promptLineReader, sender *attachSender[Req], wire attachWire[Req, Resp]) (err error) {
	defer func() { err = errors.Join(err, sender.CloseRequest()) }()
	for {
		line, readErr := input.ReadLine("")
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if text == "/exit" {
			break
		}
		if err := sender.Send(wire.HumanMessageFrame(text)); err != nil {
			return err
		}
	}
	return sender.Send(wire.StdinEOFFrame())
}

type attachPromptSender[Req, Resp any] struct {
	sender *attachSender[Req]
	wire   attachWire[Req, Resp]
}

func (s attachPromptSender[Req, Resp]) closeAfterEOF() error {
	if err := s.sender.Send(s.wire.StdinEOFFrame()); err != nil {
		return err
	}
	return s.sender.CloseRequest()
}

func (s attachPromptSender[Req, Resp]) humanMessage(text string) error {
	return s.sender.Send(s.wire.HumanMessageFrame(text))
}

func promptForAttachInput[Req, Resp any](
	input *promptLineReader,
	stdout io.Writer,
	inputPrompt *promptAttachInputPrompt,
	lastOutputEndedWithNewline *bool,
	sender attachPromptSender[Req, Resp],
) error {
	for {
		prompt := ""
		if fd, ok := terminalFileDescriptor(stdout); ok && isTerminalFD(fd) {
			if !*lastOutputEndedWithNewline {
				if err := input.StartLine(); err != nil {
					return err
				}
			}
			prompt = inputPrompt.String()
		}
		line, err := input.ReadLine(prompt)
		if errors.Is(err, io.EOF) {
			return sender.closeAfterEOF()
		}
		if err != nil {
			return err
		}
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if text == "/exit" {
			return sender.closeAfterEOF()
		}
		*lastOutputEndedWithNewline = true
		return sender.humanMessage(text)
	}
}

// receiveAttachFrames reads until the server closes the stream. It owns the
// common result/error frame handling and delegates the remaining frames to
// handle.
func receiveAttachFrames[Req, Resp any](
	stream attachStream[Req, Resp],
	wire attachWire[Req, Resp],
	label string,
	handle func(attachFrame) error,
) (*agentcomposev2.AttachResult, error) {
	var result *agentcomposev2.AttachResult
	for {
		response, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, commandExitErrorForConnect(fmt.Errorf("%s: %w", label, err))
		}
		frame := wire.Frame(response)
		switch frame.kind {
		case attachFrameError:
			return nil, commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("%s failed: %s", label, firstNonEmptyString(frame.err.GetMessage(), frame.err.GetCode(), "attach failed"))}
		case attachFrameResult:
			result = frame.result
		}
		if handle != nil {
			if err := handle(frame); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// promptAttachSession renders the prompt-mode frames shared by the Run and Exec
// attach commands. onTurnCompleted is nil when the command must not read input.
type promptAttachSession struct {
	output                     *terminalStreamOutput
	stdout                     io.Writer
	inputPrompt                *promptAttachInputPrompt
	lastOutputEndedWithNewline bool
	onTurnCompleted            func() error
}

func (s *promptAttachSession) handle(frame attachFrame) error {
	switch frame.kind {
	case attachFrameStarted:
		s.inputPrompt.UpdateFromStarted(frame.started)
	case attachFrameOutput:
		if err := writeAttachExecOutput(s.output, frame.output); err != nil {
			return err
		}
		if data := frame.output.GetData(); len(data) > 0 {
			s.lastOutputEndedWithNewline = data[len(data)-1] == '\n'
		}
	case attachFrameAgentEvent:
		if text := frame.agentEvent.GetText(); text != "" {
			if _, err := io.WriteString(s.stdout, text); err != nil {
				return err
			}
			s.lastOutputEndedWithNewline = strings.HasSuffix(text, "\n")
		}
	case attachFrameTurnCompleted:
		if s.onTurnCompleted != nil {
			return s.onTurnCompleted()
		}
	case attachFrameResult:
		s.inputPrompt.UpdateFromRun(frame.result.GetRun())
	}
	return nil
}

type interactiveAttachSpec struct {
	command    string
	label      string
	stdinLabel string
	noResult   string
	tty        bool
	finish     func(*agentcomposev2.AttachResult) error
}

// runInteractiveAttach drives the command-mode attach shared by Run and Exec:
// optional raw TTY, start frame, resize pump, stdin pump, output frames, then
// the command-specific result projection.
func runInteractiveAttach[Req, Resp any](
	cmd *cobra.Command,
	open func(context.Context) attachStream[Req, Resp],
	wire attachWire[Req, Resp],
	spec interactiveAttachSpec,
) (err error) {
	stdin, stdout, stderr := cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	var tty attachTTY
	if spec.tty {
		tty, err = prepareAttachTTY(cmd, spec.command)
		if err != nil {
			return err
		}
		defer tty.restoreAfter(&err)
	}
	stream := open(cmd.Context())
	sender := newAttachSender(stream.Send, stream.CloseRequest)
	if err := sender.Send(wire.StartFrame(attachStart{
		mode:         agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
		attachStdin:  true,
		tty:          spec.tty,
		terminalSize: tty.initialSize,
	})); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("%s start: %w", spec.label, err))
	}
	resizeCtx, stopResize := context.WithCancel(cmd.Context())
	defer stopResize()
	if spec.tty {
		stopResizePump := startAttachResizePump(resizeCtx, tty.stdoutFD, func(size *agentcomposev2.AttachTerminalSize) error {
			return sender.Send(wire.ResizeFrame(size))
		})
		defer stopResizePump()
	}
	stdinErr := make(chan error, 1)
	go func() {
		stdinErr <- pumpAttachStdin(stdin, sender, wire)
	}()
	output := newTerminalStreamOutput(stdout, stderr)
	result, err := receiveAttachFrames(stream, wire, spec.label, func(frame attachFrame) error {
		if frame.kind == attachFrameOutput {
			return writeAttachExecOutput(output, frame.output)
		}
		return nil
	})
	if err != nil {
		return err
	}
	stopResize()
	if !spec.tty {
		if err := output.Finish(); err != nil {
			return err
		}
	}
	select {
	case err := <-stdinErr:
		if err != nil {
			return fmt.Errorf("%s: %w", spec.stdinLabel, err)
		}
	default:
	}
	if result == nil {
		return errors.New(spec.noResult)
	}
	return spec.finish(result)
}
