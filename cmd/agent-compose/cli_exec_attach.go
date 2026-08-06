package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

func runComposeExecPromptOnceCommand(cmd *cobra.Command, projectName string, client execAttachClient, req *agentcomposev2.ExecRequest, options composeExecOptions, jsonOutput bool) error {
	stream := client.AttachExec(cmd.Context())
	if err := stream.Send(&agentcomposev2.AttachExecRequest{Frame: &agentcomposev2.AttachExecRequest_Start{Start: &agentcomposev2.AttachExecStart{
		Request: req, Mode: agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
		Prompt: strings.TrimSpace(options.Prompt), AttachStdin: false, Tty: false,
	}}}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt start: %w", projectName, err))
	}
	if err := stream.CloseRequest(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt close request: %w", projectName, err))
	}
	var result *agentcomposev2.AttachResult
	output := newTerminalStreamOutput(cmd.OutOrStdout(), cmd.ErrOrStderr())
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt: %w", projectName, err))
		}
		switch frame := event.GetFrame().(type) {
		case *agentcomposev2.AttachExecResponse_Output:
			if !jsonOutput {
				if err := writeAttachExecOutput(output, frame.Output); err != nil {
					return err
				}
			}
		case *agentcomposev2.AttachExecResponse_AgentEvent:
			if !jsonOutput && frame.AgentEvent.GetText() != "" {
				if _, err := io.WriteString(cmd.OutOrStdout(), frame.AgentEvent.GetText()); err != nil {
					return err
				}
			}
		case *agentcomposev2.AttachExecResponse_Result:
			result = frame.Result
		case *agentcomposev2.AttachExecResponse_Error:
			return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("exec project %s prompt failed: %s", projectName, firstNonEmptyString(frame.Error.GetMessage(), frame.Error.GetCode(), "attach failed"))}
		}
	}
	if !jsonOutput {
		if err := output.Finish(); err != nil {
			return err
		}
	}
	if result == nil {
		return fmt.Errorf("exec project %s prompt completed without result", projectName)
	}
	if jsonOutput {
		data, err := protojson.MarshalOptions{Indent: "  ", UseProtoNames: true}.Marshal(result)
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if !result.GetSuccess() {
		return commandExitError{Code: attachResultExitCode(result), Err: fmt.Errorf("exec prompt in project %s failed: %s", projectName, firstNonEmptyString(result.GetError(), result.GetOutput(), "prompt failed"))}
	}
	return nil
}

type execAttachClient interface {
	AttachExec(context.Context) execAttachStream
}

type runAttachClient interface {
	AttachAgentRun(context.Context) runAttachStream
}

type runAttachStream interface {
	Send(*agentcomposev2.AttachAgentRunRequest) error
	Receive() (*agentcomposev2.AttachAgentRunResponse, error)
	CloseRequest() error
}

type connectAttachAgentRunClient struct {
	client agentcomposev2connect.RunServiceClient
	probe  func(context.Context) error
}

func (c connectAttachAgentRunClient) AttachAgentRun(ctx context.Context) runAttachStream {
	if c.probe != nil {
		if err := c.probe(ctx); err != nil {
			return failRunAttachStream{err: err}
		}
	}
	return c.client.AttachAgentRun(ctx)
}

// failRunAttachStream is returned when the pre-attach h2c probe fails. Its
// Send surfaces the probe error through the entry function's existing
// commandExitErrorForConnect path, so an HTTP/1-only proxy is reported before
// any bidi stream is opened on the daemon.
type failRunAttachStream struct{ err error }

func (s failRunAttachStream) Send(*agentcomposev2.AttachAgentRunRequest) error {
	return s.err
}
func (s failRunAttachStream) Receive() (*agentcomposev2.AttachAgentRunResponse, error) {
	return nil, s.err
}
func (s failRunAttachStream) CloseRequest() error {
	return s.err
}

