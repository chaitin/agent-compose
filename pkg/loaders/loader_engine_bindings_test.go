package loaders

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
)

func TestLoaderEngineExecuteSupportsSessionRPCBindings(t *testing.T) {
	testLoaderEngineExecuteSupportsSessionRPCBindings(t)
}

func testLoaderEngineExecuteSupportsSessionRPCBindings(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  const created = scheduler.session.createSession({ title: "alpha" });
  const sessionId = created.session.summary.sessionId;
  const current = scheduler.session.getSession({ sessionId });
  const sessions = scheduler.session.listSessions();
  const proxy = scheduler.session.getSessionProxy({ sessionId });
  const stopped = scheduler.session.stopSession({ sessionId });
  const resumed = scheduler.session.ResumeSession({ sessionId });
  return { created, current, sessions, proxy, stopped, resumed, hasAlias: typeof scheduler.session.ResumeSession === "function" };
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.ResultJSON == "" {
		t.Fatalf("expected Execute to return result json")
	}
	if len(host.sessionCalls) != 6 {
		t.Fatalf("session rpc call count = %d, want %d", len(host.sessionCalls), 6)
	}
	wantCalls := []string{"CreateSession", "GetSession", "ListSessions", "GetSessionProxy", "StopSession", "ResumeSession"}
	for index, want := range wantCalls {
		if got := host.sessionCalls[index]; got != want {
			t.Fatalf("session rpc call %d = %q, want %q", index, got, want)
		}
	}
	if got := host.requests["CreateSession"]["title"]; got != "alpha" {
		t.Fatalf("CreateSession request title = %#v, want %#v", got, "alpha")
	}
	if host.requests["ListSessions"] != nil {
		t.Fatalf("expected ListSessions request payload to be nil, got %#v", host.requests["ListSessions"])
	}
	if got := host.requests["GetSession"]["sessionId"]; got != "session-from-host" {
		t.Fatalf("GetSession request sessionId = %#v, want %#v", got, "session-from-host")
	}

	var payload struct {
		Created struct {
			Session struct {
				Summary struct {
					SessionID string `json:"sessionId"`
				} `json:"summary"`
			} `json:"session"`
		} `json:"created"`
		Resumed struct {
			Session struct {
				Summary struct {
					VMStatus string `json:"vmStatus"`
				} `json:"summary"`
			} `json:"session"`
		} `json:"resumed"`
		Proxy struct {
			NotebookURL string `json:"notebookUrl"`
		} `json:"proxy"`
		HasAlias bool `json:"hasAlias"`
	}
	if err := json.Unmarshal([]byte(result.ResultJSON), &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) returned error: %v", err)
	}
	if payload.Created.Session.Summary.SessionID != "session-from-host" {
		t.Fatalf("created session id = %q, want %q", payload.Created.Session.Summary.SessionID, "session-from-host")
	}
	if payload.Resumed.Session.Summary.VMStatus != VMStatusRunning {
		t.Fatalf("resumed vm status = %q, want %q", payload.Resumed.Session.Summary.VMStatus, VMStatusRunning)
	}
	if payload.Proxy.NotebookURL == "" {
		t.Fatalf("expected proxy notebook url in result")
	}
	if !payload.HasAlias {
		t.Fatalf("expected ResumeSession alias to be registered")
	}
}

func TestLoaderEngineExecuteSupportsAgentAndLLMBindings(t *testing.T) {
	testLoaderEngineExecuteSupportsAgentAndLLMBindings(t)
}

