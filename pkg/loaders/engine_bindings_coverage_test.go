package loaders

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

type coverageEngineHost struct {
	sessionCalls []string
	requests     map[string]map[string]any
	agentCalls   []domain.LoaderAgentRequest
	llmCalls     []domain.LoaderLLMRequest
	commandCalls []domain.LoaderCommandRequest
	state        map[string]string
	setValues    []string
	deleted      []string
	published    []string
}

func (h *coverageEngineHost) Log(context.Context, string, any) error { return nil }

func (h *coverageEngineHost) Agent(_ context.Context, _ string, request domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	h.agentCalls = append(h.agentCalls, request)
	text := "agent-output"
	if strings.TrimSpace(request.OutputSchema) != "" {
		text = `{"summary":"ok","risk":"low"}`
	}
	return domain.LoaderAgentResult{
		Text: text, Output: text, FinalText: text, SessionID: "agent-session", CellID: "agent-cell",
		Agent: firstNonEmptyTest(request.Agent, "codex"), AgentSessionID: "agent-runtime-session", StopReason: "completed", Success: true,
	}, nil
}

func (h *coverageEngineHost) LLM(_ context.Context, _ string, request domain.LoaderLLMRequest) (domain.LoaderLLMResult, error) {
	h.llmCalls = append(h.llmCalls, request)
	text := "llm-output"
	if strings.TrimSpace(request.OutputSchema) != "" {
		text = `{"summary":"ok","risk":"low"}`
	}
	return domain.LoaderLLMResult{Text: text, Model: firstNonEmptyTest(request.Model, "gpt"), ResponseID: "resp-1", FinishReason: "stop"}, nil
}

func (h *coverageEngineHost) Command(_ context.Context, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	h.commandCalls = append(h.commandCalls, request)
	return domain.LoaderCommandResult{Stdout: "command-output", Output: "command-output", ExitCode: 0, Success: true, SessionID: "command-session", CellID: "command-cell", Artifacts: map[string]string{"stdout": "/tmp/stdout.txt"}}, nil
}

func (h *coverageEngineHost) StateGet(_ context.Context, key string) (string, bool, error) {
	value, ok := h.state[key]
	return value, ok, nil
}

func (h *coverageEngineHost) StateSet(_ context.Context, key, value string) error {
	if h.state == nil {
		h.state = map[string]string{}
	}
	h.state[key] = value
	h.setValues = append(h.setValues, value)
	return nil
}

func (h *coverageEngineHost) StateDelete(_ context.Context, key string) error {
	delete(h.state, key)
	h.deleted = append(h.deleted, key)
	return nil
}

func (h *coverageEngineHost) CallSessionRPC(_ context.Context, method, requestJSON string) (string, error) {
	if h.requests == nil {
		h.requests = map[string]map[string]any{}
	}
	h.sessionCalls = append(h.sessionCalls, method)
	if strings.TrimSpace(requestJSON) != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(requestJSON), &payload); err != nil {
			return "", err
		}
		h.requests[method] = payload
	}
	const sessionID = "session-from-host"
	switch method {
	case "CreateSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","vmStatus":"RUNNING"}}}`, nil
	case "GetSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","vmStatus":"RUNNING"}}}`, nil
	case "ListSessions":
		return `{"sessions":[{"sessionId":"` + sessionID + `","vmStatus":"RUNNING"}]}`, nil
	case "GetSessionProxy":
		return `{"sessionId":"` + sessionID + `","proxyPath":"/agent-compose/session/` + sessionID + `/lab","notebookUrl":"/agent-compose/session/` + sessionID + `/lab?token=t","driver":"boxlite","vmStatus":"RUNNING"}`, nil
	case "StopSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","vmStatus":"STOPPED"}}}`, nil
	case "ResumeSession":
		return `{"session":{"summary":{"sessionId":"` + sessionID + `","vmStatus":"RUNNING"}}}`, nil
	default:
		return `{}`, nil
	}
}

func (h *coverageEngineHost) PublishEvent(_ context.Context, topic, payloadJSON string) (domain.TopicEventRecord, error) {
	h.published = append(h.published, topic+" "+payloadJSON)
	return domain.TopicEventRecord{ID: "evt-test", Sequence: 1, Topic: topic, CorrelationID: "corr-test"}, nil
}

func TestQJSLoaderEngineBindingCoverageWorkflow(t *testing.T) {
	engine := &QJSLoaderEngine{}
	if built, err := NewLoaderEngine(nil); err != nil || built == nil {
		t.Fatalf("NewLoaderEngine built=%#v err=%v", built, err)
	}
	if got := EngineMaxExecutionTime(context.Background()); got <= 0 {
		t.Fatalf("EngineMaxExecutionTime without deadline = %d", got)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := EngineMaxExecutionTime(expired); got != 1 {
		t.Fatalf("EngineMaxExecutionTime expired = %d, want 1", got)
	}

	host := &coverageEngineHost{state: map[string]string{"existing": `{"value":1}`}}
	result, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime:     domain.LoaderRuntimeScheduler,
		PayloadJSON: `{"input":true}`,
		Script: `
