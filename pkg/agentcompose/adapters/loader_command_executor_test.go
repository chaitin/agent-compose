package adapters

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/sessionstore"
)

type fakeLoaderCommandRuntime struct{}

func (r fakeLoaderCommandRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (r fakeLoaderCommandRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r fakeLoaderCommandRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r fakeLoaderCommandRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r fakeLoaderCommandRuntime) ExecStream(_ context.Context, _ *domain.Sandbox, _ domain.VMState, _ domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	commandResult := domain.RuntimeCommandResult{
		Stdout:   "loader stdout\n",
		Stderr:   "loader stderr\n",
		Output:   "loader stdout\nloader stderr\n",
		ExitCode: 0,
		Success:  true,
	}
	payloadBytes, _ := json.Marshal(commandResult)
	payload := execution.CommandResultPrefix + string(payloadBytes) + "\n"
	if stream != nil {
		stream(domain.ExecChunk{Text: "loader stdout\n"})
		stream(domain.ExecChunk{Text: "loader stderr\n", Stream: domain.StdioStderr})
		stream(domain.ExecChunk{Text: payload})
	}
	return domain.ExecResult{
		Stdout:   "loader stdout\n" + payload,
		Stderr:   "loader stderr\n",
		Output:   "loader stdout\nloader stderr\n" + payload,
		ExitCode: 0,
		Success:  true,
	}, nil
}

func TestLoaderCommandExecutorFiltersCommandPayloadFromStreamingCellOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "loader command sandbox", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	streams := sessions.NewStreamBrokerForTest()
	ch, unsubscribe := streams.Subscribe(session.Summary.ID)
	defer unsubscribe()
	executor := NewLoaderCommandExecutor(config, store, nil, fakeRuntimeProvider{runtime: fakeLoaderCommandRuntime{}}, streams)

	result, err := executor.ExecuteLoaderCommand(ctx, session, domain.LoaderCommandRequest{
		Mode:   "shell",
		Script: "echo loader",
	})
	if err != nil {
		t.Fatalf("ExecuteLoaderCommand returned error: %v", err)
	}
	if !result.Success || result.Stdout != "loader stdout\n" || result.Stderr != "loader stderr\n" {
		t.Fatalf("loader result = %#v", result)
	}

	var outputText strings.Builder
	for {
		select {
		case event := <-ch:
			if event.EventType == sessions.WatchEventTypeCellOutput {
				outputText.WriteString(event.Chunk)
				if strings.Contains(event.Chunk, execution.CommandResultPrefix) {
					t.Fatalf("stream event leaked command payload: %#v", event)
				}
			}
		default:
			goto drained
		}
	}

drained:
	if got := outputText.String(); !strings.Contains(got, "loader stdout\n") || !strings.Contains(got, "loader stderr\n") {
		t.Fatalf("stream output = %q", got)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) == 0 {
		t.Fatalf("no cells stored")
	}
	for _, cell := range cells {
		if strings.Contains(cell.Output, execution.CommandResultPrefix) {
			t.Fatalf("cell leaked command payload: %#v", cell)
		}
	}
}
