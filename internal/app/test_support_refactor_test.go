package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	"github.com/samber/do/v2"
)

const (
	capabilityCapsetTagName        = "capset"
	fakeGuestCommandResultSentinel = "\n__guest_result__"
)

func newTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	di := do.New()
	do.ProvideValue(di, &appconfig.Config{DataRoot: t.TempDir()})
	store, err := NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	return store
}

func newTopicEventTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	return newTestConfigStore(t)
}

func newTestLLMClient(t *testing.T, configDB *ConfigStore, text string) *LLMClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp-loader","model":"model-a","status":"completed","output_text":%q}`, text)
	}))
	t.Cleanup(server.Close)
	return NewLLMClientForTest(&appconfig.Config{LLMAPIEndpoint: server.URL, LLMModel: "model-a"}, configDB, server.Client())
}

func readRequestBodyForTest(t *testing.T, r *http.Request) string {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("ReadAll request body returned error: %v", err)
	}
	return string(data)
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("file %s = %q, want %q", path, string(data), want)
	}
}

type fixedRuntimeProvider struct {
	runtime BoxRuntime
}

func (p fixedRuntimeProvider) ForDriver(string) (BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

func (p fixedRuntimeProvider) ForSession(*Session) (BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

type fakeLoaderAgentRuntime struct {
	execCalls              int
	providers              []string
	agentSpecs             []ExecSpec
	agentDeadlineDurations []time.Duration
	agentStdout            string
	agentStderr            string
	agentOutput            string
	agentNoPayload         bool
	agentWaitForContext    bool
	commandSpecs           []ExecSpec
	commandExitCode        int
	commandStdout          string
	commandStderr          string
	commandOutput          string
	commandBlock           chan struct{}
	commandNoPayload       bool
	commandTruncated       bool
	commandStreamHook      func()
	agentExitCode          int
}

func (r *fakeLoaderAgentRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error) {
	return SessionVMInfo{}, nil
}

func (r *fakeLoaderAgentRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return true, nil
}

func (r *fakeLoaderAgentRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("unexpected Exec call")
}

func (r *fakeLoaderAgentRuntime) ExecStream(ctx context.Context, session *Session, _ VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	r.execCalls++
	if isLoaderCommandExecSpec(spec) {
		return r.execLoaderCommand(session, spec, stream)
	}
	if spec.Command == "bash" || spec.Command == "node" || spec.Command == "python3" {
		stdout := firstNonEmpty(r.commandStdout, "cell stdout\n")
		stderr := r.commandStderr
		output := firstNonEmpty(r.commandOutput, stdout+stderr)
		if stream != nil {
			stream(ExecChunk{Text: stdout})
			if stderr != "" {
				stream(ExecChunk{Text: stderr, IsStderr: true})
			}
		}
		exitCode := r.commandExitCode
		return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: exitCode, Success: exitCode == 0}, nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		r.agentDeadlineDurations = append(r.agentDeadlineDurations, time.Until(deadline))
	}
	provider := agentProviderFromExecSpec(spec)
	r.providers = append(r.providers, provider)
	r.agentSpecs = append(r.agentSpecs, spec)
	if stream != nil {
		stream(ExecChunk{Text: "loader agent transcript\n", IsStderr: true})
	}
	exitCode := r.agentExitCode
	if r.agentWaitForContext {
		<-ctx.Done()
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: firstNonZeroInt(exitCode, 1), Success: false}, ctx.Err()
	}
	if r.agentNoPayload {
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: exitCode, Success: exitCode == 0}, nil
	}
	payload := agentResultPrefix + fmt.Sprintf(`{"provider":%q,"sessionId":"agent-runtime-session","stopReason":"completed","finalText":"loader agent transcript","transcript":"loader agent transcript"}`, provider)
	return ExecResult{Stdout: payload, Stderr: "loader agent transcript\n", Output: "loader agent transcript\n" + payload, ExitCode: exitCode, Success: exitCode == 0}, nil
}

