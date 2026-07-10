package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeDefaultsProjectNameFromComposeDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "review-project")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	path := filepath.Join(dir, "agent-compose.yml")
	if err := os.WriteFile(path, []byte(`
workspaces:
  default:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
`), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	normalized, err := NormalizeFile(path)
	if err != nil {
		t.Fatalf("NormalizeFile returned error: %v", err)
	}
	if normalized.Name != "review-project" {
		t.Fatalf("Name = %q, want review-project", normalized.Name)
	}
}

func TestNormalizeDefaultsProjectNameFromRelativeComposePath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "relative-project")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	path := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(path, []byte(`
workspaces:
  default:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
`), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})

	normalized, err := NormalizeFile(filepath.Join("relative-project", "custom.yml"))
	if err != nil {
		t.Fatalf("NormalizeFile returned error: %v", err)
	}
	if normalized.Name != "relative-project" {
		t.Fatalf("Name = %q, want relative-project", normalized.Name)
	}
}

func TestNormalizeExplicitProjectNameWinsOverDirectory(t *testing.T) {
	spec := mustParseCompose(t, `
name: explicit-project
agents:
  reviewer:
    provider: codex
`)

	normalized, err := Normalize(spec, NormalizeOptions{ProjectDir: "/tmp/other-project"})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Name != "explicit-project" {
		t.Fatalf("Name = %q, want explicit-project", normalized.Name)
	}
}