func testLoaderEngineExecuteSupportsAgentAndLLMBindings(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  const agent = scheduler.agent("summarize this event", {
    agent: "claude",
    sessionPolicy: "new",
    timeout: "45s",
    title: "Loader Agent Session",
    driver: "microsandbox",
    guest_image: "override-guest:latest",
    workspace_id: "workspace-42",
    session_env: [
      { name: "REQUEST_ONLY", value: "request" },
      { name: "OPENAI_API_KEY", value: "sk-test", secret: true }
    ]
  });
  const llm = scheduler.llm("answer once", { model: "gpt-5.4" });
  return { agent, llm };
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.agentPrompts) != 1 || host.agentPrompts[0] != "summarize this event" {
		t.Fatalf("agent prompts = %#v, want one summarize prompt", host.agentPrompts)
	}
	if len(host.agentCalls) != 1 {
		t.Fatalf("agent call count = %d, want %d", len(host.agentCalls), 1)
	}
	if host.agentCalls[0].Agent != "claude" {
		t.Fatalf("agent request agent = %q, want %q", host.agentCalls[0].Agent, "claude")
	}
	if host.agentCalls[0].SessionPolicy != LoaderSessionPolicyNew {
		t.Fatalf("agent request session policy = %q, want %q", host.agentCalls[0].SessionPolicy, LoaderSessionPolicyNew)
	}
	if host.agentCalls[0].Timeout != 45*time.Second {
		t.Fatalf("agent request timeout = %s, want 45s", host.agentCalls[0].Timeout)
	}
	if host.agentCalls[0].Title != "Loader Agent Session" {
		t.Fatalf("agent request title = %q, want %q", host.agentCalls[0].Title, "Loader Agent Session")
	}
	if host.agentCalls[0].Driver != driverpkg.RuntimeDriverMicrosandbox {
		t.Fatalf("agent request driver = %q, want %q", host.agentCalls[0].Driver, driverpkg.RuntimeDriverMicrosandbox)
	}
	if host.agentCalls[0].GuestImage != "override-guest:latest" {
		t.Fatalf("agent request guest image = %q, want %q", host.agentCalls[0].GuestImage, "override-guest:latest")
	}
	if host.agentCalls[0].WorkspaceID != "workspace-42" {
		t.Fatalf("agent request workspace id = %q, want %q", host.agentCalls[0].WorkspaceID, "workspace-42")
	}
	if len(host.agentCalls[0].SessionEnv) != 2 {
		t.Fatalf("agent session env count = %d, want %d", len(host.agentCalls[0].SessionEnv), 2)
	}
	requestOnly := SessionEnvVar{}
	openAIKey := SessionEnvVar{}
	for _, item := range host.agentCalls[0].SessionEnv {
		switch item.Name {
		case "REQUEST_ONLY":
			requestOnly = item
		case "OPENAI_API_KEY":
			openAIKey = item
		}
	}
	if requestOnly.Value != "request" || requestOnly.Secret {
		t.Fatalf("REQUEST_ONLY env = %#v, want value=request secret=false", requestOnly)
	}
	if openAIKey.Value != "sk-test" || !openAIKey.Secret {
		t.Fatalf("OPENAI_API_KEY env = %#v, want value=sk-test secret=true", openAIKey)
	}
	if len(host.llmPrompts) != 1 || host.llmPrompts[0] != "answer once" {
		t.Fatalf("llm prompts = %#v, want one answer prompt", host.llmPrompts)
	}
	if len(host.llmCalls) != 1 {
		t.Fatalf("llm call count = %d, want %d", len(host.llmCalls), 1)
	}
	if host.llmCalls[0].Model != "gpt-5.4" {
		t.Fatalf("llm request model = %q, want %q", host.llmCalls[0].Model, "gpt-5.4")
	}

	var payload struct {
		Agent LoaderAgentResult `json:"agent"`
		LLM   LoaderLLMResult   `json:"llm"`
	}
	if err := json.Unmarshal([]byte(result.ResultJSON), &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) returned error: %v", err)
	}
	if payload.Agent.SessionID != "agent-session" {
		t.Fatalf("agent session id = %q, want %q", payload.Agent.SessionID, "agent-session")
	}
	if payload.LLM.ResponseID != "resp-1" {
		t.Fatalf("llm response id = %q, want %q", payload.LLM.ResponseID, "resp-1")
	}
}