func (r *fakeLoaderAgentRuntime) execLoaderCommand(session *Session, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	r.commandSpecs = append(r.commandSpecs, spec)
	stdout := firstNonEmpty(r.commandStdout, "command stdout\n")
	stderr := r.commandStderr
	output := firstNonEmpty(r.commandOutput, stdout+stderr)
	cellID := loaderCommandCellIDFromExecSpec(spec)
	guestCellDir := filepath.Join("/data/state/cells", cellID)
	commandResult := RuntimeCommandResult{
		Stdout:          stdout,
		Stderr:          stderr,
		Output:          output,
		ExitCode:        r.commandExitCode,
		Success:         r.commandExitCode == 0,
		StdoutTruncated: r.commandTruncated,
		OutputTruncated: r.commandTruncated,
		Artifacts: RuntimeCommandArtifacts{
			Stdout:  filepath.Join(guestCellDir, "stdout.txt"),
			Stderr:  filepath.Join(guestCellDir, "stderr.txt"),
			Output:  filepath.Join(guestCellDir, "output.txt"),
			Request: filepath.Join(guestCellDir, "command-request.json"),
			Result:  filepath.Join(guestCellDir, "command-result.json"),
		},
	}
	if session != nil {
		hostCellDir := filepath.Join(hostSessionDir(session), "state", "cells", cellID)
		if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
			return ExecResult{}, err
		}
		guestResult := commandResult
		guestResult.Stdout += fakeGuestCommandResultSentinel
		if err := writeJSONArtifact(filepath.Join(hostCellDir, "command-result.json"), guestResult); err != nil {
			return ExecResult{}, err
		}
	}
	payloadJSON, err := json.Marshal(commandResult)
	if err != nil {
		return ExecResult{}, err
	}
	if stream != nil {
		stream(ExecChunk{Text: stdout})
		if stderr != "" {
			stream(ExecChunk{Text: stderr, IsStderr: true})
		}
		if r.commandStreamHook != nil {
			r.commandStreamHook()
		}
	}
	if r.commandBlock != nil {
		<-r.commandBlock
	}
	if r.commandNoPayload {
		return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: r.commandExitCode, Success: r.commandExitCode == 0}, nil
	}
	payload := commandResultPrefix + string(payloadJSON)
	return ExecResult{Stdout: payload, Output: output + payload, Success: true}, nil
}

func isLoaderCommandExecSpec(spec ExecSpec) bool {
	return spec.Command == "sh" && strings.Contains(strings.Join(spec.Args, " "), "agent-compose-runtime exec")
}

func loaderCommandCellIDFromExecSpec(spec ExecSpec) string {
	args := strings.Join(spec.Args, " ")
	marker := "/data/state/cells/"
	index := strings.Index(args, marker)
	if index < 0 {
		return ""
	}
	remainder := strings.Trim(args[index+len(marker):], "'\" ")
	if slash := strings.Index(remainder, "/"); slash >= 0 {
		return remainder[:slash]
	}
	return remainder
}

func agentProviderFromExecSpec(spec ExecSpec) string {
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
		fields := strings.Fields(strings.TrimSpace(arg[position+len(marker):]))
		if len(fields) > 0 {
			provider = strings.Trim(fields[0], "'\"")
		}
	}
	return provider
}

func writeJSONArtifact(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

type recordingLoaderEngine struct {
	requests []LoaderExecutionRequest
}

func (e *recordingLoaderEngine) Validate(context.Context, string, string) (LoaderValidationResult, error) {
	return LoaderValidationResult{}, nil
}

func (e *recordingLoaderEngine) Execute(ctx context.Context, request LoaderExecutionRequest, host LoaderHost) (LoaderExecutionResult, error) {
	e.requests = append(e.requests, request)
	if host != nil {
		if err := host.Log(ctx, "loader lifecycle", map[string]any{"step": "start"}); err != nil {
			return LoaderExecutionResult{}, err
		}
		if err := host.StateSet(ctx, "last", `{"value":1}`); err != nil {
			return LoaderExecutionResult{}, err
		}
		if value, ok, err := host.StateGet(ctx, "last"); err != nil || !ok || value != `{"value":1}` {
			return LoaderExecutionResult{}, fmt.Errorf("loader state read = %q/%t/%v", value, ok, err)
		}
	}
	return LoaderExecutionResult{ResultJSON: `{"ok":true}`}, nil
}

func int64FromMap(values map[string]any, key string) int64 {
	value, ok := values[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}

func validateLoaderPublishTopic(topic string) error {
	if !strings.HasPrefix(topic, "runtime.") && !strings.HasPrefix(topic, "workflow.") && !strings.HasPrefix(topic, "external.") {
		return fmt.Errorf("loader event topic must use runtime.*, workflow.*, or external.* prefix")
	}
	return nil
}

func jsonObjectDocument(payloadJSON string) bool {
	var payload map[string]any
	return json.Unmarshal([]byte(payloadJSON), &payload) == nil && payload != nil
}

func stripAgentResultPayload(raw string) string {
	index := strings.Index(raw, agentResultPrefix)
	if index < 0 {
		return raw
	}
	return strings.TrimRight(raw[:index], "\r\n")
}