func TestNormalizeRequiresProjectNameWithoutDefaultPath(t *testing.T) {
	spec := mustParseCompose(t, `
agents:
  reviewer:
    provider: codex
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "field name") {
		t.Fatalf("error = %q, want project name field path", got)
	}
}

func TestNormalizeSortsAgentsForStableOutput(t *testing.T) {
	spec := &ProjectSpec{
		Name:       "stable",
		Workspaces: map[string]WorkspaceSpec{"default": {Provider: "local", Path: "."}},
		Agents: map[string]AgentSpec{
			"worker":   {Provider: "codex"},
			"reviewer": {Provider: "codex"},
		},
	}

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if got := []string{normalized.Agents[0].Name, normalized.Agents[1].Name}; got[0] != "reviewer" || got[1] != "worker" {
		t.Fatalf("agent order = %#v, want reviewer, worker", got)
	}
}

func TestNormalizeBuildSpec(t *testing.T) {
	spec := mustParseCompose(t, `
name: build-project
agents:
  reviewer:
    provider: codex
    image: reviewer:dev
    build:
      context: agent
      dockerfile: Dockerfile.agent
      target: runtime
      args:
        NODE_ENV: development
      platforms:
        - linux/amd64
      tags:
        - reviewer:latest
      no_cache: true
      pull: true
  worker:
    provider: codex
    build: .
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	reviewer := normalized.Agents[0]
	if reviewer.Build == nil || reviewer.Build.Context != "agent" || reviewer.Build.Dockerfile != "Dockerfile.agent" || reviewer.Build.Target != "runtime" {
		t.Fatalf("reviewer build = %#v", reviewer.Build)
	}
	if reviewer.Build.Args["NODE_ENV"] != "development" || reviewer.Build.Platforms[0] != "linux/amd64" || reviewer.Build.Tags[0] != "reviewer:latest" || !reviewer.Build.NoCache || !reviewer.Build.Pull {
		t.Fatalf("reviewer build fields = %#v", reviewer.Build)
	}
	worker := normalized.Agents[1]
	if worker.Build == nil || worker.Build.Context != "." || worker.Build.Dockerfile != "Dockerfile" {
		t.Fatalf("worker build = %#v", worker.Build)
	}
}

func TestNormalizeBuildRejectsMultiplePlatforms(t *testing.T) {
	spec := mustParseCompose(t, `
name: build-project
agents:
  reviewer:
    provider: codex
    image: reviewer:dev
    build:
      context: .
      platforms:
        - linux/amd64
        - linux/arm64
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "multiple build platforms") {
		t.Fatalf("error = %q, want multiple build platforms", got)
	}
}

func TestNormalizeAgentCapsetIDs(t *testing.T) {
	spec := mustParseCompose(t, `
name: capsets
agents:
  reviewer:
    provider: codex
    capset_ids:
      - xray-dev
      - xray-dev
      - " data "
      - ""
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	got := normalized.Agents[0].CapsetIDs
	want := []string{"xray-dev", "data"}
	if len(got) != len(want) {
		t.Fatalf("capset ids = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("capset ids = %#v, want %#v", got, want)
		}
	}
}

func TestNormalizeProjectVolumesAndAgentMounts(t *testing.T) {
	raw := []byte(`
name: volume-demo
volumes:
  cache:
    driver: local
    labels:
      purpose: cache
agents:
  reviewer:
    provider: codex
    image: reviewer:latest
    volumes:
      - cache:/cache
      - ./fixtures:/fixtures:ro
      - type: bind
        source: /tmp/data
        target: /host-data
`)
	spec := mustParseCompose(t, string(raw))
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Volumes["cache"].Driver != "local" || normalized.Volumes["cache"].Labels["purpose"] != "cache" {
		t.Fatalf("volume = %#v", normalized.Volumes["cache"])
	}
	if len(normalized.Agents) != 1 || len(normalized.Agents[0].Volumes) != 3 {
		t.Fatalf("agent volumes = %#v", normalized.Agents)
	}
	if normalized.Agents[0].Volumes[0].Type != "volume" || normalized.Agents[0].Volumes[1].Type != "bind" || !normalized.Agents[0].Volumes[1].ReadOnly {
		t.Fatalf("normalized mounts = %#v", normalized.Agents[0].Volumes)
	}
	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if !strings.Contains(string(data), `"volumes"`) || !strings.Contains(string(data), `"cache"`) {
		t.Fatalf("canonical JSON missing volumes: %s", data)
	}
}

func TestNormalizePreservesValidAgentNames(t *testing.T) {
	spec := &ProjectSpec{
		Name:       "valid-agents",
		Workspaces: map[string]WorkspaceSpec{"default": {Provider: "local", Path: "."}},
		Agents: map[string]AgentSpec{
			"a1":          {Provider: "codex"},
			"agent_1":     {Provider: "codex"},
			"code-review": {Provider: "codex"},
		},
	}

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	got := []string{normalized.Agents[0].Name, normalized.Agents[1].Name, normalized.Agents[2].Name}
	want := []string{"a1", "agent_1", "code-review"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agent names = %#v, want %#v", got, want)
		}
	}
}

func TestNormalizeRejectsInvalidAgentName(t *testing.T) {
	tests := []string{
		"Review",
		"review.agent",
		"review agent",
		"review/agent",
		"-reviewer",
		"_reviewer",
		"1reviewer",
		"审查",
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			spec := &ProjectSpec{
				Name:       "invalid-agent",
				Workspaces: map[string]WorkspaceSpec{"default": {Provider: "local", Path: "."}},
				Agents: map[string]AgentSpec{
					name: {Provider: "codex"},
				},
			}

			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, "agents."+name) {
				t.Fatalf("error = %q, want agent field path", got)
			}
		})
	}
}

func TestNormalizeDefaultsDriverAndNetwork(t *testing.T) {
	spec := mustParseCompose(t, `
name: defaults
agents:
  reviewer:
    provider: codex
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Network == nil || normalized.Network.Mode != "default" {
		t.Fatalf("network = %#v, want default", normalized.Network)
	}
	if got := normalized.Agents[0].Driver; got == nil || got.Name != DriverDocker || got.Docker == nil {
		t.Fatalf("driver = %#v, want default docker", got)
	}
}

func TestNormalizeInterpolatesAgentModelFromEnvironment(t *testing.T) {
	spec := mustParseCompose(t, `
name: model-env
agents:
  reviewer:
    provider: claude
    model: ${ANTHROPIC_MODEL}
`)

	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{"ANTHROPIC_MODEL": "kimi-k2.6"}})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if got := normalized.Agents[0].Model; got != "kimi-k2.6" {
		t.Fatalf("agent model = %q, want kimi-k2.6", got)
	}
}

func TestNormalizeRequiresAgentModelEnvironmentReference(t *testing.T) {
	spec := mustParseCompose(t, `
name: model-env
agents:
  reviewer:
    provider: claude
    model: ${ANTHROPIC_MODEL}
`)

	_, err := Normalize(spec, NormalizeOptions{Env: map[string]string{}})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.model") || !strings.Contains(got, "ANTHROPIC_MODEL") {
		t.Fatalf("error = %q, want model env reference path", got)
	}
}

func TestNormalizeRejectsEmptyDriver(t *testing.T) {
	spec := mustParseCompose(t, `
name: invalid-driver
agents:
  reviewer:
    driver: {}
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.driver") || !strings.Contains(got, "exactly one runtime") {
		t.Fatalf("error = %q, want driver one-of error", got)
	}
}

