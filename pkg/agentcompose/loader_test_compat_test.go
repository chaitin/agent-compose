package agentcompose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	buspkg "agent-compose/pkg/bus"
	appconfig "agent-compose/pkg/config"
	executorpkg "agent-compose/pkg/executor"
	"agent-compose/pkg/images"
	llmpkg "agent-compose/pkg/llm"
	loaderspkg "agent-compose/pkg/loaders"
	modelpkg "agent-compose/pkg/model"
	runtimespkg "agent-compose/pkg/runtimes"
	"agent-compose/pkg/storage"
)

func newTestConfigStore(t *testing.T) *storage.ConfigStore {
	t.Helper()
	root := t.TempDir()
	return mustTestConfigStore(t, &appconfig.Config{
		DataRoot: root,
		DbAddr:   filepath.Join(root, "data.db"),
	})
}

func newTestLLMClient(t *testing.T, configDB *storage.ConfigStore, text string) *llmpkg.LLMClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp-loader","model":"model-a","status":"completed","output_text":%q}`, text)
	}))
	t.Cleanup(server.Close)
	return llmpkg.NewClient(&appconfig.Config{LLMAPIEndpoint: server.URL, LLMModel: "model-a"}, configDB, server.Client())
}

func newTestLoaderManager(t testing.TB, deps loaderspkg.ManagerDeps) *loaderspkg.LoaderManager {
	t.Helper()
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	if deps.Config == nil {
		root := t.TempDir()
		deps.Config = &appconfig.Config{
			DataRoot:    filepath.Join(root, "data"),
			SessionRoot: filepath.Join(root, "sessions"),
		}
	}
	if deps.ConfigDB == nil {
		if tt, ok := t.(*testing.T); ok {
			deps.ConfigDB = newTestConfigStore(tt)
		} else {
			t.Fatalf("newTestLoaderManager requires *testing.T when ConfigDB is omitted")
		}
	}
	if deps.Bus == nil {
		deps.Bus = buspkg.NewLoaderBusWithBuffer(16)
	}
	if deps.Engine == nil {
		deps.Engine = &loaderspkg.QJSLoaderEngine{}
	}
	if deps.Images == nil {
		deps.Images = images.NewDockerImageBackend()
	}
	if deps.Executor == nil {
		deps.Executor = loaderspkg.NewExecutor(deps.Config, deps.Store, deps.ConfigDB, nil, nil)
	}
	manager, err := loaderspkg.NewManager(deps)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	return manager
}

type recordingLoaderEngine struct {
	requests []loaderspkg.LoaderExecutionRequest
}

func (e *recordingLoaderEngine) Validate(context.Context, string, string) (loaderspkg.LoaderValidationResult, error) {
	return loaderspkg.LoaderValidationResult{}, nil
}

func (e *recordingLoaderEngine) Execute(ctx context.Context, request loaderspkg.LoaderExecutionRequest, host loaderspkg.LoaderHost) (loaderspkg.LoaderExecutionResult, error) {
	e.requests = append(e.requests, request)
	if err := host.Log(ctx, "loader lifecycle", map[string]any{"step": "start"}); err != nil {
		return loaderspkg.LoaderExecutionResult{}, err
	}
	if err := host.StateSet(ctx, "last", `{"value":1}`); err != nil {
		return loaderspkg.LoaderExecutionResult{}, err
	}
	if err := host.StateSet(ctx, "temporary", `{"delete":true}`); err != nil {
		return loaderspkg.LoaderExecutionResult{}, err
	}
	if err := host.StateDelete(ctx, "temporary"); err != nil {
		return loaderspkg.LoaderExecutionResult{}, err
	}
	if value, ok, err := host.StateGet(ctx, "last"); err != nil || !ok || value != `{"value":1}` {
		return loaderspkg.LoaderExecutionResult{}, fmt.Errorf("loader state read = %q/%t/%v", value, ok, err)
	}
	if llm, err := host.LLM(ctx, "summarize lifecycle", loaderspkg.LoaderLLMRequest{Model: "model-a"}); err != nil || llm.Text != "loader llm text" {
		return loaderspkg.LoaderExecutionResult{}, fmt.Errorf("loader llm result = %#v/%v", llm, err)
	}
	if _, err := host.PublishEvent(ctx, "runtime.test.completed", `{"provider":"test-runtime","value":1}`); err != nil {
		return loaderspkg.LoaderExecutionResult{}, err
	}
	return loaderspkg.LoaderExecutionResult{ResultJSON: `{"ok":true}`}, nil
}

type fixedRuntimeProvider struct {
	runtime runtimespkg.BoxRuntime
}

