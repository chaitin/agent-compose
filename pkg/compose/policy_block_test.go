package compose

import (
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// The agent policy block was renamed from `sandbox` to `policy`. The deprecated
// key stays a working spelling: a project written before the rename keeps
// normalizing to the same policy, and therefore to the same canonical JSON and
// spec hash. Reading that key as absent would silently turn a declared policy
// into an undeclared one, which the engine treats as unrestricted.
func TestDeprecatedSandboxKeyNormalizesIdenticallyToPolicy(t *testing.T) {
	const declaration = `
      stopped_runtime_policy: retain
      network:
        default: deny-all
        allow:
          - host: api.github.com
            port: 443
`
	policySpec := mustNormalizeCompose(t, `
name: policy-alias
agents:
  worker:
    provider: codex
    policy:
`+declaration, nil)
	legacySpec := mustNormalizeCompose(t, `
name: policy-alias
agents:
  worker:
    provider: codex
    sandbox:
`+declaration, nil)

	policyJSON, err := policySpec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	legacyJSON, err := legacySpec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if string(policyJSON) != string(legacyJSON) {
		t.Fatalf("deprecated sandbox key normalized differently:\npolicy:  %s\nsandbox: %s", policyJSON, legacyJSON)
	}
	if !strings.Contains(string(legacyJSON), `"policy"`) || strings.Contains(string(legacyJSON), `"sandbox"`) {
		t.Fatalf("canonical JSON = %s, want the policy key rewritten and no deprecated key", legacyJSON)
	}
	if got, want := mustHash(t, legacySpec), mustHash(t, policySpec); got != want {
		t.Fatalf("hash = %s, want %s for the same declaration under the policy key", got, want)
	}
}

func TestPolicyBlockRejectsDeclaringBothKeys(t *testing.T) {
	_, err := Normalize(&ProjectSpec{Name: "project", Agents: map[string]AgentSpec{
		"worker": {
			Policy:  &SandboxSpec{StoppedRuntimePolicy: domain.StoppedRuntimePolicyRemove},
			Sandbox: &SandboxSpec{StoppedRuntimePolicy: domain.StoppedRuntimePolicyRetain},
		},
	}}, NormalizeOptions{})
	if err == nil || !strings.Contains(err.Error(), "agents.worker.sandbox") || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("Normalize error = %v, want a both-keys rejection naming the agent", err)
	}
}