func runComposeAttachAgentRunCommand(cmd *cobra.Command, projectName string, client runAttachClient, req *agentcomposev2.RunAgentRequest, options composeRunOptions) (err error) {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	var stdinFD int
	var stdoutFD int
	var restoreTerminal func() error
	var initialSize *agentcomposev2.AttachTerminalSize
	if options.TTY {
		var ok bool
		stdinFD, ok = terminalFileDescriptor(stdin)
		if !ok || !isTerminalFD(stdinFD) {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -t/--tty requires terminal stdin")}
		}
		stdoutFD, ok = terminalFileDescriptor(stdout)
		if !ok || !isTerminalFD(stdoutFD) {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -t/--tty requires terminal stdout")}
		}
		restoreTerminal, err = makeTerminalRaw(stdinFD)
		if err != nil {
			return fmt.Errorf("enable raw terminal mode: %w", err)
		}
		defer func() {
			if restoreErr := restoreTerminal(); err == nil && restoreErr != nil {
				err = fmt.Errorf("restore terminal mode: %w", restoreErr)
			}
		}()
		initialSize = terminalSizeForFD(stdoutFD)
	}
	stream := client.AttachAgentRun(cmd.Context())
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.AttachAgentRunRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request:      req,
			Mode:         agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
			AttachStdin:  true,
			Tty:          options.TTY,
			TerminalSize: initialSize,
		}},
	}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s attach start: %w", projectName, err))
	}
	resizeCtx, stopResize := context.WithCancel(cmd.Context())
	defer stopResize()
	if options.TTY {
		stopResizePump := startAttachAgentRunResizePump(resizeCtx, stdoutFD, send)
		defer stopResizePump()
	}
	stdinErr := make(chan error, 1)
	go func() {
		stdinErr <- pumpAttachAgentRunStdin(stdin, send, closeRequest)
	}()
	var result *agentcomposev2.AttachResult
	output := newTerminalStreamOutput(stdout, stderr)
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("run project %s attach: %w", projectName, err))
		}
		switch frame := event.GetFrame().(type) {
		case *agentcomposev2.AttachAgentRunResponse_Output:
			if err := writeAttachExecOutput(output, frame.Output); err != nil {
				return err
			}
		case *agentcomposev2.AttachAgentRunResponse_Result:
			result = frame.Result
		case *agentcomposev2.AttachAgentRunResponse_Error:
			return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("run project %s attach failed: %s", projectName, firstNonEmptyString(frame.Error.GetMessage(), frame.Error.GetCode(), "attach failed"))}
		}
	}
	stopResize()
	if !options.TTY {
		if err := output.Finish(); err != nil {
			return err
		}
	}
	select {
	case err := <-stdinErr:
		if err != nil {
			return fmt.Errorf("run attach stdin: %w", err)
		}
	default:
	}
	if result == nil {
		return fmt.Errorf("run project %s: attach completed without result", projectName)
	}
	if !result.GetSuccess() {
		runID := ""
		if result.GetRun() != nil {
			runID = result.GetRun().GetRunId()
		}
		return commandExitError{Code: attachResultExitCode(result), Err: fmt.Errorf("run %s in project %s failed: %s", firstNonEmptyString(runID, "attach"), projectName, firstNonEmptyString(result.GetError(), result.GetOutput(), "command failed"))}
	}
	return nil
}

