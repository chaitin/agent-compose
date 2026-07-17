package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"bufio"
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
	stream := client.ExecAttach(cmd.Context())
	if err := stream.Send(&agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_Start{Start: &agentcomposev2.ExecAttachStart{
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
		case *agentcomposev2.ExecAttachResponse_Output:
			if !jsonOutput {
				if err := writeExecAttachOutput(output, frame.Output); err != nil {
					return err
				}
			}
		case *agentcomposev2.ExecAttachResponse_AgentEvent:
			if !jsonOutput && frame.AgentEvent.GetText() != "" {
				if _, err := io.WriteString(cmd.OutOrStdout(), frame.AgentEvent.GetText()); err != nil {
					return err
				}
			}
		case *agentcomposev2.ExecAttachResponse_Result:
			result = frame.Result
		case *agentcomposev2.ExecAttachResponse_Error:
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
	ExecAttach(context.Context) execAttachStream
}

type runAttachClient interface {
	RunAttach(context.Context) runAttachStream
}

type runAttachStream interface {
	Send(*agentcomposev2.RunAttachRequest) error
	Receive() (*agentcomposev2.RunAttachResponse, error)
	CloseRequest() error
}

type connectRunAttachClient struct {
	client agentcomposev2connect.RunServiceClient
}

func (c connectRunAttachClient) RunAttach(ctx context.Context) runAttachStream {
	return c.client.RunAttach(ctx)
}

func runComposeRunAttachCommand(cmd *cobra.Command, projectName string, client runAttachClient, req *agentcomposev2.RunAgentRequest, options composeRunOptions) (err error) {
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
	stream := client.RunAttach(cmd.Context())
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.RunAttachRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.RunAttachRequest{
		Frame: &agentcomposev2.RunAttachRequest_Start{Start: &agentcomposev2.RunAttachStart{
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
		stopResizePump := startRunAttachResizePump(resizeCtx, stdoutFD, send)
		defer stopResizePump()
	}
	stdinErr := make(chan error, 1)
	go func() {
		stdinErr <- pumpRunAttachStdin(stdin, send, closeRequest)
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
		case *agentcomposev2.RunAttachResponse_Output:
			if err := writeExecAttachOutput(output, frame.Output); err != nil {
				return err
			}
		case *agentcomposev2.RunAttachResponse_Result:
			result = frame.Result
		case *agentcomposev2.RunAttachResponse_Error:
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

func runComposeRunPromptAttachCommand(cmd *cobra.Command, projectName string, client runAttachClient, req *agentcomposev2.RunAgentRequest) error {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	stream := client.RunAttach(cmd.Context())
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.RunAttachRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.RunAttachRequest{
		Frame: &agentcomposev2.RunAttachRequest_Start{Start: &agentcomposev2.RunAttachStart{
			Request:     req,
			Mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
			AttachStdin: true,
			Tty:         false,
		}},
	}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s prompt attach start: %w", projectName, err))
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lastOutputEndedWithNewline := true
	inputPrompt := promptAttachInputPrompt{
		AgentName: req.GetAgentName(),
		SandboxID: firstNonEmptyString(req.GetSandboxId(), req.GetSandboxId()),
	}
	promptForInput := func() error {
		for {
			if fd, ok := terminalFileDescriptor(stdout); ok && isTerminalFD(fd) {
				if err := writePromptAttachInputPrompt(stderr, inputPrompt, !lastOutputEndedWithNewline); err != nil {
					return err
				}
			}
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return err
				}
				if err := send(&agentcomposev2.RunAttachRequest{
					Frame: &agentcomposev2.RunAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			text := strings.TrimSpace(scanner.Text())
			if text == "" {
				continue
			}
			if text == "/exit" {
				if err := send(&agentcomposev2.RunAttachRequest{
					Frame: &agentcomposev2.RunAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			lastOutputEndedWithNewline = true
			return send(&agentcomposev2.RunAttachRequest{
				Frame: &agentcomposev2.RunAttachRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}},
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
		case *agentcomposev2.RunAttachResponse_Started:
			inputPrompt.UpdateFromStarted(frame.Started)
		case *agentcomposev2.RunAttachResponse_Output:
			if err := writeExecAttachOutput(output, frame.Output); err != nil {
				return err
			}
			if data := frame.Output.GetData(); len(data) > 0 {
				lastOutputEndedWithNewline = data[len(data)-1] == '\n'
			}
		case *agentcomposev2.RunAttachResponse_AgentEvent:
			if text := frame.AgentEvent.GetText(); text != "" {
				if _, err := io.WriteString(stdout, text); err != nil {
					return err
				}
				lastOutputEndedWithNewline = strings.HasSuffix(text, "\n")
			}
		case *agentcomposev2.RunAttachResponse_AgentTurnCompleted:
			if err := promptForInput(); err != nil {
				return err
			}
		case *agentcomposev2.RunAttachResponse_Result:
			result = frame.Result
			inputPrompt.UpdateFromRun(result.GetRun())
		case *agentcomposev2.RunAttachResponse_Error:
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

func pumpRunAttachStdin(stdin io.Reader, send func(*agentcomposev2.RunAttachRequest) error, closeRequest func() error) (err error) {
	defer func() {
		err = errors.Join(err, closeRequest())
	}()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := stdin.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			if err := send(&agentcomposev2.RunAttachRequest{
				Frame: &agentcomposev2.RunAttachRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: chunk}},
			}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return send(&agentcomposev2.RunAttachRequest{
				Frame: &agentcomposev2.RunAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
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
	Send(*agentcomposev2.ExecAttachRequest) error
	Receive() (*agentcomposev2.ExecAttachResponse, error)
	CloseRequest() error
}

type connectExecAttachClient struct {
	client agentcomposev2connect.ExecServiceClient
}

func (c connectExecAttachClient) ExecAttach(ctx context.Context) execAttachStream {
	return c.client.ExecAttach(ctx)
}

func runComposeExecAttachCommand(cmd *cobra.Command, projectName string, client execAttachClient, req *agentcomposev2.ExecRequest, options composeExecOptions) (err error) {
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
	stream := client.ExecAttach(cmd.Context())
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.ExecAttachRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.ExecAttachRequest{
		Frame: &agentcomposev2.ExecAttachRequest_Start{Start: &agentcomposev2.ExecAttachStart{
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
		stopResizePump := startExecAttachResizePump(resizeCtx, stdoutFD, send)
		defer stopResizePump()
	}
	stdinErr := make(chan error, 1)
	go func() {
		stdinErr <- pumpExecAttachStdin(stdin, send, closeRequest)
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
		case *agentcomposev2.ExecAttachResponse_Output:
			if err := writeExecAttachOutput(output, frame.Output); err != nil {
				return err
			}
		case *agentcomposev2.ExecAttachResponse_Result:
			result = execResultFromAttachResult(frame.Result)
		case *agentcomposev2.ExecAttachResponse_Error:
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
	stream := client.ExecAttach(attachCtx)
	var sendMu sync.Mutex
	send := func(frame *agentcomposev2.ExecAttachRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	closeRequest := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.CloseRequest()
	}
	if err := send(&agentcomposev2.ExecAttachRequest{
		Frame: &agentcomposev2.ExecAttachRequest_Start{Start: &agentcomposev2.ExecAttachStart{
			Request:     req,
			Mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
			Prompt:      strings.TrimSpace(options.Prompt),
			AttachStdin: true,
			Tty:         false,
		}},
	}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt attach start: %w", projectName, err))
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	stdinIsTerminal := false
	if fd, ok := terminalFileDescriptor(stdin); ok {
		stdinIsTerminal = isTerminalFD(fd)
	}
	var inputErr <-chan error
	if !stdinIsTerminal {
		ch := make(chan error, 1)
		inputErr = ch
		go func() { ch <- pumpExecPromptMessages(scanner, send, closeRequest) }()
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
			if fd, ok := terminalFileDescriptor(stdout); ok && isTerminalFD(fd) {
				if err := writePromptAttachInputPrompt(stderr, inputPrompt, !lastOutputEndedWithNewline); err != nil {
					return err
				}
			}
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return err
				}
				if err := send(&agentcomposev2.ExecAttachRequest{
					Frame: &agentcomposev2.ExecAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			text := strings.TrimSpace(scanner.Text())
			if text == "" {
				continue
			}
			if text == "/exit" {
				if err := send(&agentcomposev2.ExecAttachRequest{
					Frame: &agentcomposev2.ExecAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
				}); err != nil {
					return err
				}
				return closeRequest()
			}
			lastOutputEndedWithNewline = true
			return send(&agentcomposev2.ExecAttachRequest{
				Frame: &agentcomposev2.ExecAttachRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}},
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
		case *agentcomposev2.ExecAttachResponse_Started:
			inputPrompt.UpdateFromStarted(frame.Started)
		case *agentcomposev2.ExecAttachResponse_Output:
			if err := writeExecAttachOutput(output, frame.Output); err != nil {
				return err
			}
			if data := frame.Output.GetData(); len(data) > 0 {
				lastOutputEndedWithNewline = data[len(data)-1] == '\n'
			}
		case *agentcomposev2.ExecAttachResponse_AgentEvent:
			if text := frame.AgentEvent.GetText(); text != "" {
				if _, err := io.WriteString(stdout, text); err != nil {
					return err
				}
				lastOutputEndedWithNewline = strings.HasSuffix(text, "\n")
			}
		case *agentcomposev2.ExecAttachResponse_AgentTurnCompleted:
			if stdinIsTerminal {
				if err := promptForInput(); err != nil {
					return err
				}
			}
		case *agentcomposev2.ExecAttachResponse_Result:
			result = frame.Result
			inputPrompt.UpdateFromRun(result.GetRun())
		case *agentcomposev2.ExecAttachResponse_Error:
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

func pumpExecPromptMessages(scanner *bufio.Scanner, send func(*agentcomposev2.ExecAttachRequest) error, closeRequest func() error) (err error) {
	defer func() { err = errors.Join(err, closeRequest()) }()
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if text == "/exit" {
			break
		}
		if err := send(&agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text}}}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return send(&agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}}})
}

func pumpExecAttachStdin(stdin io.Reader, send func(*agentcomposev2.ExecAttachRequest) error, closeRequest func() error) (err error) {
	defer func() {
		err = errors.Join(err, closeRequest())
	}()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := stdin.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			if err := send(&agentcomposev2.ExecAttachRequest{
				Frame: &agentcomposev2.ExecAttachRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: chunk}},
			}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return send(&agentcomposev2.ExecAttachRequest{
				Frame: &agentcomposev2.ExecAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}},
			})
		}
		if readErr != nil {
			return readErr
		}
	}
}

func writeExecAttachOutput(output *terminalStreamOutput, attachOutput *agentcomposev2.AttachOutput) error {
	if attachOutput == nil {
		return nil
	}
	return output.Write(attachOutput.GetTranscript(), string(attachOutput.GetData()), attachOutput.GetStream())
}

type promptAttachInputPrompt struct {
	AgentName string
	SandboxID string
}

func writePromptAttachInputPrompt(writer io.Writer, prompt promptAttachInputPrompt, leadingNewline bool) error {
	prefix := prompt.String()
	if leadingNewline {
		prefix = "\n" + prefix
	}
	_, err := io.WriteString(writer, prefix)
	return err
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

func newCLIRunAttachServiceClient(cli cliOptions) (agentcomposev2connect.RunServiceClient, error) {
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return nil, err
	}
	return agentcomposev2connect.NewRunServiceClient(newDaemonAttachHTTPClient(clientConfig), clientConfig.BaseURL), nil
}

func newCLIExecAttachServiceClient(cli cliOptions) (agentcomposev2connect.ExecServiceClient, error) {
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return nil, err
	}
	return agentcomposev2connect.NewExecServiceClient(newDaemonAttachHTTPClient(clientConfig), clientConfig.BaseURL), nil
}

func isAttachHTTP2TransportMismatch(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "http2: frame too large") && strings.Contains(message, "HTTP/1.1 header")
}

func isAttachRPCPath(path string) bool {
	return path == agentcomposev2connect.RunServiceRunAttachProcedure || path == agentcomposev2connect.ExecServiceExecAttachProcedure
}
