package llms

import (
	"context"
	"errors"
	"strings"
	"testing"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func declaredEnvItems(pairs ...string) []domain.SandboxEnvVar {
	items := make([]domain.SandboxEnvVar, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		items = append(items, domain.SandboxEnvVar{Name: pairs[i], Value: pairs[i+1], Secret: true})
	}
	return items
}

// TestClassifyDeclaredLLMCredential pins the recognition that both a project
// check and a run share: which variable names the daemon treats as a first-party
// credential, whether the declared endpoint is the vendor's own, and whether the
// daemon can proxy it.
func TestClassifyDeclaredLLMCredential(t *testing.T) {
	tests := []struct {
		name         string
		env          []domain.SandboxEnvVar
		wantEnvName  string
		wantFamily   string
		wantEndpoint string
		wantOfficial bool
		wantAbsorbed bool
	}{
		{
			name:         "openai key with no endpoint defaults to the vendor",
			env:          declaredEnvItems("OPENAI_API_KEY", "sk-openai"),
			wantEnvName:  "OPENAI_API_KEY",
			wantFamily:   ProviderFamilyOpenAI,
			wantEndpoint: "https://api.openai.com",
			wantOfficial: true,
			wantAbsorbed: true,
		},
		{
			name:         "anthropic bearer token keeps the bearer presentation",
			env:          declaredEnvItems("ANTHROPIC_AUTH_TOKEN", "sk-ant-token"),
			wantEnvName:  "ANTHROPIC_AUTH_TOKEN",
			wantFamily:   ProviderFamilyAnthropic,
			wantEndpoint: "https://api.anthropic.com",
			wantOfficial: true,
			wantAbsorbed: true,
		},
		{
			name:         "deepseek key is recognized as a first-party credential",
			env:          declaredEnvItems("DEEPSEEK_API_KEY", "sk-deepseek"),
			wantEnvName:  "DEEPSEEK_API_KEY",
			wantFamily:   ProviderFamilyOpenAI,
			wantEndpoint: "https://api.deepseek.com",
			wantOfficial: true,
			wantAbsorbed: true,
		},
		{
			name:         "a gateway endpoint is absorbed but is not official",
			env:          declaredEnvItems("ANTHROPIC_API_KEY", "sk-ant", "ANTHROPIC_BASE_URL", "https://anthropic.internal"),
			wantEnvName:  "ANTHROPIC_API_KEY",
			wantFamily:   ProviderFamilyAnthropic,
			wantEndpoint: "https://anthropic.internal",
			wantOfficial: false,
			wantAbsorbed: true,
		},
		{
			name:         "a generic key follows the protocol it declares",
			env:          declaredEnvItems("LLM_API_KEY", "sk-generic", "LLM_API_PROTOCOL", "messages", "LLM_API_ENDPOINT", "https://messages.internal"),
			wantEnvName:  "LLM_API_KEY",
			wantFamily:   ProviderFamilyAnthropic,
			wantEndpoint: "https://messages.internal",
			wantOfficial: false,
			wantAbsorbed: true,
		},
		{
			name:         "a google key is recognized but cannot be proxied",
			env:          declaredEnvItems("GEMINI_API_KEY", "sk-gemini"),
			wantEnvName:  "GEMINI_API_KEY",
			wantFamily:   providerFamilyGoogle,
			wantEndpoint: "https://generativelanguage.googleapis.com",
			wantOfficial: true,
			wantAbsorbed: false,
		},
		{
			name:         "an azure key names no endpoint and cannot be proxied",
			env:          declaredEnvItems("AZURE_OPENAI_API_KEY", "sk-azure"),
			wantEnvName:  "AZURE_OPENAI_API_KEY",
			wantFamily:   ProviderFamilyOpenAI,
			wantEndpoint: "",
			wantOfficial: false,
			wantAbsorbed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyDeclaredLLMCredential(tt.env)
			if !ok {
				t.Fatalf("ClassifyDeclaredLLMCredential(%v) reported no credential", tt.env)
			}
			if got.EnvName != tt.wantEnvName || got.Family != tt.wantFamily {
				t.Errorf("credential = %s/%s, want %s/%s", got.EnvName, got.Family, tt.wantEnvName, tt.wantFamily)
			}
			if got.Endpoint != tt.wantEndpoint {
				t.Errorf("Endpoint = %q, want %q", got.Endpoint, tt.wantEndpoint)
			}
			if got.Official != tt.wantOfficial {
				t.Errorf("Official = %v, want %v", got.Official, tt.wantOfficial)
			}
			if got.Absorbed != tt.wantAbsorbed {
				t.Errorf("Absorbed = %v, want %v", got.Absorbed, tt.wantAbsorbed)
			}
		})
	}
}

