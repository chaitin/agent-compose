package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMinimalSpec(t *testing.T) {
	spec, err := Parse([]byte(`
name: minimal
agents:
  reviewer:
    provider: codex
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if spec.Name != "minimal" {
		t.Fatalf("Name = %q, want minimal", spec.Name)
	}
	agent, ok := spec.Agents["reviewer"]
	if !ok {
		t.Fatalf("missing reviewer agent: %#v", spec.Agents)
	}
	if agent.Provider != "codex" {
		t.Fatalf("agent provider = %q, want codex", agent.Provider)
	}
}

func TestParseAgentStatus(t *testing.T) {
	spec, err := Parse([]byte("name: status\nagents:\n  worker:\n    status: disabled\n    provider: codex\n"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if got := normalized.Agents[0].Status; got != "disabled" {
		t.Fatalf("status = %q, want disabled", got)
	}
	spec.Agents["worker"] = AgentSpec{Status: "paused", Provider: "codex"}
	if _, err := Normalize(spec, NormalizeOptions{}); err == nil || !strings.Contains(err.Error(), "must be enabled or disabled") {
		t.Fatalf("invalid status error = %v", err)
	}
}

func TestParseFullSpec(t *testing.T) {
	spec, err := Parse([]byte(`
name: review-project
variables:
  OPENAI_API_KEY:
    value: ${OPENAI_API_KEY}
    secret: true
workspaces:
  repo:
    provider: git
    url: https://github.com/org/repo.git
    branch: main
agents:
  reviewer:
    provider: codex
    model: gpt-5
    system_prompt: Review carefully.
    image: ghcr.io/org/agent-runtime:latest
    driver:
      boxlite:
        kernel: s3://bucket/kernel
    env:
      REVIEW_MODE: strict
    workspace:
      provider: local
      path: ./repo
    jupyter:
      enabled: true
      guest_port: 8888
    scheduler:
      enabled: true
      triggers:
        - cron: "0 * * * *"
          prompt: "Review the latest workspace state."
        - event:
            topic: git.push
          prompt: "Review changes from the incoming event."
network:
  mode: default
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got := spec.Variables["OPENAI_API_KEY"]; got.Value != "${OPENAI_API_KEY}" || !got.Secret {
		t.Fatalf("OPENAI_API_KEY = %#v", got)
	}
	if spec.Workspaces["repo"].Provider != "git" || spec.Workspaces["repo"].Branch != "main" {
		t.Fatalf("workspaces = %#v", spec.Workspaces)
	}
	agent := spec.Agents["reviewer"]
	if agent.Driver == nil || agent.Driver.Boxlite == nil || agent.Driver.Boxlite.Kernel != "s3://bucket/kernel" {
		t.Fatalf("driver = %#v", agent.Driver)
	}
	if got := agent.Env["REVIEW_MODE"].Value; got != "strict" {
		t.Fatalf("REVIEW_MODE = %q, want strict", got)
	}
	if agent.Jupyter == nil || !agent.Jupyter.Enabled || agent.Jupyter.GuestPort != 8888 {
		t.Fatalf("jupyter = %#v, want enabled guest port 8888", agent.Jupyter)
	}
	if agent.Scheduler == nil || agent.Scheduler.Enabled == nil || !*agent.Scheduler.Enabled {
		t.Fatalf("scheduler enabled = %#v", agent.Scheduler)
	}
	if got := len(agent.Scheduler.Triggers); got != 2 {
		t.Fatalf("trigger count = %d, want 2", got)
	}
	if agent.Scheduler.Triggers[1].Event == nil || agent.Scheduler.Triggers[1].Event.Topic != "git.push" {
		t.Fatalf("event trigger = %#v", agent.Scheduler.Triggers[1])
	}
	if spec.Network == nil || spec.Network.Mode != "default" {
		t.Fatalf("network = %#v", spec.Network)
	}
}

func TestParseSchedulerScript(t *testing.T) {
	spec, err := Parse([]byte(`
name: qjs-project
agents:
  reviewer:
    provider: codex
    scheduler:
      enabled: true
      script: |
        scheduler.interval("hourly-review", function hourlyReview() {
          return scheduler.agent("Review the latest workspace state.");
        }, 3600000);
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	agent := spec.Agents["reviewer"]
	if agent.Scheduler == nil {
		t.Fatalf("scheduler is nil")
	}
	if !strings.Contains(agent.Scheduler.Script.Inline, `scheduler.interval("hourly-review"`) {
		t.Fatalf("scheduler script = %q, want inline qjs", agent.Scheduler.Script)
	}
	if got := len(agent.Scheduler.Triggers); got != 0 {
		t.Fatalf("trigger count = %d, want 0", got)
	}
}

func TestParseSchedulerScriptURL(t *testing.T) {
	spec, err := Parse([]byte(`
name: url-project
agents:
  reviewer:
    scheduler:
      script:
        url: ./scripts/scheduler.js
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	source := spec.Agents["reviewer"].Scheduler.Script
	if source.Inline != "" || source.URL != "./scripts/scheduler.js" {
		t.Fatalf("scheduler script source = %#v", source)
	}
}

func TestParseRejectsInvalidSchedulerScriptURLObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "unknown field", body: "url: ./scheduler.js\n        extra: true", want: "scheduler.script.extra"},
		{name: "empty URL", body: `url: ""`, want: "scheduler.script.url"},
		{name: "missing URL", body: `{}`, want: "scheduler.script.url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("agents:\n  reviewer:\n    scheduler:\n      script:\n        " + tc.body + "\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse error = %v, want path %q", err, tc.want)
			}
		})
	}
}