func TestNormalizeRejectsMultipleDrivers(t *testing.T) {
	spec := mustParseCompose(t, `
name: multi-driver
agents:
  reviewer:
    driver:
      boxlite: {}
      docker: {}
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.driver") || !strings.Contains(got, "boxlite, docker") {
		t.Fatalf("error = %q, want multiple driver error", got)
	}
}

func TestNormalizeRejectsFirecrackerDriver(t *testing.T) {
	spec := mustParseCompose(t, `
name: firecracker-driver
agents:
  reviewer:
    driver:
      firecracker:
        kernel: vmlinux
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.driver.firecracker") || !strings.Contains(got, "unsupported") {
		t.Fatalf("error = %q, want firecracker unsupported error", got)
	}
}

func TestNormalizeAcceptsSupportedDriverAndDefaultNetwork(t *testing.T) {
	spec := mustParseCompose(t, `
name: supported-driver
network:
  mode: default
agents:
  reviewer:
    driver:
      microsandbox:
        profile: secure
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Network == nil || normalized.Network.Mode != "default" {
		t.Fatalf("network = %#v, want default", normalized.Network)
	}
	if got := normalized.Agents[0].Driver; got == nil || got.Name != DriverMicrosandbox || got.Microsandbox.Profile != "secure" {
		t.Fatalf("driver = %#v", got)
	}
}

func TestNormalizeAcceptsEmptyNetworkAsDefault(t *testing.T) {
	spec := mustParseCompose(t, `
name: empty-network
network: {}
agents:
  reviewer:
    provider: codex
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Network == nil || normalized.Network.Mode != "default" {
		t.Fatalf("network = %#v, want default", normalized.Network)
	}
}

func TestNormalizePreservesJupyterConfig(t *testing.T) {
	spec := mustParseCompose(t, `
name: jupyter
agents:
  reviewer:
    provider: codex
    jupyter:
      enabled: true
      guest_port: 8888
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	jupyter := normalized.Agents[0].Jupyter
	if jupyter == nil || !jupyter.Enabled || jupyter.GuestPort != 8888 {
		t.Fatalf("jupyter = %#v, want enabled guest port 8888", jupyter)
	}
}

func TestNormalizeDropsDefaultJupyterConfig(t *testing.T) {
	spec := mustParseCompose(t, `
name: jupyter-default
agents:
  reviewer:
    provider: codex
    jupyter: {}
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Agents[0].Jupyter != nil {
		t.Fatalf("jupyter = %#v, want nil for default disabled config", normalized.Agents[0].Jupyter)
	}
}

func TestNormalizeRejectsInvalidJupyterGuestPort(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{name: "negative", port: -1},
		{name: "too high", port: 65536},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &ProjectSpec{
				Name:       "invalid-jupyter",
				Workspaces: map[string]WorkspaceSpec{"default": {Provider: "local", Path: "."}},
				Agents: map[string]AgentSpec{
					"reviewer": {
						Provider: "codex",
						Jupyter:  &JupyterSpec{Enabled: true, GuestPort: tt.port},
					},
				},
			}

			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, "agents.reviewer.jupyter.guest_port") {
				t.Fatalf("error = %q, want jupyter guest_port path", got)
			}
		})
	}
}

