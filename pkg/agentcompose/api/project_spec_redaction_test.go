package api

import (
	"testing"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestRedactProjectSpecSecretsHidesAbsorbedCredentials pins the display half of
// the daemon-only credential boundary: once the daemon absorbs a first-party
// LLM credential into its own connection, a project response must not echo the
// value back, whether or not the declaration asked for redaction.
func TestRedactProjectSpecSecretsHidesAbsorbedCredentials(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{
		Name: "demo",
		Variables: []*agentcomposev2.EnvVarSpec{
			{Name: "ANTHROPIC_AUTH_TOKEN", Value: "absorbed-project-secret"},
		},
		Agents: []*agentcomposev2.AgentSpec{{
			Name: "reviewer",
			Env: []*agentcomposev2.EnvVarSpec{
				{Name: "OPENAI_API_KEY", Value: "absorbed-agent-secret"},
				{Name: "LLM_API_KEY", Value: "absorbed-generic-secret"},
				{Name: "MODE", Value: "review"},
			},
		}},
	}

	redacted := RedactProjectSpecSecrets(spec)

	if got := redacted.Variables[0].GetValue(); got != secretRedactedValue {
		t.Errorf("project variable = %q, want %q", got, secretRedactedValue)
	}
	if got := redacted.Variables[0].GetName(); got != "ANTHROPIC_AUTH_TOKEN" {
		t.Errorf("redaction hid the variable name: %q", got)
	}
	for index, want := range []string{secretRedactedValue, secretRedactedValue, "review"} {
		if got := redacted.Agents[0].Env[index].GetValue(); got != want {
			t.Errorf("agent env[%d] = %q, want %q", index, got, want)
		}
	}
	if got := redacted.Agents[0].Env[2].GetName(); got != "MODE" {
		t.Errorf("unrelated env name = %q", got)
	}
}

// TestRedactProjectSpecSecretsKeepsUnprotectedCredentialsVisible pins the
// deliberate limit of the rule. A credential the daemon cannot absorb reaches
// the agent runtime, so the project check warns about it; redacting it here
// would describe a value as protected when it is not, and would hide the one
// signal an operator gets.
func TestRedactProjectSpecSecretsKeepsUnprotectedCredentialsVisible(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{
		Agents: []*agentcomposev2.AgentSpec{{
			Name: "reviewer",
			Env: []*agentcomposev2.EnvVarSpec{
				{Name: "GOOGLE_API_KEY", Value: "unproxyable"},
				{Name: "AZURE_OPENAI_API_KEY", Value: "unproxyable"},
				{Name: "MYCORP_API_KEY", Value: "unrecognized"},
				{Name: "ANTHROPIC_BASE_URL", Value: "https://upstream.example"},
			},
		}},
	}

	redacted := RedactProjectSpecSecrets(spec)

	for index, want := range []string{"unproxyable", "unproxyable", "unrecognized", "https://upstream.example"} {
		if got := redacted.Agents[0].Env[index].GetValue(); got != want {
			t.Errorf("env[%d] = %q, want %q to stay visible", index, got, want)
		}
	}
}

// TestRedactProjectSpecSecretsStillHonorsAnExplicitSecret keeps the existing
// opt-in rule covered: marking any variable secret redacts it regardless of
// what the credential rules decide.
func TestRedactProjectSpecSecretsStillHonorsAnExplicitSecret(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{
		Variables: []*agentcomposev2.EnvVarSpec{{Name: "RELEASE_TOKEN", Value: "declared-secret", Secret: true}},
	}

	redacted := RedactProjectSpecSecrets(spec)

	if got := redacted.Variables[0].GetValue(); got != secretRedactedValue {
		t.Errorf("explicitly secret variable = %q, want %q", got, secretRedactedValue)
	}
}

// TestRedactProjectSpecSecretsDoesNotMutateTheSource guards the caller's spec:
// the daemon keeps serving the declaration it absorbed, and only the response
// copy is redacted.
func TestRedactProjectSpecSecretsDoesNotMutateTheSource(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{
		Variables: []*agentcomposev2.EnvVarSpec{{Name: "OPENAI_API_KEY", Value: "absorbed"}},
	}

	RedactProjectSpecSecrets(spec)

	if got := spec.Variables[0].GetValue(); got != "absorbed" {
		t.Fatalf("redaction mutated the source spec: %q", got)
	}
}