func runComposeRunPromptAttachCommand(cmd *cobra.Command, projectName string, client runAttachClient, req *agentcomposev2.RunAgentRequest) (retErr error) {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	input := newPromptLineReader(stdin, stderr)
	defer func() { retErr = errors.Join(retErr, input.Close()) }()
	attachCtx, cancelAttach := context.WithCancel(cmd.Context())
	defer cancelAttach()
	stream := client.AttachAgentRun(attachCtx)
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.AttachAgentRunRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request:     req,
			Mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
			AttachStdin: true,
			Tty:         false,
		}},
	}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s prompt attach start: %w", projectName, err))
	}
	lastOutputEndedWithNewline := true
	inputPrompt := promptAttachInputPrompt{
		AgentName: req.GetAgentName(),
		SandboxID: firstNonEmptyString(req.GetSandboxId(), req.GetSandboxId()),
	}
	promptForInput := func() error {
		for {
			prompt := ""
			if fd, ok := terminalFileDescriptor(stdout); ok && isTerminalFD(fd) {
				if !lastOutputEndedWithNewline {
					if err := input.StartLine(); err != nil {
						return err
					}
				}
				prompt = inputPrompt.String()
			}
			line, err := input.ReadLine(prompt)
			if errors.Is(err, io.EOF) {
				if err := send(&agentcomposev2.AttachAgentRunRequest{
					Frame: &agentcomposev2.AttachAgentRunRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			if err != nil {
				return err
			}
			text := strings.TrimSpace(line)
			if text == "" {
				continue
			}
			if text == "/exit" {
				if err := send(&agentcomposev2.AttachAgentRunRequest{
					Frame: &agentcomposev2.AttachAgentRunRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			lastOutputEndedWithNewline = true
			return send(&agentcomposev2.AttachAgentRunRequest{
				Frame: &agentcomposev2.AttachAgentRunRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}},
			})
		}
	}
	var result *agentcomposev2.AttachResult
	output := newTerminalStreamOutput(stdout, stderr)
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("run project %s prompt attach: %w", projectName, err))
		}
		switch frame := event.GetFrame().(type) {
		case *agentcomposev2.AttachAgentRunResponse_Started:
			inputPrompt.UpdateFromStarted(frame.Started)
		case *agentcomposev2.AttachAgentRunResponse_Output:
			if err := writeAttachExecOutput(output, frame.Output); err != nil {
				return err
			}
			if data := frame.Output.GetData(); len(data) > 0 {
				lastOutputEndedWithNewline = data[len(data)-1] == '\n'
			}
		case *agentcomposev2.AttachAgentRunResponse_AgentEvent:
			if text := frame.AgentEvent.GetText(); text != "" {
				if _, err := io.WriteString(stdout, text); err != nil {
					return err
				}
				lastOutputEndedWithNewline = strings.HasSuffix(text, "\n")
			}
		case *agentcomposev2.AttachAgentRunResponse_AgentTurnCompleted:
			if err := promptForInput(); err != nil {
				return err
			}
		case *agentcomposev2.AttachAgentRunResponse_Result:
			result = frame.Result
			inputPrompt.UpdateFromRun(result.GetRun())
		case *agentcomposev2.AttachAgentRunResponse_Error:
			return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("run project %s prompt attach failed: %s", projectName, firstNonEmptyString(frame.Error.GetMessage(), frame.Error.GetCode(), "attach failed"))}
		}
	}
	if err := output.Finish(); err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("run project %s: prompt attach completed without result", projectName)
	}
	if !result.GetSuccess() {
		runID := ""
		if result.GetRun() != nil {
			runID = result.GetRun().GetRunId()
		}
		return commandExitError{Code: attachResultExitCode(result), Err: fmt.Errorf("run %s in project %s failed: %s", firstNonEmptyString(runID, "attach"), projectName, firstNonEmptyString(result.GetError(), result.GetOutput(), "prompt failed"))}
	}
	return nil
}

func pumpAttachAgentRunStdin(stdin io.Reader, send func(*agentcomposev2.AttachAgentRunRequest) error, closeRequest func() error) (err error) {
	defer func() {
		err = errors.Join(err, closeRequest())
	}()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := stdin.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			if err := send(&agentcomposev2.AttachAgentRunRequest{
				Frame: &agentcomposev2.AttachAgentRunRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: chunk}},
			}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return send(&agentcomposev2.AttachAgentRunRequest{
				Frame: &agentcomposev2.AttachAgentRunRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
			})
		}
		if readErr != nil {
			return readErr
		}
	}
}

func attachResultExitCode(result *agentcomposev2.AttachResult) int {
	if result == nil || result.GetExitCode() == 0 {
		return exitCodeGeneral
	}
	return int(result.GetExitCode())
}

