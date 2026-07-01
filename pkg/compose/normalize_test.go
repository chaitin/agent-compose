package compose

import (
	"os"
	"path/filepath"
	"strconv"
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
		Name: "stable",
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

func TestNormalizeServicesAndProjectTriggers(t *testing.T) {
	spec := mustParseCompose(t, `
name: service-project
metadata:
  labels:
    owner: platform
runtime:
  driver: docker
  image: ghcr.io/org/runtime:latest
  env:
    RUNTIME_FLAG: enabled
agents:
  reviewer:
    provider: codex
services:
  risk-review:
    runtime: node
    entry: services/risk-review.js
    timeout: 10m
    retry:
      max_attempts: 2
      backoff: 5s
    permissions:
      agents:
        - reviewer
      capabilities:
        - repo.read
    agents:
      - reviewer
triggers:
  daily:
    cron: "0 9 * * *"
    target:
      service: risk-review
    input: '{"scope":"daily"}'
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Metadata == nil || normalized.Metadata.Labels["owner"] != "platform" {
		t.Fatalf("metadata = %#v", normalized.Metadata)
	}
	if normalized.Runtime == nil || normalized.Runtime.Driver != "docker" || normalized.Runtime.Env["RUNTIME_FLAG"].Value != "enabled" {
		t.Fatalf("runtime = %#v", normalized.Runtime)
	}
	if got := len(normalized.Services); got != 1 {
		t.Fatalf("service count = %d, want 1", got)
	}
	service := normalized.Services[0]
	if service.Name != "risk-review" || service.Entry != "services/risk-review.js" || service.Retry == nil || service.Retry.Backoff != "5s" {
		t.Fatalf("service = %#v", service)
	}
	if service.Permissions == nil || service.Permissions.Capabilities[0] != "repo.read" {
		t.Fatalf("service permissions = %#v", service.Permissions)
	}
	if got := len(normalized.Triggers); got != 1 {
		t.Fatalf("trigger count = %d, want 1", got)
	}
	trigger := normalized.Triggers[0]
	if trigger.Name != "daily" || trigger.Kind != "cron" || trigger.Target.Service != "risk-review" || trigger.Input == "" {
		t.Fatalf("trigger = %#v", trigger)
	}
}

func TestNormalizeServiceSchemas(t *testing.T) {
	spec := mustParseCompose(t, `
name: schema-project
services:
  inline-review:
    entry: services/review.js
    input_schema: '{"type":"object"}'
    output_schema: schemas/review.output.json
    error_schema: ./schemas/review.error.json
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	service := normalized.Services[0]
	if service.InputSchema != `{"type":"object"}` {
		t.Fatalf("input schema = %q", service.InputSchema)
	}
	if service.OutputSchema != "schemas/review.output.json" || service.ErrorSchema != "schemas/review.error.json" {
		t.Fatalf("service schemas = %#v", service)
	}
}

func TestNormalizeValidatesServiceFileReferencesWithComposePath(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "services"))
	mustMkdirAll(t, filepath.Join(dir, "schemas"))
	mustWriteFile(t, filepath.Join(dir, "services", "review.js"), []byte("export async function main() {}\n"))
	mustWriteFile(t, filepath.Join(dir, "schemas", "review.input.json"), []byte(`{"type":"object"}`))
	mustWriteFile(t, filepath.Join(dir, "schemas", "review.output.json"), []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`))
	composePath := filepath.Join(dir, "agent-compose.yml")

	spec := mustParseCompose(t, `
name: schema-files
services:
  risk-review:
    entry: ./services/review.js
    input_schema: schemas/review.input.json
    output_schema: ./schemas/review.output.json
`)

	normalized, err := Normalize(spec, NormalizeOptions{ComposePath: composePath})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	service := normalized.Services[0]
	if service.Entry != "services/review.js" {
		t.Fatalf("entry = %q, want services/review.js", service.Entry)
	}
	if service.InputSchema != "schemas/review.input.json" || service.OutputSchema != "schemas/review.output.json" {
		t.Fatalf("schemas = %#v, want normalized relative references", service)
	}
}

func TestNormalizeRejectsMissingServiceEntryWithComposePath(t *testing.T) {
	dir := t.TempDir()
	spec := mustParseCompose(t, `
name: missing-entry
services:
  risk-review:
    entry: services/review.js
`)

	_, err := Normalize(spec, NormalizeOptions{ComposePath: filepath.Join(dir, "agent-compose.yml")})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.entry") || !strings.Contains(got, "does not exist") {
		t.Fatalf("error = %q, want missing entry field error", got)
	}
}

func TestNormalizeRejectsServiceEntryDirectoryWithComposePath(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "services", "review.js"))
	spec := mustParseCompose(t, `
name: directory-entry
services:
  risk-review:
    entry: services/review.js
`)

	_, err := Normalize(spec, NormalizeOptions{ComposePath: filepath.Join(dir, "agent-compose.yml")})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.entry") || !strings.Contains(got, "regular file") {
		t.Fatalf("error = %q, want directory entry field error", got)
	}
}

func TestNormalizeRejectsInvalidSchemaFileJSONWithComposePath(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "services"))
	mustMkdirAll(t, filepath.Join(dir, "schemas"))
	mustWriteFile(t, filepath.Join(dir, "services", "review.js"), []byte("export async function main() {}\n"))
	mustWriteFile(t, filepath.Join(dir, "schemas", "review.input.json"), []byte(`{"type":`))
	spec := mustParseCompose(t, `
name: invalid-schema-file
services:
  risk-review:
    entry: services/review.js
    input_schema: schemas/review.input.json
`)

	_, err := Normalize(spec, NormalizeOptions{ComposePath: filepath.Join(dir, "agent-compose.yml")})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.input_schema") || !strings.Contains(got, "valid JSON") {
		t.Fatalf("error = %q, want invalid schema JSON field error", got)
	}
}

func TestNormalizeRejectsInvalidServiceSchemas(t *testing.T) {
	tests := []struct {
		name      string
		field     string
		value     string
		wantError string
	}{
		{name: "invalid inline json", field: "input_schema", value: `{"type":`, wantError: "valid JSON"},
		{name: "parent reference", field: "output_schema", value: "../secret.json", wantError: "project directory"},
		{name: "absolute reference", field: "error_schema", value: "/etc/passwd", wantError: "relative path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustParseCompose(t, `
name: invalid-schema
services:
  risk-review:
    entry: services/review.js
    `+tt.field+`: `+quoteYAMLScalar(tt.value)+`
`)

			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, "services.risk-review."+tt.field) || !strings.Contains(got, tt.wantError) {
				t.Fatalf("error = %q, want field %s and %q", got, tt.field, tt.wantError)
			}
		})
	}
}

func TestNormalizeRejectsUnsafeServiceEntry(t *testing.T) {
	tests := []struct {
		name      string
		entry     string
		wantError string
	}{
		{name: "parent reference", entry: "../review.js", wantError: "project directory"},
		{name: "absolute reference", entry: "/tmp/review.js", wantError: "relative path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustParseCompose(t, `
name: invalid-entry
services:
  risk-review:
    entry: `+quoteYAMLScalar(tt.entry)+`
`)

			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, "services.risk-review.entry") || !strings.Contains(got, tt.wantError) {
				t.Fatalf("error = %q, want entry field and %q", got, tt.wantError)
			}
		})
	}
}

func TestNormalizeRejectsInvalidEnvName(t *testing.T) {
	spec := mustParseCompose(t, `
name: invalid-env
agents:
  reviewer:
    provider: codex
    env:
      BAD-NAME: value
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.env.BAD-NAME") || !strings.Contains(got, "environment variable name") {
		t.Fatalf("error = %q, want env name path", got)
	}
}

func TestNormalizeRejectsServiceWithoutEntry(t *testing.T) {
	spec := mustParseCompose(t, `
name: bad-service
services:
  risk-review:
    runtime: node
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.entry") {
		t.Fatalf("error = %q, want service entry path", got)
	}
}

func TestNormalizeRejectsSymlinkServiceEntry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.js")
	if err := os.WriteFile(target, []byte("export async function main() {}\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "services"), 0o700); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "services", "review.js")); err != nil {
		t.Fatalf("symlink service entry: %v", err)
	}
	composePath := filepath.Join(dir, "agent-compose.yml")
	if err := os.WriteFile(composePath, []byte(`
name: symlink-entry
services:
  risk-review:
    entry: services/review.js
`), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	_, err := NormalizeFile(composePath)
	if err == nil {
		t.Fatalf("expected NormalizeFile to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.entry") || !strings.Contains(got, "regular file") {
		t.Fatalf("error = %q, want symlink service entry rejection", got)
	}
}

func TestNormalizeRejectsSymlinkInputSchema(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "services"), 0o700); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "schemas"), 0o700); err != nil {
		t.Fatalf("mkdir schemas: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "services", "review.js"), []byte("export async function main() {}\n"), 0o600); err != nil {
		t.Fatalf("write service: %v", err)
	}
	target := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(target, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "schemas", "input.json")); err != nil {
		t.Fatalf("symlink schema: %v", err)
	}
	composePath := filepath.Join(dir, "agent-compose.yml")
	if err := os.WriteFile(composePath, []byte(`
name: symlink-schema
services:
  risk-review:
    entry: services/review.js
    input_schema: schemas/input.json
`), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	_, err := NormalizeFile(composePath)
	if err == nil {
		t.Fatalf("expected NormalizeFile to fail")
	}
	if got := err.Error(); !strings.Contains(got, "services.risk-review.input_schema") || !strings.Contains(got, "regular file") {
		t.Fatalf("error = %q, want symlink schema rejection", got)
	}
}

func TestNormalizeRejectsTriggerWithoutTarget(t *testing.T) {
	spec := mustParseCompose(t, `
name: bad-trigger
triggers:
  daily:
    cron: "0 9 * * *"
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "triggers.daily.target") {
		t.Fatalf("error = %q, want trigger target path", got)
	}
}

func TestNormalizePreservesValidAgentNames(t *testing.T) {
	spec := &ProjectSpec{
		Name: "valid-agents",
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
				Name: "invalid-agent",
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

func mustParseCompose(t *testing.T, raw string) *ProjectSpec {
	t.Helper()
	spec, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	return spec
}

func quoteYAMLScalar(value string) string {
	return strconv.Quote(value)
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create directory %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}
