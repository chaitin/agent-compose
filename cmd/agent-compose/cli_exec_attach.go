package main

import (
	"context"
	"errors"
	"fmt"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"io"
	"strings"

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
	// The one-shot prompt sends no further input, so close the turn explicitly.
	// Half-closing the request alone leaves the runtime waiting for more input
	// and the response stream never completes.
	if err := stream.Send(&agentcomposev2.AttachExecRequest{Frame: &agentcomposev2.AttachExecRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}}}); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt end input: %w", projectName, err))
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

type runAttachStream = attachStream[*agentcomposev2.AttachAgentRunRequest, *agentcomposev2.AttachAgentRunResponse]

type connectAttachAgentRunClient struct {
	client agentcomposev2connect.RunServiceClient
}

func (c connectAttachAgentRunClient) AttachAgentRun(ctx context.Context) runAttachStream {
	return c.client.AttachAgentRun(ctx)
}

func runComposeAttachAgentRunCommand(cmd *cobra.Command, projectName string, client runAttachClient, req *agentcomposev2.RunAgentRequest, options composeRunOptions) (err error) {
	return runInteractiveAttach(cmd, client.AttachAgentRun, newAgentRunAttachWire(req), interactiveAttachSpec{
		command:    "run",
		label:      fmt.Sprintf("run project %s attach", projectName),
		stdinLabel: "run attach stdin",
		noResult:   fmt.Sprintf("run project %s: attach completed without result", projectName),
		tty:        options.TTY,
		finish: func(result *agentcomposev2.AttachResult) error {
			if result.GetSuccess() {
				return nil
			}
			runID := ""
			if result.GetRun() != nil {
				runID = result.GetRun().GetRunId()
			}
			return commandExitError{Code: attachResultExitCode(result), Err: fmt.Errorf("run %s in project %s failed: %s", firstNonEmptyString(runID, "attach"), projectName, firstNonEmptyString(result.GetError(), result.GetOutput(), "command failed"))}
		},
	})
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
	wire := newAgentRunAttachWire(req)
	sender := newAttachSender(stream.Send, stream.CloseRequest)
	if err := sender.Send(wire.StartFrame(attachStart{
		mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
		attachStdin: true,
	})); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s prompt attach start: %w", projectName, err))
	}
	session := &promptAttachSession{
		output: newTerminalStreamOutput(stdout, stderr),
		stdout: stdout,
		inputPrompt: &promptAttachInputPrompt{
			AgentName: req.GetAgentName(),
			SandboxID: firstNonEmptyString(req.GetSandboxId(), req.GetSandboxId()),
		},
		lastOutputEndedWithNewline: true,
	}
	promptSender := attachPromptSender[*agentcomposev2.AttachAgentRunRequest, *agentcomposev2.AttachAgentRunResponse]{sender: sender, wire: wire}
	session.onTurnCompleted = func() error {
		return promptForAttachInput(input, stdout, session.inputPrompt, &session.lastOutputEndedWithNewline, promptSender)
	}
	result, err := receiveAttachFrames(stream, wire, fmt.Sprintf("run project %s prompt attach", projectName), session.handle)
	if err != nil {
		return err
	}
	if err := session.output.Finish(); err != nil {
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

func attachResultExitCode(result *agentcomposev2.AttachResult) int {
	if result == nil || result.GetExitCode() == 0 {
		return exitCodeGeneral
	}
	return int(result.GetExitCode())
}

type execAttachStream = attachStream[*agentcomposev2.AttachExecRequest, *agentcomposev2.AttachExecResponse]

type connectAttachExecClient struct {
	client agentcomposev2connect.ExecServiceClient
}

func (c connectAttachExecClient) AttachExec(ctx context.Context) execAttachStream {
	return c.client.AttachExec(ctx)
}

func runComposeAttachExecCommand(cmd *cobra.Command, projectName string, client execAttachClient, req *agentcomposev2.ExecRequest, options composeExecOptions) (err error) {
	return runInteractiveAttach(cmd, client.AttachExec, newExecAttachWire(req), interactiveAttachSpec{
		command:    "exec",
		label:      fmt.Sprintf("exec project %s attach", projectName),
		stdinLabel: "exec attach stdin",
		noResult:   fmt.Sprintf("exec project %s: attach completed without result", projectName),
		tty:        options.TTY,
		finish: func(result *agentcomposev2.AttachResult) error {
			execResult := execResultFromAttachResult(result)
			if execResult.GetSuccess() {
				return nil
			}
			return commandExitError{Code: execResultExitCode(execResult), Err: fmt.Errorf("exec %s in sandbox %s failed: %s", execResult.GetExecId(), execResult.GetSandboxId(), firstNonEmptyString(execResult.GetError(), execResult.GetStderr(), execResult.GetOutput(), "command failed"))}
		},
	})
}

func runComposeExecPromptAttachCommand(cmd *cobra.Command, projectName string, client execAttachClient, req *agentcomposev2.ExecRequest, options composeExecOptions) (retErr error) {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	attachCtx, cancelAttach := context.WithCancel(cmd.Context())
	defer cancelAttach()
	stream := client.AttachExec(attachCtx)
	wire := newExecAttachWire(req)
	sender := newAttachSender(stream.Send, stream.CloseRequest)
	if err := sender.Send(wire.StartFrame(attachStart{
		mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
		prompt:      strings.TrimSpace(options.Prompt),
		attachStdin: true,
	})); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s prompt attach start: %w", projectName, err))
	}
	input := newPromptLineReader(stdin, stderr)
	defer func() { retErr = errors.Join(retErr, input.Close()) }()
	stdinIsTerminal := input.IsTerminal()
	var inputErr <-chan error
	if !stdinIsTerminal {
		ch := make(chan error, 1)
		inputErr = ch
		go func() { ch <- pumpPromptMessages(input, sender, wire) }()
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
	session := &promptAttachSession{
		output:                     newTerminalStreamOutput(stdout, stderr),
		stdout:                     stdout,
		inputPrompt:                &promptAttachInputPrompt{SandboxID: req.GetSandboxId()},
		lastOutputEndedWithNewline: true,
	}
	if stdinIsTerminal {
		promptSender := attachPromptSender[*agentcomposev2.AttachExecRequest, *agentcomposev2.AttachExecResponse]{sender: sender, wire: wire}
		session.onTurnCompleted = func() error {
			return promptForAttachInput(input, stdout, session.inputPrompt, &session.lastOutputEndedWithNewline, promptSender)
		}
	}
	result, err := receiveAttachFrames(stream, wire, fmt.Sprintf("exec project %s prompt attach", projectName), session.handle)
	if err != nil {
		return err
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
	if err := session.output.Finish(); err != nil {
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
	return strings.Contains(message, "http2: frame too large") && strings.Contains(message, "HTTP/1.1 header")
}

func isAttachRPCPath(path string) bool {
	return path == agentcomposev2connect.RunServiceAttachAgentRunProcedure || path == agentcomposev2connect.ExecServiceAttachExecProcedure
}
