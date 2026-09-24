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

// TestRedactProjectSpecSecretsHidesEveryProviderCredential pins the rule to the
// denylist rather than to the subset the daemon can proxy. A recognized
// credential the facade cannot absorb is still removed from the guest
// environment, so the declaration is again the only place the value survives and
// a view must not return it.
func TestRedactProjectSpecSecretsHidesEveryProviderCredential(t *testing.T) {
	redacted := RedactProjectSpecSecrets(&agentcomposev2.ProjectSpec{
		Agents: []*agentcomposev2.AgentSpec{{
			Name: "reviewer",
			Env: []*agentcomposev2.EnvVarSpec{
				{Name: "GOOGLE_API_KEY", Value: "unproxyable"},
				{Name: "AZURE_OPENAI_API_KEY", Value: "unproxyable"},
				{Name: "GEMINI_API_KEY", Value: "unproxyable"},
				{Name: "CODEX_API_KEY", Value: "absorbed"},
				{Name: "DEEPSEEK_API_KEY", Value: "absorbed"},
				{Name: "LLM_API_HEADERS", Value: `{"X-Gateway-Token":"header"}`},
			},
		}},
	})

	for _, item := range redacted.Agents[0].Env {
		if got := item.GetValue(); got != secretRedactedValue {
			t.Errorf("%s = %q, want %q", item.GetName(), got, secretRedactedValue)
		}
	}
}

// TestRedactProjectSpecSecretsKeepsUnrecognizedCredentialsVisible pins the
// deliberate limit of the rule. A credential-looking name the denylist does not
// know really is passed through to the guest, so the project check warning is
// the operator's only signal; redacting the view would describe the value as
// protected without changing the exposure.
func TestRedactProjectSpecSecretsKeepsUnrecognizedCredentialsVisible(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{
		Agents: []*agentcomposev2.AgentSpec{{
			Name: "reviewer",
			Env: []*agentcomposev2.EnvVarSpec{
				{Name: "MYCORP_API_KEY", Value: "unrecognized"},
				{Name: "MYCORP_AUTH_TOKEN", Value: "unrecognized"},
				// An address is not a credential: the operator declared it for the
				// daemon, and the views keep showing what they wrote.
				{Name: "ANTHROPIC_BASE_URL", Value: "https://upstream.example"},
				{Name: "LLM_API_ENDPOINT", Value: "https://upstream.example/v1"},
			},
		}},
	}

	redacted := RedactProjectSpecSecrets(spec)

	for index, want := range []string{"unrecognized", "unrecognized", "https://upstream.example", "https://upstream.example/v1"} {
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