type execAttachStream interface {
	Send(*agentcomposev2.AttachExecRequest) error
	Receive() (*agentcomposev2.AttachExecResponse, error)
	CloseRequest() error
}

type connectAttachExecClient struct {
	client agentcomposev2connect.ExecServiceClient
	probe  func(context.Context) error
}

func (c connectAttachExecClient) AttachExec(ctx context.Context) execAttachStream {
	if c.probe != nil {
		if err := c.probe(ctx); err != nil {
			return failExecAttachStream{err: err}
		}
	}
	return c.client.AttachExec(ctx)
}

// failExecAttachStream is the exec counterpart of failRunAttachStream: it
// surfaces a failed pre-attach h2c probe through the existing attach error
// mapping before any bidi stream is opened on the daemon.
type failExecAttachStream struct{ err error }

func (s failExecAttachStream) Send(*agentcomposev2.AttachExecRequest) error {
	return s.err
}
func (s failExecAttachStream) Receive() (*agentcomposev2.AttachExecResponse, error) {
	return nil, s.err
}
func (s failExecAttachStream) CloseRequest() error {
	return s.err
}

func runComposeAttachExecCommand(cmd *cobra.Command, projectName string, client execAttachClient, req *agentcomposev2.ExecRequest, options composeExecOptions) (err error) {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	var stdinFD int
	var stdoutFD int
	var restoreTerminal func() error
	var initialSize *agentcomposev2.AttachTerminalSize
	if options.TTY {
		var ok bool
		stdinFD, ok = terminalFileDescriptor(stdin)
		if !ok || !isTerminalFD(stdinFD) {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec -t/--tty requires terminal stdin")}
		}
		stdoutFD, ok = terminalFileDescriptor(stdout)
		if !ok || !isTerminalFD(stdoutFD) {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec -t/--tty requires terminal stdout")}
		}
		restoreTerminal, err = makeTerminalRaw(stdinFD)
		if err != nil {
			return fmt.Errorf("enable raw terminal mode: %w", err)
		}
		defer func() {
			if restoreErr := restoreTerminal(); err == nil && restoreErr != nil {
				err = fmt.Errorf("restore terminal mode: %w", restoreErr)
			}
		}()
		initialSize = terminalSizeForFD(stdoutFD)
	}
	stream := client.AttachExec(cmd.Context())
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.AttachExecRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.AttachExecRequest{
		Frame: &agentcomposev2.AttachExecRequest_Start{Start: &agentcomposev2.AttachExecStart{
			Request:      req,
			AttachStdin:  true,
			Tty:          options.TTY,
			TerminalSize: initialSize,
			Mode:         agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
		}},
	}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s attach start: %w", projectName, err))
	}
	resizeCtx, stopResize := context.WithCancel(cmd.Context())
	defer stopResize()
	if options.TTY {
		stopResizePump := startAttachExecResizePump(resizeCtx, stdoutFD, send)
		defer stopResizePump()
	}
	stdinErr := make(chan error, 1)
	go func() {
		stdinErr <- pumpAttachExecStdin(stdin, send, closeRequest)
	}()
	var result *agentcomposev2.ExecResult
	output := newTerminalStreamOutput(stdout, stderr)
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("exec project %s attach: %w", projectName, err))
		}
		switch frame := event.GetFrame().(type) {
		case *agentcomposev2.AttachExecResponse_Output:
			if err := writeAttachExecOutput(output, frame.Output); err != nil {
				return err
			}
		case *agentcomposev2.AttachExecResponse_Result:
			result = execResultFromAttachResult(frame.Result)
		case *agentcomposev2.AttachExecResponse_Error:
			return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("exec project %s attach failed: %s", projectName, firstNonEmptyString(frame.Error.GetMessage(), frame.Error.GetCode(), "attach failed"))}
		}
	}
	stopResize()
	if !options.TTY {
		if err := output.Finish(); err != nil {
			return err
		}
	}
	select {
	case err := <-stdinErr:
		if err != nil {
			return fmt.Errorf("exec attach stdin: %w", err)
		}
	default:
	}
	if result == nil {
		return fmt.Errorf("exec project %s: attach completed without result", projectName)
	}
	if !result.GetSuccess() {
		return commandExitError{Code: execResultExitCode(result), Err: fmt.Errorf("exec %s in sandbox %s failed: %s", result.GetExecId(), result.GetSandboxId(), firstNonEmptyString(result.GetError(), result.GetStderr(), result.GetOutput(), "command failed"))}
	}
	return nil
}