// TestClassifyDeclaredLLMCredentialIgnoresUnrecognizedNames pins the limit of
// the daemon's reach: a private credential under a name the daemon does not know
// is passed through to the runtime untouched. Recognizing it would mean either
// breaking a workload the daemon cannot proxy or silently dropping a credential.
func TestClassifyDeclaredLLMCredentialIgnoresUnrecognizedNames(t *testing.T) {
	env := declaredEnvItems("XXX_API_KEY", "local-secret", "XXX_API", "https://my-llama.internal")
	if got, ok := ClassifyDeclaredLLMCredential(env); ok {
		t.Fatalf("ClassifyDeclaredLLMCredential = %#v, want no recognition", got)
	}
}

// TestDeclaredUpstreamFromAgentEnvBuildsADaemonOwnedConnection pins that the
// declaration becomes a connection the daemon owns, carrying the real credential
// and the vendor's default endpoint and presentation.
func TestDeclaredUpstreamFromAgentEnvBuildsADaemonOwnedConnection(t *testing.T) {
	dialect, err := DialectFor("claude")
	if err != nil {
		t.Fatalf("DialectFor(claude): %v", err)
	}
	upstream, ok := DeclaredUpstreamFromAgentEnv("sandbox-1", declaredEnvItems("ANTHROPIC_AUTH_TOKEN", "sk-ant-token"), dialect, "")
	if !ok {
		t.Fatal("DeclaredUpstreamFromAgentEnv reported no upstream")
	}
	if !strings.HasPrefix(upstream.Provider.ID, DeclaredConnectionPrefix+"sandbox-1:"+ProviderFamilyAnthropic+":") {
		t.Errorf("Provider.ID = %q, want it scoped to the sandbox, family and declaration", upstream.Provider.ID)
	}
	if upstream.Provider.APIKey != "sk-ant-token" {
		t.Error("the declared credential is not carried by the daemon-owned connection")
	}
	if upstream.Provider.BaseURL != "https://api.anthropic.com" {
		t.Errorf("BaseURL = %q, want the vendor endpoint", upstream.Provider.BaseURL)
	}
	if upstream.Provider.AuthHeader != "Authorization" || upstream.Provider.AuthScheme != "Bearer" {
		t.Errorf("auth = %s/%s, want bearer", upstream.Provider.AuthHeader, upstream.Provider.AuthScheme)
	}
	if upstream.Provider.Scope != ProviderScopeDeclared {
		t.Errorf("Scope = %q, want %q", upstream.Provider.Scope, ProviderScopeDeclared)
	}
	if !IsDeclaredConnectionID(upstream.Provider.ID) {
		t.Errorf("id %q is not in the reserved declared namespace", upstream.Provider.ID)
	}
}

// TestPrepareAgentLLMDeclaredCredentialStaysOnTheDaemon is the regression test
// for the exposure this change closes. An operator who publishes a key in an
// agent's environment must not thereby hand that key to the guest: the run is
// proxied, and the only credential the guest can read is the run-scoped facade
// token.
func TestPrepareAgentLLMDeclaredCredentialStaysOnTheDaemon(t *testing.T) {
	tests := []struct {
		name            string
		agent           string
		env             []domain.SandboxEnvVar
		model           string
		vendorVarNames  []string
		wantProviderKey string
	}{
		{
			name: "codex", agent: "codex",
			env:   declaredEnvItems("OPENAI_API_KEY", "sk-openai-secret"),
			model: "gpt-5.5", vendorVarNames: []string{"OPENAI_API_KEY", "LLM_API_KEY"},
			wantProviderKey: "sk-openai-secret",
		},
		{
			name: "claude", agent: "claude",
			env:   declaredEnvItems("ANTHROPIC_API_KEY", "sk-ant-secret"),
			model: "claude-sonnet-4", vendorVarNames: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "LLM_API_KEY"},
			wantProviderKey: "sk-ant-secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateLLMEnv(t)
			root := t.TempDir()
			store := &prepareAgentLLMStore{}

			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: bareModelConfig(root), Store: store,
				Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
				AgentKind: tt.agent, Model: tt.model, AgentEnv: tt.env,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			if len(store.declared) != 1 {
				t.Fatalf("persisted declared connections = %d, want 1", len(store.declared))
			}
			if store.declared[0].APIKey != tt.wantProviderKey {
				t.Error("the daemon-owned connection does not carry the declared credential")
			}
			if len(store.savedTokens) != 1 {
				t.Fatalf("saved tokens = %d, want one facade token", len(store.savedTokens))
			}
			if prepared.Credential != prepared.Token {
				t.Errorf("Credential = %q, want the facade token", prepared.Credential)
			}
			// Every credential-shaped variable the guest can read carries the
			// token. The declared key is nowhere in the guest environment.
			for _, name := range tt.vendorVarNames {
				if got := prepared.Env[name]; got != prepared.Token {
					t.Errorf("env[%s] = %q, want the facade token", name, got)
				}
			}
			for name, value := range prepared.Env {
				if strings.Contains(value, tt.wantProviderKey) {
					t.Errorf("env[%s] carries the declared key", name)
				}
			}
			if strings.Contains(prepared.Endpoint, "api.openai.com") || strings.Contains(prepared.Endpoint, "api.anthropic.com") {
				t.Errorf("Endpoint = %q, want the daemon facade route", prepared.Endpoint)
			}
		})
	}
}

