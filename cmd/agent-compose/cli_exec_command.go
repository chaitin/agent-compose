package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

func executeCLI(ctx context.Context, out, errOut io.Writer, args []string, runDaemon daemonRunner) int {
	cmd := newRootCommand(out, errOut, runDaemon)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return commandExitCode(err)
	}
	return 0
}

type composeExecOptions struct {
	RunID       string
	Command     string
	Prompt      string
	Cwd         string
	Interactive bool
	TTY         bool
}

func executeComposeRunRequest(cmd *cobra.Command, cli cliOptions, projectName, projectID string, client agentcomposev2connect.RunServiceClient, runReq *agentcomposev2.RunAgentRequest, detach bool) error {
	if detach {
		return startDetachedRun(cmd, cli, projectName, client, runReq)
	}
	detail, completed, warnings, err := runComposeRunStreamAndDetail(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), client, projectID, projectName, runReq, cli.JSON)
	if err != nil {
		return err
	}
	if cli.JSON {
		output := composeRunOutputFromDetail(detail)
		output.Warnings = appendUniqueStrings(output.Warnings, warnings...)
		if runJupyterURLShouldBePrinted(runReq) {
			jupyter, resolveErr := resolveRunJupyterOutput(cmd.Context(), cli, runSummarySandboxID(completed))
			if resolveErr != nil {
				warnings = appendUniqueStrings(warnings, resolveErr.Error())
				output.Warnings = appendUniqueStrings(output.Warnings, resolveErr.Error())
			}
			output.JupyterURL = jupyter.URL
			output.JupyterPath = jupyter.Path
		}
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if !cli.JSON {
		if runJupyterURLShouldBePrinted(runReq) {
			jupyter, resolveErr := resolveRunJupyterOutput(cmd.Context(), cli, runSummarySandboxID(completed))
			if resolveErr != nil {
				warnings = appendUniqueStrings(warnings, resolveErr.Error())
			} else if err := writeJupyterRunText(cmd.OutOrStdout(), jupyter); err != nil {
				return err
			}
		}
		if err := writeRunWarnings(cmd.ErrOrStderr(), warnings); err != nil {
			return err
		}
	}
	return composeRunCompletionError(projectName, runReq.GetAgentName(), completed, detail)
}

func composeExecArgs(cmd *cobra.Command, args []string) error {
	if cmd.Flags().Changed("run") {
		return nil
	}
	return cobra.MinimumNArgs(1)(cmd, args)
}

func runComposeExecCommand(cmd *cobra.Command, cli cliOptions, options composeExecOptions, args []string) error {
	if options.TTY && !options.Interactive {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec -t/--tty requires -i/--interactive")}
	}
	if cli.JSON && (options.Interactive || options.TTY) {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec --json cannot be used with -i/--interactive or -t/--tty")}
	}
	if !cmd.Flags().Changed("run") && len(args) > 0 && strings.TrimSpace(args[0]) == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec requires non-empty sandbox")}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	runtimeProject, err := resolveComposeRuntimeProject(cmd.Context(), clients.project, cli, "exec", runtimeProjectIdentityOnly)
	if err != nil {
		return err
	}
	projectID := runtimeProject.id()
	req, err := normalizeComposeExecRequest(cmd, clients, projectID, options, args)
	if err != nil {
		return err
	}
	if options.Interactive {
		attachClient, err := newCLIExecAttachServiceClient(cli)
		if err != nil {
			return err
		}
		if strings.TrimSpace(options.Prompt) != "" {
			return runComposeExecPromptAttachCommand(cmd, runtimeProject.name(), connectExecAttachClient{client: attachClient}, req, options)
		}
		return runComposeExecAttachCommand(cmd, runtimeProject.name(), connectExecAttachClient{client: attachClient}, req, options)
	}
	if strings.TrimSpace(options.Prompt) != "" {
		attachClient, err := newCLIExecAttachServiceClient(cli)
		if err != nil {
			return err
		}
		return runComposeExecPromptOnceCommand(cmd, runtimeProject.name(), connectExecAttachClient{client: attachClient}, req, options, cli.JSON)
	}
	stream, err := clients.execStream.ExecStream(cmd.Context(), connect.NewRequest(req))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s: %w", runtimeProject.name(), err))
	}
	var result *agentcomposev2.ExecResult
	output := newTerminalStreamOutput(cmd.OutOrStdout(), cmd.ErrOrStderr())
	for stream.Receive() {
		event := stream.Msg()
		switch event.GetEventType() {
		case agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT:
			if cli.JSON {
				continue
			}
			if err := output.Write(event.GetTranscript(), event.GetChunk(), event.GetStream()); err != nil {
				return err
			}
		case agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED:
			result = event.GetResult()
		}
	}
	if !cli.JSON {
		if err := output.Finish(); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s: %w", runtimeProject.name(), err))
	}
	if result == nil {
		return fmt.Errorf("exec project %s: stream completed without result", runtimeProject.name())
	}
	if cli.JSON {
		data, err := json.MarshalIndent(composeExecOutputFromResult(result), "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if !result.GetSuccess() {
		return commandExitError{Code: execResultExitCode(result), Err: fmt.Errorf("exec %s in sandbox %s failed: %s", result.GetExecId(), result.GetSandboxId(), firstNonEmptyString(result.GetError(), result.GetStderr(), result.GetOutput(), "command failed"))}
	}
	return nil
}