func (p fixedRuntimeProvider) ForDriver(string) (runtimespkg.BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

func (p fixedRuntimeProvider) ForSession(*modelpkg.Session) (runtimespkg.BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

type fakeLoaderAgentRuntime struct {
	execCalls              int
	providers              []string
	agentSpecs             []modelpkg.ExecSpec
	agentDeadlineDurations []time.Duration
	agentStdout            string
	agentStderr            string
	agentOutput            string
	agentNoPayload         bool
	agentWaitForContext    bool
	commandSpecs           []modelpkg.ExecSpec
	commands               []modelpkg.ExecSpec
	commandExitCode        int
	commandStdout          string
	commandStderr          string
	commandOutput          string
	commandBlock           chan struct{}
	commandNoPayload       bool
	commandStreamHook      func()
	agentExitCode          int
}

func (r *fakeLoaderAgentRuntime) EnsureSession(context.Context, *modelpkg.Session, modelpkg.VMState, modelpkg.ProxyState) (runtimespkg.SessionVMInfo, error) {
	return runtimespkg.SessionVMInfo{}, nil
}

func (r *fakeLoaderAgentRuntime) StopSession(context.Context, *modelpkg.Session, modelpkg.VMState) (bool, error) {
	return true, nil
}

func (r *fakeLoaderAgentRuntime) Exec(context.Context, *modelpkg.Session, modelpkg.VMState, modelpkg.ExecSpec) (modelpkg.ExecResult, error) {
	return modelpkg.ExecResult{}, fmt.Errorf("unexpected Exec call")
}

func (r *fakeLoaderAgentRuntime) ExecStream(ctx context.Context, session *modelpkg.Session, _ modelpkg.VMState, spec modelpkg.ExecSpec, stream modelpkg.ExecStreamWriter) (modelpkg.ExecResult, error) {
	r.execCalls++
	if isLoaderCommandExecSpec(spec) {
		r.commandSpecs = append(r.commandSpecs, spec)
		r.commands = append(r.commands, spec)
		stdout := firstNonEmpty(r.commandStdout, "command stdout\n")
		stderr := r.commandStderr
		output := firstNonEmpty(r.commandOutput, stdout+stderr)
		cellID := "cell-fake"
		if session != nil {
			hostCellDir := filepath.Join(hostSessionDir(session), "state", "cells", cellID)
			_ = os.MkdirAll(hostCellDir, 0o755)
		}
		result := modelpkg.RuntimeCommandResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: r.commandExitCode, Success: r.commandExitCode == 0}
		payload, _ := json.Marshal(result)
		if stream != nil {
			stream(modelpkg.ExecChunk{Text: stdout})
			if stderr != "" {
				stream(modelpkg.ExecChunk{Text: stderr, IsStderr: true})
			}
			if r.commandStreamHook != nil {
				r.commandStreamHook()
			}
		}
		if r.commandBlock != nil {
			<-r.commandBlock
		}
		if r.commandNoPayload {
			return modelpkg.ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: r.commandExitCode, Success: r.commandExitCode == 0}, nil
		}
		payloadText := executorpkg.CommandResultPrefix + string(payload)
		return modelpkg.ExecResult{Stdout: payloadText, Stderr: "", Output: output + payloadText, ExitCode: r.commandExitCode, Success: r.commandExitCode == 0}, nil
	}
	if spec.Command == "bash" || spec.Command == "node" || spec.Command == "python3" {
		stdout := firstNonEmpty(r.commandStdout, "cell stdout\n")
		stderr := r.commandStderr
		output := firstNonEmpty(r.commandOutput, stdout+stderr)
		if stream != nil {
			stream(modelpkg.ExecChunk{Text: stdout})
			if stderr != "" {
				stream(modelpkg.ExecChunk{Text: stderr, IsStderr: true})
			}
		}
		exitCode := r.commandExitCode
		return modelpkg.ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: exitCode, Success: exitCode == 0}, nil
	}

	if deadline, ok := ctx.Deadline(); ok {
		r.agentDeadlineDurations = append(r.agentDeadlineDurations, time.Until(deadline))
	}
	provider := providerFromExecSpec(spec)
	r.providers = append(r.providers, provider)
	r.agentSpecs = append(r.agentSpecs, spec)
	if stream != nil {
		stream(modelpkg.ExecChunk{Text: "loader agent transcript\n", IsStderr: true})
	}
	exitCode := r.agentExitCode
	if r.agentWaitForContext {
		<-ctx.Done()
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return modelpkg.ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: firstNonZeroInt(exitCode, 1), Success: false}, ctx.Err()
	}
	if r.agentNoPayload {
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return modelpkg.ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: exitCode, Success: exitCode == 0}, nil
	}
	payload := executorpkg.AgentResultPrefix + fmt.Sprintf(`{"provider":%q,"agent":%q,"sessionId":"agent-runtime-session","stopReason":"completed","finalText":"loader agent transcript","transcript":"loader agent transcript","success":%t,"exitCode":%d}`, provider, provider, exitCode == 0, exitCode)
	return modelpkg.ExecResult{Stdout: payload, Stderr: "loader agent transcript\n", Output: "loader agent transcript\n" + payload, ExitCode: exitCode, Success: exitCode == 0}, nil
}

func isLoaderCommandExecSpec(spec modelpkg.ExecSpec) bool {
	if spec.Command != "sh" {
		return false
	}
	return strings.Contains(strings.Join(spec.Args, " "), "agent-compose-runtime exec")
}

func providerFromExecSpec(spec modelpkg.ExecSpec) string {
	provider := "codex"
	for index, arg := range spec.Args {
		if arg == "--provider" && index+1 < len(spec.Args) {
			return strings.Trim(spec.Args[index+1], "'\"")
		}
		marker := "--provider "
		position := strings.Index(arg, marker)
		if position < 0 {
			continue
		}
		remainder := strings.TrimSpace(arg[position+len(marker):])
		if remainder == "" {
			continue
		}
		return strings.Trim(strings.Fields(remainder)[0], "'\"")
	}
	return provider
}
