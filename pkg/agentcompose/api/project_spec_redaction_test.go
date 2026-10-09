package api

import (
	"testing"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"

	"gopkg.in/yaml.v3"
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
				{Name: "AZURE_OPENAI_API_KEY", Value: "unproxyable"},
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
				// Google credentials stopped being recognized with the Gemini
				// provider, so they are exposed exactly like the names above.
				{Name: "GOOGLE_API_KEY", Value: "unrecognized"},
				{Name: "GEMINI_API_KEY", Value: "unrecognized"},
				// An address is not a credential: the operator declared it for the
				// daemon, and the views keep showing what they wrote.
				{Name: "ANTHROPIC_BASE_URL", Value: "https://upstream.example"},
				{Name: "LLM_API_ENDPOINT", Value: "https://upstream.example/v1"},
			},
		}},
	}

	redacted := RedactProjectSpecSecrets(spec)

	for index, want := range []string{"unrecognized", "unrecognized", "unrecognized", "unrecognized", "https://upstream.example", "https://upstream.example/v1"} {
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

// TestRedactedProjectViewRoundTripsThroughPatch pins the contract between the
// two rules that decide whether an edit survives: the view redacts a provider
// credential by name whether or not the declaration set secret: true, and the
// restore step (internal/projects) must recover that value from the stored
// revision. When the two disagree, "read the project, change one field, save it
// back" fails closed for every project that declares a credential through the
// plain form, so the operator can no longer edit anything.
func TestRedactedProjectViewRoundTripsThroughPatch(t *testing.T) {
	parsed, err := compose.Parse([]byte(`
name: demo
variables:
  OPENAI_API_KEY: sk-real-project-credential
  MODE: review
agents:
  reviewer:
    provider: openai
    model: gpt-5
    env:
      DEEPSEEK_API_KEY: sk-real-agent-credential
`))
	if err != nil {
		t.Fatalf("compose.Parse() error = %v", err)
	}
	persisted, err := compose.Normalize(parsed, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("compose.Normalize() error = %v", err)
	}

	view := ProjectSpecToProtoRedacted(persisted)
	if got := envValueByName(view.GetVariables(), "OPENAI_API_KEY"); got != secretRedactedValue {
		t.Fatalf("view credential = %q, want %q", got, secretRedactedValue)
	}

	// The client sends the view back through the same parse path the transport
	// uses for PatchProject.
	raw, shapeIssues := ProjectSpecYAMLShape(view)
	if len(shapeIssues) > 0 {
		t.Fatalf("redacted view is not a valid request shape: %#v", shapeIssues)
	}
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	submitted, err := compose.Parse(encoded)
	if err != nil {
		t.Fatalf("client re-parse view: %v", err)
	}

	restored, issues, err := projects.RestoreProjectSecrets(persisted, submitted)
	if err != nil {
		t.Fatalf("RestoreProjectSecrets() error = %v", err)
	}
	if len(issues) > 0 {
		t.Fatalf("round trip rejected: %#v", issues)
	}
	if got := restored.Variables["OPENAI_API_KEY"].Value; got != "sk-real-project-credential" {
		t.Fatalf("project credential = %q, want the stored value", got)
	}
	if got := restored.Variables["MODE"].Value; got != "review" {
		t.Fatalf("unrelated variable = %q", got)
	}
	if got := restored.Agents["reviewer"].Env["DEEPSEEK_API_KEY"].Value; got != "sk-real-agent-credential" {
		t.Fatalf("agent credential = %q, want the stored value", got)
	}
}

func envValueByName(items []*agentcomposev2.EnvVarSpec, name string) string {
	for _, item := range items {
		if item.GetName() == name {
			return item.GetValue()
		}
	}
	return ""
}

// TestRedactProjectSpecSecretsKeepsUnabsorbedCredentialsVisible pins the
// boundary the credential surface draws. A recognized but not-yet-absorbed
// credential (git, MCP, registry) still reaches the guest as plaintext, so the
// view keeps showing it: redacting it would claim an isolation the runtime does
// not provide. The LLM credential beside it is held by the daemon and is
// redacted as before.
func TestRedactProjectSpecSecretsKeepsUnabsorbedCredentialsVisible(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{
		Agents: []*agentcomposev2.AgentSpec{{
			Name: "reviewer",
			Env: []*agentcomposev2.EnvVarSpec{
				{Name: "GITHUB_TOKEN", Value: "plaintext-git"},
				{Name: "MCP_TOKEN", Value: "plaintext-mcp"},
				{Name: "REGISTRY_TOKEN", Value: "plaintext-registry"},
				{Name: "OPENAI_API_KEY", Value: "held-by-daemon"},
			},
			McpServers: []*agentcomposev2.MCPServerSpec{{
				Headers: []*agentcomposev2.EnvVarSpec{
					{Name: "Authorization", Value: "Bearer plaintext-header"},
				},
			}},
		}},
	}

	redacted := RedactProjectSpecSecrets(spec)

	for index, want := range []string{"plaintext-git", "plaintext-mcp", "plaintext-registry", secretRedactedValue} {
		if got := redacted.Agents[0].Env[index].GetValue(); got != want {
			t.Errorf("env[%d] = %q, want %q", index, got, want)
		}
	}
	if got := redacted.Agents[0].McpServers[0].Headers[0].GetValue(); got != "Bearer plaintext-header" {
		t.Errorf("MCP header = %q, want the unabsorbed header left visible", got)
	}
}