func runComposeExecPromptAttachCommand(cmd *cobra.Command, projectName string, client execAttachClient, req *agentcomposev2.ExecRequest, options composeExecOptions) (retErr error) {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	attachCtx, cancelAttach := context.WithCancel(cmd.Context())
	defer cancelAttach()
	stream := client.AttachExec(attachCtx)
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.AttachExecRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.AttachExecRequest{
		Frame: &agentcomposev2.AttachExecRequest_Start{Start: &agentcomposev2.AttachExecStart{
			Request:     req,
			Mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
			Prompt:      strings.TrimSpace(options.Prompt),
			AttachStdin: true,
			Tty:         false,
		}},
	}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt attach start: %w", projectName, err))
	}
	input := newPromptLineReader(stdin, stderr)
	defer func() { retErr = errors.Join(retErr, input.Close()) }()
	stdinIsTerminal := input.IsTerminal()
	var inputErr <-chan error
	if !stdinIsTerminal {
		ch := make(chan error, 1)
		inputErr = ch
		go func() { ch <- pumpExecPromptMessages(input, send, closeRequest) }()
		defer func() {
			cancelAttach()
			select {
			case err := <-inputErr:
				if retErr == nil && err != nil {
					retErr = err
				}
			default:
			}
		}()
	}
	lastOutputEndedWithNewline := true
	inputPrompt := promptAttachInputPrompt{
		SandboxID: req.GetSandboxId(),
	}
	promptForInput := func() error {
		for {
			prompt := ""
			if fd, ok := terminalFileDescriptor(stdout); ok && isTerminalFD(fd) {
				if !lastOutputEndedWithNewline {
					if err := input.StartLine(); err != nil {
						return err
					}
				}
				prompt = inputPrompt.String()
			}
			line, err := input.ReadLine(prompt)
			if errors.Is(err, io.EOF) {
				if err := send(&agentcomposev2.AttachExecRequest{
					Frame: &agentcomposev2.AttachExecRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			if err != nil {
				return err
			}
			text := strings.TrimSpace(line)
			if text == "" {
				continue
			}
			if text == "/exit" {
				if err := send(&agentcomposev2.AttachExecRequest{
					Frame: &agentcomposev2.AttachExecRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			lastOutputEndedWithNewline = true
			return send(&agentcomposev2.AttachExecRequest{
				Frame: &agentcomposev2.AttachExecRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}},
			})
		}
	}
	var result *agentcomposev2.AttachResult
	output := newTerminalStreamOutput(stdout, stderr)
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt attach: %w", projectName, err))
		}
		switch frame := event.GetFrame().(type) {
		case *agentcomposev2.AttachExecResponse_Started:
			inputPrompt.UpdateFromStarted(frame.Started)
		case *agentcomposev2.AttachExecResponse_Output:
			if err := writeAttachExecOutput(output, frame.Output); err != nil {
				return err
			}
			if data := frame.Output.GetData(); len(data) > 0 {
				lastOutputEndedWithNewline = data[len(data)-1] == '\n'
			}
		case *agentcomposev2.AttachExecResponse_AgentEvent:
			if text := frame.AgentEvent.GetText(); text != "" {
				if _, err := io.WriteString(stdout, text); err != nil {
					return err
				}
				lastOutputEndedWithNewline = strings.HasSuffix(text, "\n")
			}
		case *agentcomposev2.AttachExecResponse_AgentTurnCompleted:
			if stdinIsTerminal {
				if err := promptForInput(); err != nil {
					return err
				}
			}
		case *agentcomposev2.AttachExecResponse_Result:
			result = frame.Result
			inputPrompt.UpdateFromRun(result.GetRun())
		case *agentcomposev2.AttachExecResponse_Error:
			return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("exec project %s prompt attach failed: %s", projectName, firstNonEmptyString(frame.Error.GetMessage(), frame.Error.GetCode(), "attach failed"))}
		}
	}
	if inputErr != nil {
		select {
		case err := <-inputErr:
			inputErr = nil
			if err != nil {
				return err
			}
		default:
		}
	}
	if err := output.Finish(); err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("exec project %s: prompt attach completed without result", projectName)
	}
	if !result.GetSuccess() {
		runID := ""
		if result.GetRun() != nil {
			runID = result.GetRun().GetRunId()
		}
		return commandExitError{Code: attachResultExitCode(result), Err: fmt.Errorf("run %s in project %s failed: %s", firstNonEmptyString(runID, "attach"), projectName, firstNonEmptyString(result.GetError(), result.GetOutput(), "prompt failed"))}
	}
	return nil
}