func TestParseUnknownFieldIncludesPath(t *testing.T) {
	_, err := Parse([]byte(`
name: unknown-field
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - event:
            topic: git.push
            extra: bad
`))
	if err == nil {
		t.Fatalf("expected Parse to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.scheduler.triggers[0].event.extra") {
		t.Fatalf("error = %q, want field path", got)
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte("name: [broken\n"))
	if err == nil {
		t.Fatalf("expected Parse to fail")
	}
	if got := err.Error(); !strings.Contains(got, "parse compose") || !strings.Contains(got, "line") {
		t.Fatalf("error = %q, want parse context", got)
	}
}

func TestParseTypeErrorIncludesPath(t *testing.T) {
	_, err := Parse([]byte(`
agents:
  reviewer:
    scheduler:
      enabled:
        nested: true
`))
	if err == nil {
		t.Fatalf("expected Parse to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.scheduler.enabled") {
		t.Fatalf("error = %q, want field path", got)
	}
}

func TestParseSchedulerScriptTypeErrorIncludesPath(t *testing.T) {
	_, err := Parse([]byte(`
agents:
  reviewer:
    scheduler:
      script:
        nested: true
`))
	if err == nil {
		t.Fatalf("expected Parse to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.scheduler.script") {
		t.Fatalf("error = %q, want field path", got)
	}
}

func TestParseRejectsUnsupportedJupyterExposeFields(t *testing.T) {
	_, err := Parse([]byte(`
name: invalid-jupyter
agents:
  reviewer:
    jupyter:
      enabled: true
      host_port: 18088
`))
	if err == nil {
		t.Fatalf("expected Parse to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.jupyter.host_port") || !strings.Contains(got, "unknown field") {
		t.Fatalf("error = %q, want unsupported jupyter field path", got)
	}
}

func TestParseRejectsInvalidJupyterGuestPortType(t *testing.T) {
	_, err := Parse([]byte(`
name: invalid-jupyter
agents:
  reviewer:
    jupyter:
      guest_port: soon
`))
	if err == nil {
		t.Fatalf("expected Parse to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.jupyter.guest_port") || !strings.Contains(got, "expected int") {
		t.Fatalf("error = %q, want jupyter guest_port type path", got)
	}
}

func TestParseFileIncludesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-compose.yml")
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	_, err := ParseFile(path)
	if err == nil {
		t.Fatalf("expected ParseFile to fail")
	}
	if got := err.Error(); !strings.Contains(got, path) || !strings.Contains(got, "unknown") {
		t.Fatalf("error = %q, want file path and field path", got)
	}
}