const interval = scheduler.interval(function heartbeat() {}, 2500, "interval-auto");
clearInterval(interval);
scheduler.timeout("timeout-id", 3500, function secondTimeout() {});
scheduler.cron("cron-id", "*/5 * * * *", function cronHandler(event) { return { cron: event.input }; }, { id: "cron-id", timezone: "UTC" });
scheduler.on("runtime.test.*", function onEvent(event) { return { event }; }, "event-id");

function main(payload) {
  scheduler.log("coverage", { payload });
  const created = scheduler.session.createSession({ title: "alpha" });
  const sessionId = created.session.summary.sessionId;
  const current = scheduler.session.getSession({ sessionId });
  const sessions = scheduler.session.listSessions();
  const proxy = scheduler.session.getSessionProxy({ sessionId });
  const stopped = scheduler.session.stopSession({ sessionId });
  const resumed = scheduler.session.ResumeSession({ sessionId });
  const RiskSummary = scheduler.z.object({ summary: scheduler.z.string(), risk: scheduler.z.enum(["low", "high"]) });
  const agent = scheduler.agent("summarize", {
    agent: "claude", sessionPolicy: "new", timeout: "45s", title: "Loader Agent Session",
    driver: "microsandbox", guestImage: "guest:latest", workspaceId: "workspace-1",
    sessionEnv: { REQUEST_ONLY: "request" },
    volumes: ["cache:/cache", { type: "bind", source: "./fixtures", target: "/fixtures", readOnly: true }],
    outputSchema: RiskSummary
  });
  const llm = scheduler.llm("answer", { model: "gpt", outputSchema: RiskSummary });
  const execResult = scheduler.exec({ command: "python3", args: ["-V"], cwd: "/tmp", env: { FOO: "bar" }, timeoutMs: 30000, maxOutputBytes: 128, sessionPolicy: "new", volumes: ["./bin:/host-bin:ro"] });
  const shellResult = scheduler.shell("echo hello", { cwd: "/tmp", env: { SHELL_FOO: "baz" }, maxOutputBytes: 64, volumes: [{ source: "shell-cache", target: "/shell-cache" }] });
  scheduler.state.set("nil", null);
  scheduler.state.set("bool", true);
  scheduler.state.set("number", 42);
  scheduler.state.set("nan", NaN);
  scheduler.state.set("inf", Infinity);
  scheduler.state.set("object", { nested: [1, "two"] });
  const existing = scheduler.state.get("existing");
  scheduler.state.delete("existing");
  const published = scheduler.event.publish("runtime.test.requested", { value: 1 });
  return { current, sessions, proxy, stopped, resumed, agent, llm, execResult, shellResult, existing, published, runtime: scheduler.runtime.name };
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(result.Triggers) != 3 || len(host.sessionCalls) != 6 || len(host.agentCalls) != 1 || len(host.llmCalls) != 1 || len(host.commandCalls) != 2 || len(host.published) != 1 {
		t.Fatalf("unexpected host calls result=%#v sessions=%#v agent=%d llm=%d commands=%d published=%#v", result.Triggers, host.sessionCalls, len(host.agentCalls), len(host.llmCalls), len(host.commandCalls), host.published)
	}
	if host.agentCalls[0].Driver != driverpkg.RuntimeDriverMicrosandbox || host.commandCalls[0].Mode != "exec" || host.commandCalls[1].Mode != "shell" {
		t.Fatalf("unexpected request mappings: agent=%#v commands=%#v", host.agentCalls[0], host.commandCalls)
	}
	if len(host.agentCalls[0].Volumes) != 2 ||
		host.agentCalls[0].Volumes[0].Type != domain.VolumeMountTypeVolume ||
		host.agentCalls[0].Volumes[1].Type != domain.VolumeMountTypeBind ||
		!host.agentCalls[0].Volumes[1].ReadOnly {
		t.Fatalf("unexpected agent volumes: %#v", host.agentCalls[0].Volumes)
	}
	if len(host.commandCalls[0].Volumes) != 1 ||
		host.commandCalls[0].Volumes[0].Type != domain.VolumeMountTypeBind ||
		!host.commandCalls[0].Volumes[0].ReadOnly ||
		len(host.commandCalls[1].Volumes) != 1 ||
		host.commandCalls[1].Volumes[0].Type != domain.VolumeMountTypeVolume {
		t.Fatalf("unexpected command volumes: exec=%#v shell=%#v", host.commandCalls[0].Volumes, host.commandCalls[1].Volumes)
	}
	if !strings.Contains(result.ResultJSON, `"runtime":"scheduler"`) || !strings.Contains(result.ResultJSON, `"eventId":"evt-test"`) {
		t.Fatalf("result json = %s", result.ResultJSON)
	}

	triggerResult, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime:     domain.LoaderRuntimeScheduler,
		PayloadJSON: `{"input":true}`,
		Trigger:     &domain.LoaderTrigger{ID: "cron-id"},
		Script:      `scheduler.cron("cron-id", "*/5 * * * *", function cronHandler(event) { return { cron: event.input }; }, { id: "cron-id" });`,
	}, host)
	if err != nil || triggerResult.ResultJSON != `{"cron":true}` {
		t.Fatalf("trigger result=%#v err=%v", triggerResult, err)
	}

	singleTriggerResult, err := engine.Execute(context.Background(), LoaderExecutionRequest{
		Runtime:     domain.LoaderRuntimeScheduler,
		PayloadJSON: `{"value":3}`,
		Script:      `scheduler.on("runtime.single", function only(payload) { return { single: payload.value }; });`,
	}, host)
	if err != nil || singleTriggerResult.ResultJSON != `{"single":3}` {
		t.Fatalf("single trigger result=%#v err=%v", singleTriggerResult, err)
	}

	validation, err := engine.Validate(context.Background(), domain.LoaderRuntimeScheduler, `
const interval = scheduler.setInterval(1000, function fast() {}, "interval-alt");
scheduler.clearInterval("missing");
const timeout = setTimeout(function later() {}, 2000, "timeout-alt");
clearTimeout(timeout);
scheduler.schedule("cron-alt", "*/10 * * * *", function cron() {}, { timezone: "UTC" });
scheduler.addEventListener("runtime.alt", "event-alt", function event() {});
`)
	if err != nil || len(validation.Triggers) != 3 {
		t.Fatalf("alternate validation=%#v err=%v", validation, err)
	}
	if validation.Triggers[0].ID != "interval-alt" || validation.Triggers[1].ID != "cron-alt" || validation.Triggers[2].ID != "event-alt" {
		t.Fatalf("alternate trigger order = %#v", validation.Triggers)
	}

	warningOnly, err := engine.Validate(context.Background(), domain.LoaderRuntimeScheduler, `const value = 1;`)
	if err != nil || len(warningOnly.Warnings) != 1 {
		t.Fatalf("warning validation=%#v err=%v", warningOnly, err)
	}
}

