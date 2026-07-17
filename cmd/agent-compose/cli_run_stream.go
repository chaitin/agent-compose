package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

func runComposeRunStreamAndDetail(ctx context.Context, stdout, stderr io.Writer, client agentcomposev2connect.RunServiceClient, projectID, projectName string, runReq *agentcomposev2.RunAgentRequest, suppressOutput bool) (*agentcomposev2.RunDetail, *agentcomposev2.RunSummary, []string, error) {
	stream, err := client.RunAgentStream(ctx, connect.NewRequest(runReq))
	if err != nil {
		return nil, nil, nil, commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", projectName, runReq.GetAgentName(), err))
	}
	var completed *agentcomposev2.RunSummary
	var warnings []string
	var runID string
	output := newTerminalStreamOutput(stdout, stderr)
	defer func() {
		if ctx.Err() != nil && strings.TrimSpace(runID) != "" {
			_, _ = client.StopRun(context.Background(), connect.NewRequest(&agentcomposev2.StopRunRequest{
				RunId:  runID,
				Reason: "client interrupted",
			}))
		}
	}()
	for stream.Receive() {
		event := stream.Msg()
		if strings.TrimSpace(event.GetRunId()) != "" {
			runID = event.GetRunId()
		}
		warnings = appendUniqueStrings(warnings, event.GetWarnings()...)
		if event.GetRun() != nil {
			warnings = appendUniqueStrings(warnings, event.GetRun().GetWarnings()...)
		}
		switch event.GetEventType() {
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT:
			if suppressOutput {
				continue
			}
			if err := output.Write(event.GetTranscript(), event.GetChunk(), event.GetStream()); err != nil {
				return nil, nil, nil, err
			}
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED:
			completed = event.GetRun()
			if completed.GetRunId() != "" {
				runID = completed.GetRunId()
			}
		}
	}
	if !suppressOutput {
		if err := output.Finish(); err != nil {
			return nil, nil, nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, nil, nil, commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", projectName, runReq.GetAgentName(), err))
	}
	if completed == nil {
		return nil, nil, nil, fmt.Errorf("run project %s agent %s: stream completed without terminal run", projectName, runReq.GetAgentName())
	}
	warnings = appendUniqueStrings(warnings, completed.GetWarnings()...)
	detail, err := getRunDetail(ctx, client, projectID, completed.GetRunId())
	if err != nil {
		return nil, nil, nil, commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", completed.GetRunId(), projectName, err))
	}
	return detail.Msg.GetRun(), completed, warnings, nil
}

func runInteractiveComposeRun(cmd *cobra.Command, options composeRunOptions, projectName string, client agentcomposev2connect.RunServiceClient, sandboxClient agentcomposev2connect.SandboxServiceClient, baseReq *agentcomposev2.RunAgentRequest, promptMode bool, firstPrompt, firstCommand string) (err error) {
	sandboxID := strings.TrimSpace(baseReq.GetSandboxId())
	removeOnExit := options.Remove && sandboxID == ""
	defer func() {
		if !removeOnExit || strings.TrimSpace(sandboxID) == "" {
			return
		}
		removeErr := removeSandbox(context.Background(), sandboxClient, sandboxID, true)
		if removeErr == nil {
			return
		}
		wrapped := commandExitErrorForConnect(fmt.Errorf("remove interactive sandbox %s: %w", sandboxID, removeErr))
		if err == nil {
			err = wrapped
			return
		}
		_ = writeRunWarnings(cmd.ErrOrStderr(), []string{fmt.Sprintf("interactive sandbox cleanup failed: %v", removeErr)})
	}()

	firstInput := firstCommand
	if promptMode {
		firstInput = firstPrompt
	}
	pending := make([]string, 0, 1)
	if strings.TrimSpace(firstInput) != "" {
		pending = append(pending, firstInput)
	}
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		var line string
		if len(pending) > 0 {
			line = pending[0]
			pending = pending[1:]
		} else {
			if !scanner.Scan() {
				if scanErr := scanner.Err(); scanErr != nil {
					return scanErr
				}
				return nil
			}
			line = scanner.Text()
		}
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		if input == "/exit" {
			return nil
		}
		runReq := proto.Clone(baseReq).(*agentcomposev2.RunAgentRequest)
		runReq.SandboxId = sandboxID
		if strings.TrimSpace(sandboxID) != "" {
			runReq.Driver = ""
		}
		runReq.ClientRequestId = manualRunClientRequestID(projectName, baseReq.GetAgentName(), input)
		if promptMode {
			runReq.Prompt = input
			runReq.Command = ""
		} else {
			runReq.Prompt = ""
			runReq.Command = input
		}
		detail, completed, warnings, runErr := runComposeRunStreamAndDetail(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), client, baseReq.GetProjectId(), projectName, runReq, false)
		if runErr != nil {
			return runErr
		}
		if completed.GetSandboxId() != "" {
			sandboxID = completed.GetSandboxId()
		}
		if err := writeRunWarnings(cmd.ErrOrStderr(), warnings); err != nil {
			return err
		}
		if err := composeRunCompletionError(projectName, baseReq.GetAgentName(), completed, detail); err != nil {
			return err
		}
	}
}

func writeTranscriptOrChunk(stdout, stderr io.Writer, transcript *agentcomposev2.TranscriptEvent, chunk string, stream agentcomposev2.StdioStream) error {
	text, stream := transcriptOrChunkText(transcript, chunk, stream)
	if text == "" {
		return nil
	}
	target := stdout
	if stream == agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		target = stderr
	}
	_, err := io.WriteString(target, text)
	return err
}

func transcriptOrChunkText(transcript *agentcomposev2.TranscriptEvent, chunk string, stream agentcomposev2.StdioStream) (string, agentcomposev2.StdioStream) {
	if transcript != nil {
		return transcript.GetText(), transcript.GetStream()
	}
	return chunk, stream
}

type terminalStreamOutput struct {
	stdout terminalStreamWriter
	stderr terminalStreamWriter
}

type terminalStreamWriter struct {
	writer   io.Writer
	wrote    bool
	lastByte byte
}

func newTerminalStreamOutput(stdout, stderr io.Writer) *terminalStreamOutput {
	return &terminalStreamOutput{
		stdout: terminalStreamWriter{writer: stdout},
		stderr: terminalStreamWriter{writer: stderr},
	}
}

func runSummaryTerminal(run *agentcomposev2.RunSummary) bool {
	switch run.GetStatus() {
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, agentcomposev2.RunStatus_RUN_STATUS_FAILED, agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return true
	default:
		return false
	}
}