func (p *promptAttachInputPrompt) UpdateFromStarted(started *agentcomposev2.AttachStarted) {
	if started == nil {
		return
	}
	p.UpdateFromRun(started.GetRun())
	if sessionID := strings.TrimSpace(started.GetSandboxId()); sessionID != "" {
		p.SandboxID = sessionID
	}
}

func (p *promptAttachInputPrompt) UpdateFromRun(run *agentcomposev2.RunSummary) {
	if run == nil {
		return
	}
	if agentName := strings.TrimSpace(run.GetAgentName()); agentName != "" {
		p.AgentName = agentName
	}
	if sandboxID := firstNonEmptyString(run.GetSandboxId(), run.GetSandboxId()); sandboxID != "" {
		p.SandboxID = sandboxID
	}
}

func (p promptAttachInputPrompt) String() string {
	agentName := strings.TrimSpace(p.AgentName)
	if agentName == "" {
		agentName = "agent"
	}
	sandboxID := shortOpaqueID(p.SandboxID)
	if sandboxID == "" {
		sandboxID = "sandbox"
	}
	return fmt.Sprintf("%s@%s:> ", agentName, sandboxID)
}

func normalizeComposeExecRequest(cmd *cobra.Command, clients cliServiceClients, projectID string, options composeExecOptions, args []string) (*agentcomposev2.ExecRequest, error) {
	commandText := strings.TrimSpace(options.Command)
	promptText := strings.TrimSpace(options.Prompt)
	if commandText != "" && promptText != "" {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec requires only one of --command or --prompt")}
	}
	positionalCommand := len(args) > 1
	if commandText == "" && promptText == "" && !positionalCommand {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec requires --command, --prompt, or a command after --")}
	}
	if positionalCommand && (commandText != "" || promptText != "") {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec positional command cannot be combined with --command or --prompt")}
	}
	legacyTargetFlags := []string{}
	if cmd.Flags().Changed("run") {
		legacyTargetFlags = append(legacyTargetFlags, "--run")
	}
	if len(legacyTargetFlags) > 1 {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec target can only be specified once")}
	}
	if len(legacyTargetFlags) > 0 {
		if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose exec "+legacyTargetFlags[0], "agent-compose exec <sandbox>"); err != nil {
			return nil, err
		}
		command, err := composeExecCommandFromArgs(options, nil)
		if err != nil {
			return nil, err
		}
		req := &agentcomposev2.ExecRequest{
			Command: command,
			Cwd:     strings.TrimSpace(options.Cwd),
		}
		switch legacyTargetFlags[0] {
		case "--run":
			runID := strings.TrimSpace(options.RunID)
			if runID == "" {
				return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec --run requires a value")}
			}
			runID, err = resolveComposeRunIDRef(cmd.Context(), clients.run, projectID, "", runID)
			if err != nil {
				return nil, err
			}
			req.Target = &agentcomposev2.ExecRequest_RunId{RunId: runID}
		}
		return req, nil
	}
	sandbox := strings.TrimSpace(args[0])
	if sandbox == "" {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec requires non-empty sandbox")}
	}
	sandbox, err := resolveComposeSandboxRefWithProject(cmd.Context(), clients, projectID, sandbox)
	if err != nil {
		return nil, err
	}
	command, err := composeExecCommandFromArgs(options, args[1:])
	if err != nil {
		return nil, err
	}
	return &agentcomposev2.ExecRequest{
		Command: command,
		Cwd:     strings.TrimSpace(options.Cwd),
		Target:  &agentcomposev2.ExecRequest_SandboxId{SandboxId: sandbox},
	}, nil
}

func composeExecCommandFromArgs(options composeExecOptions, args []string) (*agentcomposev2.ExecCommand, error) {
	commandText := strings.TrimSpace(options.Command)
	if commandText != "" {
		if len(args) > 0 {
			return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec command can be specified either with --command or positional arguments, not both")}
		}
		return &agentcomposev2.ExecCommand{Command: "bash", Args: []string{"-lc", commandText}}, nil
	}
	if strings.TrimSpace(options.Prompt) != "" {
		return nil, nil
	}
	if len(args) > 0 {
		return &agentcomposev2.ExecCommand{Command: args[0], Args: append([]string(nil), args[1:]...)}, nil
	}
	return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("exec requires --command, --prompt, or a command after --")}
}

type composeExecOutput struct {
	ExecID    string   `json:"exec_id"`
	SandboxID string   `json:"sandbox_id"`
	RunID     string   `json:"run_id,omitempty"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	ExitCode  int32    `json:"exit_code"`
	Success   bool     `json:"success"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	Output    string   `json:"output,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func composeExecOutputFromResult(result *agentcomposev2.ExecResult) composeExecOutput {
	return composeExecOutput{
		ExecID:    displayOpaqueID(result.GetExecId()),
		SandboxID: displayOpaqueID(result.GetSandboxId()),
		RunID:     displayOpaqueID(result.GetRunId()),
		Command:   result.GetCommand().GetCommand(),
		Args:      append([]string(nil), result.GetCommand().GetArgs()...),
		Cwd:       result.GetCwd(),
		ExitCode:  result.GetExitCode(),
		Success:   result.GetSuccess(),
		Stdout:    result.GetStdout(),
		Stderr:    result.GetStderr(),
		Output:    result.GetOutput(),
		Error:     result.GetError(),
	}
}

func execResultExitCode(result *agentcomposev2.ExecResult) int {
	if code := int(result.GetExitCode()); code > 0 && code < 126 {
		return code
	}
	return exitCodeGeneral
}