func TestIntegrationQJSLoaderEngineBindingCoverageWorkflow(t *testing.T) {
	TestQJSLoaderEngineBindingCoverageWorkflow(t)
}

func TestE2EQJSLoaderEngineBindingCoverageWorkflow(t *testing.T) {
	TestQJSLoaderEngineBindingCoverageWorkflow(t)
}

func TestQJSLoaderEngineValidationCoverageWorkflow(t *testing.T) {
	engine := &QJSLoaderEngine{}
	if _, err := engine.Validate(context.Background(), "unknown", `function main(){}`); err == nil || !strings.Contains(err.Error(), "unsupported loader runtime") {
		t.Fatalf("unsupported runtime error = %v", err)
	}
	if _, err := engine.Validate(context.Background(), domain.LoaderRuntimeScheduler, " "); err == nil || !strings.Contains(err.Error(), "loader script is required") {
		t.Fatalf("blank script error = %v", err)
	}
	tests := []struct {
		script  string
		wantErr string
	}{
		{`scheduler.exec("python3")`, "scheduler.exec is unavailable during validation"},
		{`scheduler.shell("echo hello")`, "scheduler.shell is unavailable during validation"},
		{`scheduler.event.publish("runtime.test", {})`, "scheduler.event.publish is unavailable during validation"},
		{`scheduler.agent("summarize")`, "scheduler.agent is unavailable during validation"},
		{`scheduler.llm("answer")`, "scheduler.llm is unavailable during validation"},
		{`scheduler.session.getSession({ sessionId: "session-1" })`, "scheduler.session.getSession is unavailable during validation"},
		{`scheduler.cron("*/5 * * * *", function cron() {}, { id: "a" }, { id: "b" });`, "at most one options"},
		{`scheduler.cron("first", "*/5 * * * *", function cron() {}, { id: "second" });`, "multiple trigger ids"},
		{`scheduler.cron("", function cron() {});`, "non-empty expression"},
		{`scheduler.cron(123, function cron() {});`, "unsupported scheduler.cron signature"},
		{`scheduler.on("", function onEvent() {});`, "non-empty topic"},
		{`scheduler.on("runtime.test", "not-a-function");`, "unsupported scheduler.on signature"},
		{`scheduler.interval(function interval() {});`, "requires a callback and interval"},
		{`scheduler.interval("bad");`, "requires a callback and interval"},
		{`scheduler.timeout(function timeout() {}, 0);`, "positive delay"},
		{`scheduler.timeout("dup", function one() {}, 1000); scheduler.timeout("dup", function two() {}, 2000);`, "duplicate loader trigger id"},
	}
	for _, tt := range tests {
		_, err := engine.Validate(context.Background(), domain.LoaderRuntimeScheduler, tt.script)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Fatalf("Validate(%q) error = %v, want %q", tt.script, err, tt.wantErr)
		}
	}

	execTests := []struct {
		script  string
		wantErr string
	}{
		{`function main() { scheduler.log(); }`, "scheduler.log requires a message"},
		{`function main() { scheduler.log(" "); }`, "scheduler.log requires a non-empty message"},
		{`function main() { return scheduler.event.publish("", {}); }`, "non-empty topic"},
		{`function main() { return scheduler.event.publish("runtime.test", []); }`, "payload must be an object"},
		{`function main() { return scheduler.agent(""); }`, "scheduler.agent requires a non-empty prompt"},
		{`function main() { return scheduler.agent("summarize", { sessionEnv: "bad" }); }`, "decode scheduler.agent sessionEnv"},
		{`function main() { return scheduler.agent("summarize", { volumes: "bad" }); }`, "decode scheduler.agent volumes"},
		{`function main() { return scheduler.llm(""); }`, "scheduler.llm requires a non-empty prompt"},
		{`function main() { return scheduler.llm("answer", "bad"); }`, "decode scheduler.llm options"},
		{`function main() { return scheduler.exec("python3"); }`, "scheduler.exec requires a request object"},
		{`function main() { return scheduler.exec({ args: ["-V"] }); }`, "scheduler.exec requires a non-empty command"},
		{`function main() { return scheduler.exec({ command: "python3", args: "bad" }); }`, "decode scheduler.exec args"},
		{`function main() { return scheduler.exec({ command: "python3", env: { A: 1 } }); }`, "decode scheduler.exec env"},
		{`function main() { return scheduler.exec({ command: "python3", timeoutMs: "bad" }); }`, "decode scheduler.exec timeoutMs"},
		{`function main() { return scheduler.exec({ command: "python3", volumes: [":/cache"] }); }`, "decode scheduler.exec volumes"},
		{`function main() { return scheduler.shell(""); }`, "scheduler.shell requires a non-empty script"},
		{`function main() { return scheduler.shell("echo ok", "bad"); }`, "scheduler.shell options must be an object"},
		{`function main() { return scheduler.shell("echo ok", { maxOutputBytes: "bad" }); }`, "decode scheduler.shell maxOutputBytes"},
		{`function main() { return scheduler.shell("echo ok", { volumes: [{ source: "cache" }] }); }`, "decode scheduler.shell volumes"},
		{`function main() { return scheduler.agent("summarize", { timeout: 30000 }); }`, "decode scheduler.agent timeout"},
		{`function main() { return scheduler.state.get(""); }`, "scheduler.state.get requires a non-empty key"},
		{`function main() { return scheduler.state.set("key"); }`, "scheduler.state.set requires a key and value"},
		{`function main() { return scheduler.state.delete(""); }`, "scheduler.state.delete requires a non-empty key"},
		{`function main() { return scheduler.session.getSession({}, {}); }`, "accepts at most one request object"},
		{`scheduler.on("runtime.a", function a() {}); scheduler.on("runtime.b", function b() {});`, "loader defines multiple triggers"},
		{`scheduler.on("runtime.a", function a() {});`, "loader trigger missing not found"},
	}
	for _, tt := range execTests {
		request := LoaderExecutionRequest{Runtime: domain.LoaderRuntimeScheduler, Script: tt.script}
		if strings.Contains(tt.wantErr, "trigger missing") {
			request.Trigger = &domain.LoaderTrigger{ID: "missing"}
		}
		_, err := engine.Execute(context.Background(), request, &coverageEngineHost{})
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Fatalf("Execute(%q) error = %v, want %q", tt.script, err, tt.wantErr)
		}
	}
	if _, err := engine.Execute(context.Background(), LoaderExecutionRequest{Runtime: domain.LoaderRuntimeScheduler, Script: `function main(){}`}, nil); err == nil || !strings.Contains(err.Error(), "loader host is required") {
		t.Fatalf("nil host error = %v", err)
	}
	if _, err := engine.Execute(context.Background(), LoaderExecutionRequest{Runtime: domain.LoaderRuntimeScheduler, Script: `function main(){}`, PayloadJSON: `{bad json`}, &coverageEngineHost{}); err == nil {
		t.Fatalf("invalid payload JSON returned nil error")
	}
}