func TestLoaderEngineExecuteSupportsAgentStructuredOutput(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  const RiskSummary = scheduler.z.object({
    summary: scheduler.z.string(),
    risk: scheduler.z.enum(["low", "high"])
  });
  const agent = scheduler.agent("summarize this event", {
    agent: "claude",
    outputSchema: RiskSummary
  });
  return { agent };
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.agentCalls) != 1 {
		t.Fatalf("agent call count = %d, want %d", len(host.agentCalls), 1)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(host.agentCalls[0].OutputSchema), &schema); err != nil {
		t.Fatalf("decode output schema: %v", err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema = %#v, want object with additionalProperties=false", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["summary"] == nil || properties["risk"] == nil {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}

	var payload struct {
		Agent struct {
			FinalText string         `json:"finalText"`
			JSON      map[string]any `json:"json"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(result.ResultJSON), &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) returned error: %v", err)
	}
	if payload.Agent.FinalText != `{"summary":"ok","risk":"low"}` {
		t.Fatalf("agent finalText = %q", payload.Agent.FinalText)
	}
	if payload.Agent.JSON["risk"] != "low" {
		t.Fatalf("agent json = %#v, want risk=low", payload.Agent.JSON)
	}
}

func TestLoaderEngineExecuteSupportsAgentPlainJSONSchema(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	_, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  return scheduler.agent("summarize this event", {
    outputSchema: {
      type: "object",
      properties: { summary: { type: "string" } },
      required: ["summary"],
      additionalProperties: false
    }
  });
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.agentCalls) != 1 {
		t.Fatalf("agent call count = %d, want %d", len(host.agentCalls), 1)
	}
	if !strings.Contains(host.agentCalls[0].OutputSchema, `"summary"`) {
		t.Fatalf("agent output schema = %q", host.agentCalls[0].OutputSchema)
	}
}

func TestLoaderEngineExecuteSupportsLLMStructuredOutput(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  const RiskSummary = scheduler.z.object({
    summary: scheduler.z.string(),
    risk: scheduler.z.enum(["low", "high"])
  });
  return scheduler.llm("summarize this event", {
    model: "gpt-5.4",
    outputSchema: RiskSummary
  });
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.llmCalls) != 1 {
		t.Fatalf("llm call count = %d, want %d", len(host.llmCalls), 1)
	}
	if !strings.Contains(host.llmCalls[0].OutputSchema, `"risk"`) {
		t.Fatalf("llm output schema = %q", host.llmCalls[0].OutputSchema)
	}
	var payload struct {
		Text string         `json:"text"`
		JSON map[string]any `json:"json"`
	}
	if err := json.Unmarshal([]byte(result.ResultJSON), &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) returned error: %v", err)
	}
	if payload.Text != `{"summary":"ok","risk":"low"}` || payload.JSON["risk"] != "low" {
		t.Fatalf("llm payload = %#v", payload)
	}
}

func TestLoaderEngineExecuteSupportsLLMPlainJSONSchema(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	_, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  return scheduler.llm("summarize this event", {
    outputSchema: {
      type: "object",
      properties: { summary: { type: "string" } },
      required: ["summary"],
      additionalProperties: false
    }
  });
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.llmCalls) != 1 {
		t.Fatalf("llm call count = %d, want %d", len(host.llmCalls), 1)
	}
	if !strings.Contains(host.llmCalls[0].OutputSchema, `"summary"`) {
		t.Fatalf("llm output schema = %q", host.llmCalls[0].OutputSchema)
	}
}

func TestLoaderEngineExecuteValidatesAgentStructuredOutput(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &invalidStructuredAgentHost{}

	_, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  return scheduler.agent("summarize this event", {
    outputSchema: scheduler.z.object({
      summary: scheduler.z.string(),
      risk: scheduler.z.enum(["low", "high"])
    })
  });
}`,
	}, host)
	if err == nil || !strings.Contains(err.Error(), "agent JSON output does not match outputSchema") {
		t.Fatalf("Execute error = %v, want structured output validation error", err)
	}
}

func TestLoaderEngineExecuteValidatesLLMStructuredOutput(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &invalidStructuredLLMHost{}

	_, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  return scheduler.llm("summarize this event", {
    outputSchema: scheduler.z.object({
      summary: scheduler.z.string(),
      risk: scheduler.z.enum(["low", "high"])
    })
  });
}`,
	}, host)
	if err == nil || !strings.Contains(err.Error(), "llm JSON output does not match outputSchema") {
		t.Fatalf("Execute error = %v, want structured output validation error", err)
	}
}

func TestLoaderEngineExecuteSupportsCommandBindings(t *testing.T) {
	testLoaderEngineExecuteSupportsCommandBindings(t)
}

func testLoaderEngineExecuteSupportsCommandBindings(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  const execResult = scheduler.exec({
    command: "python3",
    args: ["-V", "--literal value"],
    cwd: "/tmp/work",
    env: { FOO: "bar" },
    timeoutMs: 30000,
    maxOutputBytes: 128,
    title: "Loader Command Session",
    sessionPolicy: "new",
    driver: "microsandbox",
    guestImage: "command-guest:latest",
    workspaceId: "workspace-command",
    sessionEnv: { COMMAND_TOKEN: { value: "token", secret: true } }
  });
  const shellResult = scheduler.shell("echo hello", {
    cwd: "/tmp/shell",
    env: { SHELL_FOO: "baz" },
    maxOutputBytes: 64
  });
  return { execResult, shellResult };
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.commandCalls) != 2 {
		t.Fatalf("command call count = %d, want %d", len(host.commandCalls), 2)
	}
	execCall := host.commandCalls[0]
	if execCall.Mode != "exec" || execCall.Command != "python3" {
		t.Fatalf("exec call command = %#v, want mode=exec command=python3", execCall)
	}
	if got, want := strings.Join(execCall.Args, "|"), "-V|--literal value"; got != want {
		t.Fatalf("exec args = %q, want %q", got, want)
	}
	if execCall.Cwd != "/tmp/work" || execCall.Env["FOO"] != "bar" {
		t.Fatalf("exec cwd/env = %q %#v", execCall.Cwd, execCall.Env)
	}
	if execCall.TimeoutMs != 30000 || execCall.MaxOutputBytes != 128 {
		t.Fatalf("exec timeout/max = %d/%d, want 30000/128", execCall.TimeoutMs, execCall.MaxOutputBytes)
	}
	if execCall.SessionPolicy != LoaderSessionPolicyNew || execCall.Driver != driverpkg.RuntimeDriverMicrosandbox {
		t.Fatalf("exec session policy/driver = %q/%q", execCall.SessionPolicy, execCall.Driver)
	}
	if execCall.Title != "Loader Command Session" || execCall.GuestImage != "command-guest:latest" || execCall.WorkspaceID != "workspace-command" {
		t.Fatalf("exec session overrides = %#v", execCall)
	}
	if len(execCall.SessionEnv) != 1 || execCall.SessionEnv[0].Name != "COMMAND_TOKEN" || execCall.SessionEnv[0].Value != "token" || !execCall.SessionEnv[0].Secret {
		t.Fatalf("exec session env = %#v, want secret COMMAND_TOKEN", execCall.SessionEnv)
	}
	shellCall := host.commandCalls[1]
	if shellCall.Mode != "shell" || shellCall.Script != "echo hello" {
		t.Fatalf("shell call = %#v, want mode=shell script", shellCall)
	}
	if shellCall.Command != "" || len(shellCall.Args) != 0 {
		t.Fatalf("shell command/args = %q/%#v, want empty", shellCall.Command, shellCall.Args)
	}
	if shellCall.Cwd != "/tmp/shell" || shellCall.Env["SHELL_FOO"] != "baz" || shellCall.MaxOutputBytes != 64 {
		t.Fatalf("shell cwd/env/max = %#v", shellCall)
	}

	var payload struct {
		ExecResult  LoaderCommandResult `json:"execResult"`
		ShellResult LoaderCommandResult `json:"shellResult"`
	}
	if err := json.Unmarshal([]byte(result.ResultJSON), &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) returned error: %v", err)
	}
	if payload.ExecResult.SessionID != "command-session" || payload.ExecResult.CellID != "command-cell" || payload.ExecResult.Stdout != "command-output" {
		t.Fatalf("exec result = %#v", payload.ExecResult)
	}
	if payload.ShellResult.Artifacts["stdout"] != "/tmp/stdout.txt" {
		t.Fatalf("shell artifacts = %#v", payload.ShellResult.Artifacts)
	}
}

func TestLoaderEngineCommandBindingsValidateInputs(t *testing.T) {
	engine := &QJSLoaderEngine{}
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{
			name:    "exec requires object",
			script:  `function main() { return scheduler.exec("python3"); }`,
			wantErr: "scheduler.exec requires a request object",
		},
		{
			name:    "exec requires command",
			script:  `function main() { return scheduler.exec({ args: ["-V"] }); }`,
			wantErr: "scheduler.exec requires a non-empty command",
		},
		{
			name:    "exec args array",
			script:  `function main() { return scheduler.exec({ command: "python3", args: "bad" }); }`,
			wantErr: "decode scheduler.exec args",
		},
		{
			name:    "shell requires script",
			script:  `function main() { return scheduler.shell(""); }`,
			wantErr: "scheduler.shell requires a non-empty script",
		},
		{
			name:    "shell options object",
			script:  `function main() { return scheduler.shell("echo ok", "bad"); }`,
			wantErr: "scheduler.shell options must be an object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := engine.Execute(context.Background(), LoaderExecutionRequest{
				Runtime: LoaderRuntimeScheduler,
				Script:  tt.script,
			}, &recordingLoaderHost{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Execute error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoaderEngineJSONAndRegistrationBranches(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &statefulRecordingLoaderHost{state: map[string]string{"existing": `{"value":1}`}}
	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime:     LoaderRuntimeScheduler,
		PayloadJSON: `{"input":true}`,
		Script: `
const interval = scheduler.interval(function heartbeat() {}, 2500, "interval-auto");
clearInterval(interval);
scheduler.timeout(3000, function firstTimeout() {}, "timeout-number-first");
scheduler.timeout("timeout-id", 3500, function secondTimeout() {});
scheduler.cron("cron-id", "*/5 * * * *", function cronHandler(event) { return { cron: event.input }; }, { id: "cron-id", timezone: "UTC" });
scheduler.on("runtime.test.*", function onEvent(event) { return { event }; }, "event-id");

function main(payload) {
  scheduler.log("json branches", { payload });
  scheduler.state.set("nil", null);
  scheduler.state.set("bool", true);
  scheduler.state.set("number", 42);
  scheduler.state.set("string", "value");
  scheduler.state.set("nan", NaN);
  scheduler.state.set("inf", Infinity);
  scheduler.state.set("object", { nested: [1, "two"] });
  scheduler.state.set("deleteMe", undefined);
  const existing = scheduler.state.get("existing");
  const missing = scheduler.state.get("missing");
  scheduler.state.delete("string");
  return { existing, missingType: typeof missing, runtime: scheduler.runtime.name };
}
`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.ResultJSON, `"runtime":"scheduler"`) || !strings.Contains(result.ResultJSON, `"missingType":"undefined"`) {
		t.Fatalf("result json = %s", result.ResultJSON)
	}
	if len(result.Triggers) != 4 {
		t.Fatalf("trigger count = %d, want 4: %#v", len(result.Triggers), result.Triggers)
	}
	for _, want := range []string{"null", "true", "42", `"NaN"`, `"Infinity"`, `{"nested":[1,"two"]}`} {
		found := false
		for _, value := range host.setValues {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing state value %s in %#v", want, host.setValues)
		}
	}
	if len(host.deleted) < 2 {
		t.Fatalf("deleted state keys = %#v", host.deleted)
	}

	triggerResult, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime:     LoaderRuntimeScheduler,
		PayloadJSON: `{"input":true}`,
		Trigger:     &LoaderTrigger{ID: "cron-id"},
		Script: `
scheduler.timeout("timeout-id", 3500, function secondTimeout() {});
scheduler.cron("cron-id", "*/5 * * * *", function cronHandler(event) { return { cron: event.input }; }, { id: "cron-id", timezone: "UTC" });
`,
	}, host)
	if err != nil {
		t.Fatalf("Execute trigger returned error: %v", err)
	}
	if triggerResult.ResultJSON != `{"cron":true}` {
		t.Fatalf("trigger result json = %s", triggerResult.ResultJSON)
	}
	if _, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Trigger: &LoaderTrigger{ID: "missing"},
		Script:  `scheduler.on("runtime.test", function onEvent() {});`,
	}, host); err == nil || !strings.Contains(err.Error(), "loader trigger missing not found") {
		t.Fatalf("missing trigger error = %v", err)
	}
	if _, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script:  `scheduler.on("runtime.one", function first() {}); scheduler.on("runtime.two", function second() {});`,
	}, host); err == nil || !strings.Contains(err.Error(), "multiple triggers") {
		t.Fatalf("multiple triggers error = %v", err)
	}
	if _, err := engine.Validate(context.Background(), LoaderRuntimeScheduler, `scheduler.cron("*/5 * * * *", function cron() {}, { id: "a" }, { id: "b" });`); err == nil || !strings.Contains(err.Error(), "at most one options") {
		t.Fatalf("cron options error = %v", err)
	}
	if _, err := engine.Validate(context.Background(), LoaderRuntimeScheduler, `scheduler.on("", function onEvent() {});`); err == nil || !strings.Contains(err.Error(), "non-empty topic") {
		t.Fatalf("event topic error = %v", err)
	}
	if _, err := engine.Validate(context.Background(), LoaderRuntimeScheduler, `scheduler.timeout(function timeout() {}, 0);`); err == nil || !strings.Contains(err.Error(), "positive delay") {
		t.Fatalf("timeout delay error = %v", err)
	}
}

func TestLoaderEngineExecuteRejectsNonStringAgentTimeout(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	_, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  return scheduler.agent("summarize this event", { timeout: 30000 });
}`,
	}, host)
	if err == nil || !strings.Contains(err.Error(), "decode scheduler.agent timeout") {
		t.Fatalf("Execute error = %v, want timeout decode error", err)
	}
}