func TestNormalizeRejectsUnsupportedNetwork(t *testing.T) {
	spec := mustParseCompose(t, `
name: unsupported-network
network:
  mode: bridge
agents:
  reviewer:
    provider: codex
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "network.mode") || !strings.Contains(got, "unsupported") {
		t.Fatalf("error = %q, want network mode error", got)
	}
}

func TestNormalizeRejectsInvalidTrigger(t *testing.T) {
	spec := mustParseCompose(t, `
name: invalid-trigger
agents:
  reviewer:
    scheduler:
      triggers:
        - cron: "0 * * * *"
          interval: 1m
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.scheduler.triggers[0]") || !strings.Contains(got, "exactly one kind") {
		t.Fatalf("error = %q, want trigger one-of error", got)
	}
}

func TestNormalizePreservesSchedulerScript(t *testing.T) {
	spec := mustParseCompose(t, `
name: inline-script
agents:
  reviewer:
    scheduler:
      script: |
        scheduler.interval("hourly-review", "1h", { prompt: "review changes" });
        export async function main(payload) {
          return payload;
        }
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	scheduler := normalized.Agents[0].Scheduler
	if scheduler == nil {
		t.Fatalf("scheduler is nil")
	}
	if !strings.Contains(scheduler.Script, `scheduler.interval("hourly-review"`) {
		t.Fatalf("scheduler script = %q, want inline qjs", scheduler.Script)
	}
	if strings.HasPrefix(scheduler.Script, "\n") || strings.HasSuffix(scheduler.Script, "\n") {
		t.Fatalf("scheduler script = %q, want trimmed script", scheduler.Script)
	}
	if got := len(scheduler.Triggers); got != 0 {
		t.Fatalf("scheduler triggers = %d, want 0", got)
	}
}

func TestNormalizeTreatsBlankSchedulerScriptAsUnset(t *testing.T) {
	spec := mustParseCompose(t, `
name: blank-script
agents:
  reviewer:
    scheduler:
      script: "   "
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	scheduler := normalized.Agents[0].Scheduler
	if scheduler == nil {
		t.Fatalf("scheduler is nil")
	}
	if scheduler.Script != "" {
		t.Fatalf("scheduler script = %q, want empty", scheduler.Script)
	}
	if got := len(scheduler.Triggers); got != 0 {
		t.Fatalf("scheduler triggers = %d, want 0", got)
	}
}

func TestNormalizeRejectsSchedulerScriptWithTriggers(t *testing.T) {
	spec := mustParseCompose(t, `
name: mixed-scheduler
agents:
  reviewer:
    scheduler:
      script: |
        scheduler.interval("hourly-review", "1h");
      triggers:
        - interval: 1h
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.scheduler") || !strings.Contains(got, "script") || !strings.Contains(got, "triggers") {
		t.Fatalf("error = %q, want scheduler script/triggers mutual exclusion error", got)
	}
}

func TestNormalizePreservesSchedulerTriggersWithoutScript(t *testing.T) {
	spec := mustParseCompose(t, `
name: trigger-scheduler
agents:
  reviewer:
    scheduler:
      triggers:
        - name: hourly-review
          interval: 1h
          prompt: review changes
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	scheduler := normalized.Agents[0].Scheduler
	if scheduler == nil {
		t.Fatalf("scheduler is nil")
	}
	if scheduler.Script != "" {
		t.Fatalf("scheduler script = %q, want empty", scheduler.Script)
	}
	if got := len(scheduler.Triggers); got != 1 {
		t.Fatalf("scheduler triggers = %d, want 1", got)
	}
	if trigger := scheduler.Triggers[0]; trigger.Name != "hourly-review" || trigger.Kind != "interval" || trigger.Interval != "1h" || trigger.Prompt != "review changes" {
		t.Fatalf("scheduler trigger = %#v, want normalized interval trigger", trigger)
	}
}

func TestNormalizeRejectsInvalidTriggerPayloads(t *testing.T) {
	tests := []struct {
		name      string
		trigger   string
		wantField string
	}{
		{name: "empty cron", trigger: `cron: ""`, wantField: "triggers[0].cron"},
		{name: "invalid cron", trigger: `cron: "not cron"`, wantField: "triggers[0].cron"},
		{name: "empty interval", trigger: `interval: ""`, wantField: "triggers[0].interval"},
		{name: "invalid interval", trigger: `interval: soon`, wantField: "triggers[0].interval"},
		{name: "zero interval", trigger: `interval: 0s`, wantField: "triggers[0].interval"},
		{name: "negative interval", trigger: `interval: -1s`, wantField: "triggers[0].interval"},
		{name: "empty timeout", trigger: `timeout: ""`, wantField: "triggers[0].timeout"},
		{name: "invalid timeout", trigger: `timeout: soon`, wantField: "triggers[0].timeout"},
		{name: "zero timeout", trigger: `timeout: 0s`, wantField: "triggers[0].timeout"},
		{name: "negative timeout", trigger: `timeout: -1s`, wantField: "triggers[0].timeout"},
		{name: "empty event topic", trigger: "event: {}", wantField: "triggers[0].event.topic"},
		{name: "blank event topic", trigger: `event: { topic: "" }`, wantField: "triggers[0].event.topic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustParseCompose(t, `
name: invalid-trigger-payload
agents:
  reviewer:
    scheduler:
      triggers:
        - `+tt.trigger+`
`)

			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, tt.wantField) {
				t.Fatalf("error = %q, want field %s", got, tt.wantField)
			}
		})
	}
}

func TestNormalizeRejectsTriggerWithoutKind(t *testing.T) {
	tests := []string{
		"{}",
		"{ name: hourly }",
		"{ prompt: run }",
		"{ name: hourly, prompt: run }",
	}

	for _, trigger := range tests {
		t.Run(trigger, func(t *testing.T) {
			spec := mustParseCompose(t, `
name: missing-trigger-kind
agents:
  reviewer:
    scheduler:
      triggers:
        - `+trigger+`
`)

			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, "agents.reviewer.scheduler.triggers[0]") {
				t.Fatalf("error = %q, want trigger path", got)
			}
		})
	}
}

func TestParseRejectsDuplicateAgentKeys(t *testing.T) {
	_, err := Parse([]byte(`
name: duplicate-agent
agents:
  reviewer:
    provider: codex
  reviewer:
    provider: codex
`))
	if err == nil {
		t.Fatalf("expected Parse to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer") || !strings.Contains(got, "duplicate") {
		t.Fatalf("error = %q, want duplicate agent path", got)
	}
}

func TestNormalizeResolvesAgentWorkspaceReference(t *testing.T) {
	spec := mustParseCompose(t, `
name: reference-workspace
workspaces:
  repo-root:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
    workspace:
      name: repo-root
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Agents[0].Workspace == nil || normalized.Agents[0].Workspace.Provider != "local" || normalized.Agents[0].Workspace.Path != "." || normalized.Agents[0].Workspace.Name != "" {
		t.Fatalf("workspace = %#v", normalized.Agents[0].Workspace)
	}
}

func TestNormalizeUsesOnlyGlobalWorkspaceByDefault(t *testing.T) {
	spec := mustParseCompose(t, `
name: default-workspace
workspaces:
  repo-root:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Agents[0].Workspace == nil || normalized.Agents[0].Workspace.Provider != "local" || normalized.Agents[0].Workspace.Path != "." || normalized.Agents[0].Workspace.Name != "" {
		t.Fatalf("workspace = %#v", normalized.Agents[0].Workspace)
	}
}

func TestNormalizeRejectsMixedAgentWorkspaceDefinition(t *testing.T) {
	spec := mustParseCompose(t, `
name: mixed-workspace
workspaces:
  repo-root:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
    workspace:
      name: repo-root
      provider: local
      path: .
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.workspace") || !strings.Contains(got, "name") {
		t.Fatalf("error = %q, want mixed workspace error", got)
	}
}

func TestNormalizeRejectsAmbiguousDefaultWorkspace(t *testing.T) {
	spec := mustParseCompose(t, `
name: ambiguous-workspace
workspaces:
  repo-root:
    provider: local
    path: .
  docs-repo:
    provider: git
    url: https://example.test/docs.git
agents:
  reviewer:
    provider: codex
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.workspace") || !strings.Contains(got, "multiple") {
		t.Fatalf("error = %q, want ambiguous default workspace error", got)
	}
}

func mustParseCompose(t *testing.T, raw string) *ProjectSpec {
	t.Helper()
	spec, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(spec.Workspaces) == 0 {
		spec.Workspaces = map[string]WorkspaceSpec{"default": {Provider: "local", Path: "."}}
	}
	return spec
}