func TestIntegrationQJSLoaderEngineValidationCoverageWorkflow(t *testing.T) {
	TestQJSLoaderEngineValidationCoverageWorkflow(t)
}

func TestE2EQJSLoaderEngineValidationCoverageWorkflow(t *testing.T) {
	TestQJSLoaderEngineValidationCoverageWorkflow(t)
}

func TestLoaderSessionEnvDecodingEdgeBranches(t *testing.T) {
	items, err := loaderSessionEnvItems(map[string]any{
		" BOOL ":         true,
		"FLOAT":          float64(12.50),
		"OPENAI_API_KEY": map[string]any{"value": "secret", "secret": false},
		"NUMBER_SECRET":  map[string]any{"value": float64(7), "secret": float64(1)},
		"STRING_SECRET":  map[string]any{"value": "x", "secret": "true"},
		" ":              "ignored",
	})
	if err != nil {
		t.Fatalf("loaderSessionEnvItems map returned error: %v", err)
	}
	env := domain.SessionEnvMap(items)
	if env["BOOL"] != "true" || env["FLOAT"] != "12.5" || env["OPENAI_API_KEY"] != "secret" || env["NUMBER_SECRET"] != "7" {
		t.Fatalf("env map = %#v items=%#v", env, items)
	}
	if secret := findEnvSecretForTest(items, "OPENAI_API_KEY"); secret {
		t.Fatalf("OPENAI_API_KEY explicit secret=false was not honored: %#v", items)
	}
	if !findEnvSecretForTest(items, "NUMBER_SECRET") || !findEnvSecretForTest(items, "STRING_SECRET") {
		t.Fatalf("secret flags were not decoded: %#v", items)
	}

	arrayItems, err := loaderSessionEnvItems([]any{
		map[string]any{"name": "A", "value": nil},
		map[string]any{"name": "B", "value": false, "secret": "false"},
		map[string]any{"name": "C_TOKEN", "value": map[string]any{"value": "nested"}},
	})
	if err != nil {
		t.Fatalf("loaderSessionEnvItems array returned error: %v", err)
	}
	arrayEnv := domain.SessionEnvMap(arrayItems)
	if arrayEnv["A"] != "" || arrayEnv["B"] != "false" || arrayEnv["C_TOKEN"] != "nested" || !findEnvSecretForTest(arrayItems, "C_TOKEN") {
		t.Fatalf("array env = %#v items=%#v", arrayEnv, arrayItems)
	}

	errorCases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "bad container", value: "bad", want: "object map or array"},
		{name: "bad item", value: []any{"bad"}, want: "item 0 must be an object"},
		{name: "missing name", value: []any{map[string]any{"value": "x"}}, want: "requires a non-empty name"},
		{name: "bad item secret", value: []any{map[string]any{"name": "A", "secret": []any{}}}, want: "secret must be a boolean"},
		{name: "bad nested secret", value: map[string]any{"A": map[string]any{"value": "x", "secret": []any{}}}, want: "secret must be a boolean"},
	}
	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loaderSessionEnvItems(tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loaderSessionEnvItems(%#v) error = %v, want %q", tc.value, err, tc.want)
			}
		})
	}

	if secret := loaderSecretEnvName("plain"); secret {
		t.Fatalf("plain env name should not be secret")
	}
	for _, name := range []string{"password", "ACCESS_TOKEN", "CLIENT_SECRET", "API_KEY", "LLM_API_KEY"} {
		if !loaderSecretEnvName(name) {
			t.Fatalf("loaderSecretEnvName(%q) = false", name)
		}
	}
}

func findEnvSecretForTest(items []domain.SessionEnvVar, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return item.Secret
		}
	}
	return false
}

func firstNonEmptyTest(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