// TestPrepareAgentLLMDeclaredCredentialWithoutAModel pins that a declaration
// which names no model is still ErrNoModel: the daemon will not send an
// arbitrary catalog default to an endpoint the operator named.
func TestPrepareAgentLLMDeclaredCredentialWithoutAModel(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		models: []Model{{ID: "gpt-5.5", Name: "gpt-5.5", Enabled: true, DefaultModel: true}},
	}}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store,
		Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "codex",
		AgentEnv:  declaredEnvItems("OPENAI_API_KEY", "sk-openai"),
	})
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrNoModel", err)
	}
	// The declaration was never usable, so the daemon must not keep the
	// credential in its connection configuration.
	if len(store.declared) != 0 {
		t.Fatalf("a declaration with no model was persisted: %#v", store.declared)
	}
}

// TestCatalogNeverSelectsADeclaredConnection pins the isolation a declared
// connection needs. It is addressable by the run that declared it and by nothing
// else, so one agent's credential can never be used to serve another agent.
func TestCatalogNeverSelectsADeclaredConnection(t *testing.T) {
	dialect, err := DialectFor("codex")
	if err != nil {
		t.Fatalf("DialectFor(codex): %v", err)
	}
	upstream, ok := DeclaredUpstreamFromAgentEnv("sandbox-1", declaredEnvItems("OPENAI_API_KEY", "sk-openai"), dialect, "gpt-5.5")
	if !ok {
		t.Fatal("DeclaredUpstreamFromAgentEnv reported no upstream")
	}
	catalog, err := LoadCatalog(context.Background(), fakeCatalogStore{providers: []Provider{upstream.Provider}})
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if got := catalog.Connections(); len(got) != 0 {
		t.Fatalf("Connections() = %v, want none: a declared connection is not shared configuration", got)
	}
	if _, err := catalog.Resolve("", "gpt-5.5", nil); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("Resolve(no connection named) error = %v, want ErrNoConnection", err)
	}
	target, err := catalog.Resolve(upstream.Provider.ID, "gpt-5.5", nil)
	if err != nil {
		t.Fatalf("Resolve(declared id) error = %v", err)
	}
	if target.Provider.ID != upstream.Provider.ID {
		t.Errorf("resolved provider = %q, want %q", target.Provider.ID, upstream.Provider.ID)
	}
}

// TestMergeManagedExecEnvStripsDeclaredProviderKeys pins the last line of the
// base environment: the upstream an operator declared never survives into the
// guest, while the facade token and address the managed layer installs do.
func TestMergeManagedExecEnvStripsDeclaredProviderKeys(t *testing.T) {
	base := map[string]string{
		"OPENAI_API_KEY":    "sk-declared",
		"OPENAI_BASE_URL":   "https://declared-upstream.example",
		"LLM_API_ENDPOINT":  "https://declared-upstream.example/v1",
		"LLM_API_PROTOCOL":  "chat_completions",
		"GOOGLE_API_KEY":    "sk-google-declared",
		"CODEX_API_KEY":     "sk-codex-declared",
		"DEEPSEEK_API_KEY":  "sk-deepseek-declared",
		"LLM_API_HEADERS":   `{"x-secret":"1"}`,
		"UNRELATED_SETTING": "kept",
	}
	managed := map[string]string{
		"OPENAI_API_KEY":               "facade-token",
		"OPENAI_BASE_URL":              "http://daemon.test/llm/openai/v1",
		"LLM_API_ENDPOINT":             "http://daemon.test/llm/openai/v1",
		"LLM_API_PROTOCOL":             "responses",
		guestFacadeTokenEnvName:        "facade-token",
		"AGENT_COMPOSE_RESOLVED_MODEL": "gpt-5.5",
	}
	merged := MergeManagedExecEnv(base, managed)
	if got := merged["OPENAI_API_KEY"]; got != "facade-token" {
		t.Errorf("OPENAI_API_KEY = %q, want the managed facade token", got)
	}
	if got := merged["OPENAI_BASE_URL"]; got != "http://daemon.test/llm/openai/v1" {
		t.Errorf("OPENAI_BASE_URL = %q, want the managed facade address", got)
	}
	for _, stripped := range []string{"GOOGLE_API_KEY", "CODEX_API_KEY", "DEEPSEEK_API_KEY", "LLM_API_HEADERS"} {
		if value, ok := merged[stripped]; ok {
			t.Errorf("%s = %q survived the managed merge, but it is a declared provider credential", stripped, value)
		}
	}
	if got := merged["UNRELATED_SETTING"]; got != "kept" {
		t.Errorf("UNRELATED_SETTING = %q, want the base value", got)
	}
}

