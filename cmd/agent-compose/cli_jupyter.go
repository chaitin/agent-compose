package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"connectrpc.com/connect"
)

const detachedRunJupyterSandboxWait = 30 * time.Second

type runDetailGetter interface {
	GetRun(context.Context, *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error)
}

type composeRunJupyterOutput struct {
	URL  string
	Path string
}

func runJupyterRequested(req *agentcomposev2.RunAgentRequest) bool {
	return req != nil && req.GetJupyter() != nil && req.GetJupyter().GetEnabled()
}

func runJupyterURLShouldBePrinted(req *agentcomposev2.RunAgentRequest) bool {
	return runJupyterRequested(req) && req.GetCleanupPolicy() == agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING
}

func resolveRunJupyterOutput(ctx context.Context, cli cliOptions, sandboxID string) (composeRunJupyterOutput, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return composeRunJupyterOutput{}, fmt.Errorf("jupyter URL unavailable: run did not report a sandbox")
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return composeRunJupyterOutput{}, err
	}
	sandbox, err := clients.sandbox.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		return composeRunJupyterOutput{}, commandExitErrorForConnect(fmt.Errorf("load sandbox %s jupyter proxy: %w", sandboxID, err))
	}
	proxyPath := strings.TrimSpace(sandbox.Msg.GetSandbox().GetProxyPath())
	notebookURL := strings.TrimSpace(sandbox.Msg.GetSandbox().GetNotebookUrl())
	if proxyPath == "" && notebookURL == "" {
		return composeRunJupyterOutput{}, fmt.Errorf("jupyter URL unavailable: sandbox %s did not report a proxy path", sandboxID)
	}
	baseURL, err := browserBaseURLForCLI(cli)
	if err != nil {
		return composeRunJupyterOutput{}, err
	}
	return composeRunJupyterOutput{
		URL:  joinBaseURLAndPath(baseURL, firstNonEmptyString(notebookURL, proxyPath)),
		Path: proxyPath,
	}, nil
}

func resolveDetachedRunJupyterOutput(ctx context.Context, cli cliOptions, runClient agentcomposev2connect.RunServiceClient, run *agentcomposev2.RunSummary) (composeRunJupyterOutput, *agentcomposev2.RunSummary, error) {
	if run == nil {
		return composeRunJupyterOutput{}, run, fmt.Errorf("jupyter URL unavailable: run did not report a summary")
	}
	sandboxID := runSummarySandboxID(run)
	if sandboxID == "" {
		updated, err := waitForDetachedRunSandbox(ctx, runClient, run.GetProjectId(), run.GetRunId(), detachedRunJupyterSandboxWait)
		if err != nil {
			return composeRunJupyterOutput{}, run, err
		}
		run = updated
		sandboxID = runSummarySandboxID(run)
	}
	jupyter, err := resolveRunJupyterOutput(ctx, cli, sandboxID)
	return jupyter, run, err
}

func waitForDetachedRunSandbox(ctx context.Context, client runDetailGetter, projectID, runID string, timeout time.Duration) (*agentcomposev2.RunSummary, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("jupyter URL unavailable: run did not report an id")
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last *agentcomposev2.RunSummary
	for {
		if waitCtx.Err() != nil {
			return last, fmt.Errorf("jupyter URL unavailable: timed out waiting for run %s to report a sandbox", runID)
		}
		detail, err := client.GetRun(waitCtx, connect.NewRequest(&agentcomposev2.GetRunRequest{
			ProjectId: strings.TrimSpace(projectID),
			RunId:     runID,
		}))
		if err != nil {
			if waitCtx.Err() != nil {
				return last, fmt.Errorf("jupyter URL unavailable: timed out waiting for run %s to report a sandbox", runID)
			}
			return nil, commandExitErrorForConnect(fmt.Errorf("wait for run %s sandbox for jupyter URL: %w", runID, err))
		}
		last = detail.Msg.GetRun().GetSummary()
		if runSummarySandboxID(last) != "" {
			return last, nil
		}
		if runSummaryTerminal(last) {
			return last, fmt.Errorf("jupyter URL unavailable: run %s completed before reporting a sandbox", runID)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return last, fmt.Errorf("jupyter URL unavailable: timed out waiting for run %s to report a sandbox", runID)
		case <-timer.C:
		}
	}
}

func browserBaseURLForCLI(cli cliOptions) (string, error) {
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return "", err
	}
	if !clientConfig.UseUnixSocket {
		return strings.TrimRight(strings.TrimSpace(clientConfig.BaseURL), "/"), nil
	}
	return "", nil
}

func joinBaseURLAndPath(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	lowerPath := strings.ToLower(path)
	if strings.HasPrefix(lowerPath, "http://") || strings.HasPrefix(lowerPath, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if baseURL == "" {
		return path
	}
	return baseURL + path
}

func writeJupyterRunText(out io.Writer, jupyter composeRunJupyterOutput) error {
	value := firstNonEmptyString(jupyter.URL, jupyter.Path)
	if value == "" {
		return nil
	}
	_, err := fmt.Fprintf(out, "Jupyter: %s\n", value)
	return err
}