func TestLoaderEngineEventPublishHostAPI(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}
	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `
function main() {
  const published = scheduler.event.publish("runtime.test.requested", { value: 1 });
  return published;
}
`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.published) != 1 || !strings.Contains(host.published[0], "runtime.test.requested") {
		t.Fatalf("published calls = %#v", host.published)
	}
	if !strings.Contains(result.ResultJSON, `"eventId":"evt-test"`) {
		t.Fatalf("result json = %s", result.ResultJSON)
	}
}

func TestLoaderEngineCommandBindingsUnavailableDuringValidation(t *testing.T) {
	engine := &QJSLoaderEngine{}
	_, err := engine.Validate(context.Background(), LoaderRuntimeScheduler, `
scheduler.exec({ command: "python3", args: ["-V"] });
`)
	if err == nil || !strings.Contains(err.Error(), "scheduler.exec is unavailable during validation") {
		t.Fatalf("Validate exec error = %v", err)
	}

	_, err = engine.Validate(context.Background(), LoaderRuntimeScheduler, `
scheduler.shell("echo hello");
`)
	if err == nil || !strings.Contains(err.Error(), "scheduler.shell is unavailable during validation") {
		t.Fatalf("Validate shell error = %v", err)
	}
}

func TestLoaderEngineEventPublishUnavailableDuringValidation(t *testing.T) {
	engine := &QJSLoaderEngine{}
	_, err := engine.Validate(context.Background(), LoaderRuntimeScheduler, `
scheduler.event.publish("runtime.test.requested", { value: 1 });
`)
	if err == nil || !strings.Contains(err.Error(), "scheduler.event.publish is unavailable during validation") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestLoaderEngineExecuteLeavesAgentUnsetWhenOptionsOmitProvider(t *testing.T) {
	engine := &QJSLoaderEngine{}
	host := &recordingLoaderHost{}

	_, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime: LoaderRuntimeScheduler,
		Script: `function main() {
  return scheduler.agent("summarize this event", {
    title: "Loader Agent Session"
  });
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(host.agentCalls) != 1 {
		t.Fatalf("agent call count = %d, want %d", len(host.agentCalls), 1)
	}
	if host.agentCalls[0].Agent != "" {
		t.Fatalf("agent request agent = %q, want empty string when provider is omitted", host.agentCalls[0].Agent)
	}
	if host.agentCalls[0].Title != "Loader Agent Session" {
		t.Fatalf("agent request title = %q, want %q", host.agentCalls[0].Title, "Loader Agent Session")
	}
}