// TestDeclaredEndpointNamesStayOffTheGuestEnv is the end-to-end shape of the
// same rule for every declaration spelling the daemon recognizes: whichever
// vendor variable carries the endpoint, the value must not reach the guest.
func TestDeclaredEndpointNamesStayOffTheGuestEnv(t *testing.T) {
	for _, name := range []string{
		"LLM_API_ENDPOINT", "LLM_API_PROTOCOL",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT",
		"OPENAI_BASE_URL", "DEEPSEEK_BASE_URL", "OPENROUTER_BASE_URL",
	} {
		t.Run(name, func(t *testing.T) {
			base := map[string]string{name: "https://declared-upstream.example", "KEPT": "1"}
			merged := MergeManagedExecEnv(base, nil)
			if _, ok := merged[name]; ok {
				t.Fatalf("%s reached the guest runtime", name)
			}
			if merged["KEPT"] != "1" {
				t.Fatalf("unrelated base env was dropped: %#v", merged)
			}
		})
	}
}

// TestDeclaredCredentialNamesAreAllOnTheGuestDenylist is the regression test for
// the gap the review found: the recognition table and the denylist are separate
// lists in separate packages, so a credential the daemon imports and proxies
// could still be passed through to the guest under its own name. A declaration
// whose name is not on the denylist reaches the sandbox in plaintext even though
// the run is proxied, which contradicts both the project check and the view
// redaction that key off the same guarantee.
func TestDeclaredCredentialNamesAreAllOnTheGuestDenylist(t *testing.T) {
	for _, spec := range declaredCredentialSpecs {
		for _, name := range spec.EnvNames {
			if !driverpkg.LLMProviderCredentialEnvName(name) {
				t.Errorf("credential %s (family %s) is recognized but not on the guest denylist: the declared value would reach the sandbox", name, spec.Family)
			}
		}
		for _, name := range spec.EndpointEnvNames {
			if !driverpkg.LLMProviderEnvName(name) {
				t.Errorf("endpoint %s (family %s) is recognized but not on the guest denylist: the declared address would reach the sandbox", name, spec.Family)
			}
		}
	}
	if !driverpkg.LLMProviderCredentialEnvName(genericCredentialEnvName) {
		t.Errorf("%s is absorbed but not on the guest denylist", genericCredentialEnvName)
	}
}

// TestDeclaredConnectionIDDependsOnTheDeclaration is the regression test for the
// cross-run credential hazard the review found. The declaration is per run — a
// run request may carry its own environment — so a row keyed only by sandbox and
// family would let one run's preparation overwrite the upstream another run is
// still resolving through its own facade token.
func TestDeclaredConnectionIDDependsOnTheDeclaration(t *testing.T) {
	dialect, err := DialectFor("codex")
	if err != nil {
		t.Fatalf("DialectFor(codex): %v", err)
	}
	idFor := func(key string) string {
		t.Helper()
		upstream, ok := DeclaredUpstreamFromAgentEnv("sandbox-1", declaredEnvItems("OPENAI_API_KEY", key), dialect, "gpt-5")
		if !ok {
			t.Fatalf("DeclaredUpstreamFromAgentEnv reported no upstream for %q", key)
		}
		return upstream.Provider.ID
	}

	first, second := idFor("key-a"), idFor("key-b")
	if first == second {
		t.Fatalf("two declarations of one sandbox share the connection id %q", first)
	}
	if again := idFor("key-a"); again != first {
		t.Errorf("the same declaration is not stable: %q then %q", first, again)
	}
	for _, id := range []string{first, second} {
		if !IsDeclaredConnectionID(id) {
			t.Errorf("id %q is not in the reserved declared namespace", id)
		}
		if !strings.HasPrefix(id, DeclaredConnectionPrefix+"sandbox-1:"+ProviderFamilyOpenAI+":") {
			t.Errorf("id %q is not scoped to the sandbox and family", id)
		}
	}
}