// A declaration error must name the key the project actually used, so the
// message points at a line that exists in the file.
func TestPolicyBlockErrorPathNamesTheDeclaredKey(t *testing.T) {
	tests := []struct {
		name    string
		policy  *SandboxSpec
		legacy  *SandboxSpec
		wantErr string
	}{
		{
			name:    "policy key",
			policy:  &SandboxSpec{StoppedRuntimePolicy: "prune"},
			wantErr: "agents.worker.policy.stopped_runtime_policy",
		},
		{
			name:    "deprecated sandbox key",
			legacy:  &SandboxSpec{StoppedRuntimePolicy: "prune"},
			wantErr: "agents.worker.sandbox.stopped_runtime_policy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Normalize(&ProjectSpec{Name: "project", Agents: map[string]AgentSpec{
				"worker": {Policy: tt.policy, Sandbox: tt.legacy},
			}}, NormalizeOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Normalize error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestPolicyBlockAccessorsPreferPolicy(t *testing.T) {
	policy := &SandboxSpec{StoppedRuntimePolicy: domain.StoppedRuntimePolicyRemove}
	legacy := &SandboxSpec{StoppedRuntimePolicy: domain.StoppedRuntimePolicyRetain}

	if got := (AgentSpec{Policy: policy, Sandbox: legacy}).PolicyBlock(); got != policy {
		t.Fatalf("AgentSpec.PolicyBlock() = %#v, want the policy block", got)
	}
	if got := (AgentSpec{Sandbox: legacy}).PolicyBlock(); got != legacy {
		t.Fatalf("AgentSpec.PolicyBlock() = %#v, want the deprecated block", got)
	}
	if got := (AgentSpec{}).PolicyBlock(); got != nil {
		t.Fatalf("AgentSpec.PolicyBlock() = %#v, want nil for an undeclared policy", got)
	}

	normalizedPolicy := &NormalizedSandboxSpec{StoppedRuntimePolicy: domain.StoppedRuntimePolicyRemove}
	normalizedLegacy := &NormalizedSandboxSpec{StoppedRuntimePolicy: domain.StoppedRuntimePolicyRetain}
	if got := (NormalizedAgentSpec{Policy: normalizedPolicy, Sandbox: normalizedLegacy}).PolicyBlock(); got != normalizedPolicy {
		t.Fatalf("NormalizedAgentSpec.PolicyBlock() = %#v, want the policy block", got)
	}
	if got := (NormalizedAgentSpec{Sandbox: normalizedLegacy}).PolicyBlock(); got != normalizedLegacy {
		t.Fatalf("NormalizedAgentSpec.PolicyBlock() = %#v, want the deprecated block", got)
	}
	if got := (NormalizedAgentSpec{}).PolicyBlock(); got != nil {
		t.Fatalf("NormalizedAgentSpec.PolicyBlock() = %#v, want nil for an undeclared policy", got)
	}
}

// A project that still writes the deprecated key must be reportable before
// normalization resolves it away, and the report must name both the key used and
// the key that replaces it.
func TestDeprecatedPolicyBlocksReportsTheDeprecatedKey(t *testing.T) {
	spec := mustParseCompose(t, `
name: deprecation
agents:
  zulu:
    sandbox:
      stopped_runtime_policy: retain
  alpha:
    sandbox:
      network:
        default: deny-all
  current:
    policy:
      stopped_runtime_policy: remove
`)

	blocks := DeprecatedPolicyBlocks(spec)
	if len(blocks) != 2 {
		t.Fatalf("DeprecatedPolicyBlocks returned %#v, want the two agents using the deprecated key", blocks)
	}
	// Ordered by agent name, so the report is deterministic.
	if blocks[0].Path != "agents.alpha.sandbox" || blocks[1].Path != "agents.zulu.sandbox" {
		t.Fatalf("DeprecatedPolicyBlocks = %#v, want agents.alpha then agents.zulu", blocks)
	}
	if blocks[0].Agent != "agents.alpha" {
		t.Fatalf("Agent = %q, want agents.alpha", blocks[0].Agent)
	}
	if blocks[0].Current != "agents.alpha.policy" {
		t.Fatalf("Current = %q, want agents.alpha.policy", blocks[0].Current)
	}
}

func TestDeprecatedPolicyBlocksIsQuietForTheCurrentKey(t *testing.T) {
	spec := mustParseCompose(t, `
name: no-deprecation
agents:
  worker:
    policy:
      stopped_runtime_policy: retain
`)
	if blocks := DeprecatedPolicyBlocks(spec); len(blocks) != 0 {
		t.Fatalf("DeprecatedPolicyBlocks = %#v, want none when the project uses the current key", blocks)
	}
	if blocks := DeprecatedPolicyBlocks(nil); blocks != nil {
		t.Fatalf("DeprecatedPolicyBlocks(nil) = %#v, want nil", blocks)
	}
}

// A project policy is a library entry: an agent that names one must enforce
// exactly what an agent that repeats the block enforces, including the spec
// hash, so switching a project to references cannot change what runs.
func TestProjectPolicyReferenceMatchesAnInlineDeclaration(t *testing.T) {
	const network = `
      network:
        default: deny-all
        allow:
          - host: api.github.com
            port: 443
`
	referenced := mustNormalizeCompose(t, `
name: policy-reference
policies:
  locked-down:
`+network+`
agents:
  worker:
    provider: codex
    policy: locked-down
`, nil)
	inline := mustNormalizeCompose(t, `
name: policy-reference
agents:
  worker:
    provider: codex
    policy:
`+network, nil)

	referencedJSON, err := referenced.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	inlineJSON, err := inline.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if string(referencedJSON) != string(inlineJSON) {
		t.Fatalf("a named policy normalized differently:\nreferenced: %s\ninline:     %s", referencedJSON, inlineJSON)
	}
	if got, want := mustHash(t, referenced), mustHash(t, inline); got != want {
		t.Fatalf("hash = %s, want %s for the same effective policy", got, want)
	}
	selected := referenced.Agents[0].PolicyBlock()
	if selected == nil || selected.Network == nil || selected.Network.Default != SandboxNetworkDefaultDenyAll {
		t.Fatalf("selected policy = %#v, want the declared deny policy", selected)
	}
	// The library itself is an authoring convenience; the agent carries the
	// effective block, which is what the engine enforces.
	if len(selected.Network.Allow) != 1 || selected.Network.Allow[0].Host != "api.github.com" {
		t.Fatalf("selected policy allow = %#v, want the declared allowance", selected.Network.Allow)
	}
}

// The deprecated key accepts the same shapes as the current one, so a project
// can move to `policy` and to references independently.
func TestProjectPolicyReferenceWorksUnderTheDeprecatedKey(t *testing.T) {
	spec := mustNormalizeCompose(t, `
name: policy-reference
policies:
  keep:
    stopped_runtime_policy: retain
agents:
  worker:
    provider: codex
    sandbox: keep
`, nil)
	selected := spec.Agents[0].PolicyBlock()
	if selected == nil || selected.StoppedRuntimePolicy != domain.StoppedRuntimePolicyRetain {
		t.Fatalf("selected policy = %#v, want the retained project policy", selected)
	}
}

func TestProjectPolicyErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "undefined name",
			body:    "agents:\n  worker:\n    policy: missing\n",
			wantErr: "agents.worker.policy",
		},
		{
			name:    "empty name",
			body:    "agents:\n  worker:\n    policy: \"\"\n",
			wantErr: "agents.worker.policy",
		},
		{
			name:    "a project policy cannot name another",
			body:    "policies:\n  base: other\nagents:\n  worker:\n    provider: codex\n",
			wantErr: "policies.base",
		},
		{
			name:    "an unselected definition is still validated",
			body:    "policies:\n  bad:\n    network:\n      default: allow\nagents:\n  worker:\n    provider: codex\n",
			wantErr: "policies.bad.network.default",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := composeError(t, "name: policy-errors\n"+tt.body)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want a message naming %q", err, tt.wantErr)
			}
		})
	}
}

// composeError returns the first error a project body produces, whether strict
// parsing or normalization reports it.
func composeError(t *testing.T, raw string) error {
	t.Helper()
	spec, err := Parse([]byte(raw))
	if err != nil {
		return err
	}
	_, err = Normalize(spec, NormalizeOptions{})
	return err
}