func pumpExecPromptMessages(input *promptLineReader, send func(*agentcomposev2.AttachExecRequest) error, closeRequest func() error) (err error) {
	defer func() { err = errors.Join(err, closeRequest()) }()
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
		if err := send(&agentcomposev2.AttachExecRequest{Frame: &agentcomposev2.AttachExecRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}}}); err != nil {
			return err
		}
	}
	return send(&agentcomposev2.AttachExecRequest{Frame: &agentcomposev2.AttachExecRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}}})
}

func pumpAttachExecStdin(stdin io.Reader, send func(*agentcomposev2.AttachExecRequest) error, closeRequest func() error) (err error) {
	defer func() {
		err = errors.Join(err, closeRequest())
	}()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := stdin.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			if err := send(&agentcomposev2.AttachExecRequest{
				Frame: &agentcomposev2.AttachExecRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: chunk}},
			}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return send(&agentcomposev2.AttachExecRequest{
				Frame: &agentcomposev2.AttachExecRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
			})
		}
		if readErr != nil {
			return readErr
		}
	}
}

func writeAttachExecOutput(output *terminalStreamOutput, attachOutput *agentcomposev2.AttachOutput) error {
	if attachOutput == nil {
		return nil
	}
	return output.Write(attachOutput.GetTranscript(), string(attachOutput.GetData()), attachOutput.GetStream())
}

type promptAttachInputPrompt struct {
	AgentName string
	SandboxID string
}

func execResultFromAttachResult(result *agentcomposev2.AttachResult) *agentcomposev2.ExecResult {
	if result == nil {
		return nil
	}
	if result.GetExecResult() != nil {
		return result.GetExecResult()
	}
	return &agentcomposev2.ExecResult{
		ExitCode: result.GetExitCode(),
		Success:  result.GetSuccess(),
		Output:   result.GetOutput(),
		Error:    result.GetError(),
	}
}

func isAttachHTTP2TransportMismatch(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	// A prior-knowledge h2c request answered by an HTTP/1-only peer (e.g. an
	// HTTP/1 reverse proxy) surfaces one of two x/net/http2 errors: the h2c
	// client mis-parses the HTTP/1.1 status line as a frame header ("frame too
	// large ... HTTP/1.1 header"), or the connection is torn down during
	// establishment before the first stream is registered ("client conn could
	// not be established"). Both mean the peer does not speak h2c on this path.
	if strings.Contains(message, "http2: frame too large") && strings.Contains(message, "HTTP/1.1 header") {
		return true
	}
	return strings.Contains(message, "http2: client conn could not be established")
}

func isAttachRPCPath(path string) bool {
	return path == agentcomposev2connect.RunServiceAttachAgentRunProcedure || path == agentcomposev2connect.ExecServiceAttachExecProcedure
}
