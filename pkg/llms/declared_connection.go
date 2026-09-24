package llms

import (
	"net/url"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// DeclaredConnectionPrefix marks a connection the daemon derived from an
// agent's own environment declaration rather than from operator configuration.
//
// The prefix is reserved: a managed provider id may not contain a colon, so an
// operator can neither address one of these rows through the public LLM service
// nor collide with one.
const DeclaredConnectionPrefix = "session-env:"

// ProviderScopeDeclared is the scope of a connection derived from an
// environment declaration. Like env_default it is not operator configuration.
const ProviderScopeDeclared = "declared"

// providerFamilyGoogle is the family of the Google Generative Language API. The
// daemon cannot proxy it today, but recognizing the credential lets a project
// check report the exposure instead of passing it through silently.
const providerFamilyGoogle = "google"

// DeclaredConnectionID is the deterministic id of the connection a sandbox
// declared for one provider family. It is per sandbox and family so concurrent
// runs of one sandbox rewrite a single row while different sandboxes never
// share a credential.
func DeclaredConnectionID(sandboxID, family string) string {
	return DeclaredConnectionPrefix + strings.TrimSpace(sandboxID) + ":" + strings.TrimSpace(family)
}

// IsDeclaredConnectionID reports whether id names a connection the daemon
// derived from an agent declaration.
func IsDeclaredConnectionID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), DeclaredConnectionPrefix)
}

// declaredCredentialSpec is one vendor's first-party credential convention.
type declaredCredentialSpec struct {
	// EnvNames carry the credential, most specific first.
	EnvNames []string
	// Family is the connection family the credential is served over.
	Family string
	// Protocol is the wire protocol the vendor's first-party endpoint speaks.
	Protocol Protocol
	// Auth is the credential presentation the vendor expects. It is explicit
	// because a bearer token and an API key use different headers on the same
	// Anthropic Messages protocol.
	Auth ProviderAuth
	// Endpoint is the vendor's first-party base URL used when the declaration
	// names none.
	Endpoint string
	// EndpointEnvNames override Endpoint.
	EndpointEnvNames []string
	// OfficialHosts are the hosts that count as the vendor's first party.
	OfficialHosts []string
	// Absorbable reports whether the daemon can proxy this credential. A
	// credential the daemon cannot proxy is still recognized, so a project check
	// can warn about it, but it is not turned into a connection.
	Absorbable bool
}

// declaredCredentialSpecs is the daemon's recognition of first-party LLM
// credentials. Order matters only between names that can both be set for one
// vendor: the API key is the more specific declaration, so it wins over a
// bearer token.
var declaredCredentialSpecs = []declaredCredentialSpec{
	{
		EnvNames:         []string{"ANTHROPIC_API_KEY"},
		Family:           ProviderFamilyAnthropic,
		Protocol:         ProtocolMessages,
		Auth:             ProviderAuthXAPIKey,
		Endpoint:         "https://api.anthropic.com",
		EndpointEnvNames: []string{"ANTHROPIC_BASE_URL", "LLM_API_ENDPOINT"},
		OfficialHosts:    []string{"api.anthropic.com"},
		Absorbable:       true,
	},
	{
		EnvNames:         []string{"ANTHROPIC_AUTH_TOKEN"},
		Family:           ProviderFamilyAnthropic,
		Protocol:         ProtocolMessages,
		Auth:             ProviderAuthBearer,
		Endpoint:         "https://api.anthropic.com",
		EndpointEnvNames: []string{"ANTHROPIC_BASE_URL", "LLM_API_ENDPOINT"},
		OfficialHosts:    []string{"api.anthropic.com"},
		Absorbable:       true,
	},
	{
		EnvNames:         []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
		Family:           ProviderFamilyOpenAI,
		Protocol:         ProtocolResponses,
		Auth:             ProviderAuthBearer,
		Endpoint:         "https://api.openai.com",
		EndpointEnvNames: []string{"OPENAI_BASE_URL", "LLM_API_ENDPOINT"},
		OfficialHosts:    []string{"api.openai.com"},
		Absorbable:       true,
	},
	{
		EnvNames:         []string{"DEEPSEEK_API_KEY"},
		Family:           ProviderFamilyOpenAI,
		Protocol:         ProtocolChatCompletions,
		Auth:             ProviderAuthBearer,
		Endpoint:         "https://api.deepseek.com",
		EndpointEnvNames: []string{"DEEPSEEK_BASE_URL", "LLM_API_ENDPOINT"},
		OfficialHosts:    []string{"api.deepseek.com"},
		Absorbable:       true,
	},
	{
		EnvNames:         []string{"OPENROUTER_API_KEY"},
		Family:           ProviderFamilyOpenAI,
		Protocol:         ProtocolChatCompletions,
		Auth:             ProviderAuthBearer,
		Endpoint:         "https://openrouter.ai/api/v1",
		EndpointEnvNames: []string{"OPENROUTER_BASE_URL", "LLM_API_ENDPOINT"},
		OfficialHosts:    []string{"openrouter.ai"},
		Absorbable:       true,
	},
	{
		EnvNames:   []string{"AZURE_OPENAI_API_KEY"},
		Family:     ProviderFamilyOpenAI,
		Protocol:   ProtocolChatCompletions,
		Auth:       ProviderAuthBearer,
		Absorbable: false,
	},
	{
		EnvNames:         []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"},
		Family:           providerFamilyGoogle,
		Protocol:         ProtocolChatCompletions,
		Auth:             ProviderAuthBearer,
		Endpoint:         "https://generativelanguage.googleapis.com",
		EndpointEnvNames: []string{"LLM_API_ENDPOINT"},
		OfficialHosts:    []string{"generativelanguage.googleapis.com"},
		Absorbable:       false,
	},
}

// DeclaredCredential classifies one LLM credential an operator declared in
// project or agent environment.
type DeclaredCredential struct {
	// EnvName is the variable that carries the credential.
	EnvName string
	// Family is the vendor family the credential belongs to.
	Family string
	// Endpoint is the endpoint the credential would be presented to: the
	// declared override when there is one, otherwise the vendor's first-party
	// base URL. It is empty for a credential whose endpoint is always declared.
	Endpoint string
	// Official reports that Endpoint is the vendor's own first-party host, so
	// the traffic would go to the vendor rather than to a gateway or a private
	// deployment.
	Official bool
	// Absorbed reports that the daemon proxies this credential: the declaration
	// becomes a daemon-owned connection and the guest receives only a run-scoped
	// facade token.
	Absorbed bool
}

// declaredCredential is a DeclaredCredential plus the secret it classifies and
// the wire convention it is served over. The secret stays off the exported
// struct so a caller that only needs to classify a declaration cannot
// accidentally carry the key into output or an error.
type declaredCredential struct {
	DeclaredCredential
	protocol Protocol
	auth     ProviderAuth
	apiKey   string
}

// ClassifyDeclaredLLMCredential reports the first-party LLM credential an
// environment declares, if any. It reads the declaration the same way a run
// does, so a project check and a run cannot disagree about what was declared.
func ClassifyDeclaredLLMCredential(items []domain.SandboxEnvVar) (DeclaredCredential, bool) {
	credential, ok := recognizeDeclaredCredential(items, "")
	if !ok {
		return DeclaredCredential{}, false
	}
	return credential.DeclaredCredential, true
}

// ClassifyDeclaredLLMCredentials reports every first-party LLM credential an
// environment declares. A project may declare more than one — an operator can
// keep an Anthropic and an OpenAI key side by side — and a check that reports
// only the first would leave the others unmentioned.
func ClassifyDeclaredLLMCredentials(items []domain.SandboxEnvVar) []DeclaredCredential {
	recognized := recognizeDeclaredCredentials(items, "")
	credentials := make([]DeclaredCredential, 0, len(recognized))
	for _, credential := range recognized {
		credentials = append(credentials, credential.DeclaredCredential)
	}
	return credentials
}

// UnprotectedCredentialEnvName reports whether name carries a credential the
// daemon cannot classify. Such a value is passed through to the agent runtime
// unchanged, so a project check warns about it rather than implying protection.
func UnprotectedCredentialEnvName(name string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(name))
	return strings.HasSuffix(normalized, "_API_KEY") || strings.HasSuffix(normalized, "_AUTH_TOKEN")
}

// recognizeDeclaredCredential is the credential a run uses: the most specific
// declaration the environment publishes.
func recognizeDeclaredCredential(items []domain.SandboxEnvVar, canonical Protocol) (declaredCredential, bool) {
	credentials := recognizeDeclaredCredentials(items, canonical)
	if len(credentials) == 0 {
		return declaredCredential{}, false
	}
	return credentials[0], true
}

// recognizeDeclaredCredentials returns every declaration in the daemon's
// precedence order: the vendor-specific names first, then the vendor-neutral
// LLM_API_KEY. canonical is the protocol the agent speaks when a generic
// declaration names none, which keeps a vendor-neutral credential pointed at the
// family the run would otherwise use.
func recognizeDeclaredCredentials(items []domain.SandboxEnvVar, canonical Protocol) []declaredCredential {
	if len(items) == 0 {
		return nil
	}
	var credentials []declaredCredential
	for _, spec := range declaredCredentialSpecs {
		for _, name := range spec.EnvNames {
			key := strings.TrimSpace(EnvItemValue(items, name))
			if key == "" {
				continue
			}
			credentials = append(credentials, newDeclaredCredential(spec, name, key, envItemFirst(items, spec.EndpointEnvNames...)))
			break
		}
	}
	if generic, ok := genericDeclaredCredential(items, canonical); ok {
		credentials = append(credentials, generic)
	}
	return credentials
}

func newDeclaredCredential(spec declaredCredentialSpec, envName, apiKey, declaredEndpoint string) declaredCredential {
	endpoint := strings.TrimRight(strings.TrimSpace(declaredEndpoint), "/")
	if endpoint == "" {
		endpoint = spec.Endpoint
	}
	return declaredCredential{
		DeclaredCredential: DeclaredCredential{
			EnvName:  strings.ToUpper(strings.TrimSpace(envName)),
			Family:   spec.Family,
			Endpoint: endpoint,
			Official: isOfficialEndpoint(endpoint, spec.OfficialHosts),
			Absorbed: spec.Absorbable && endpoint != "",
		},
		protocol: spec.Protocol,
		auth:     spec.Auth,
		apiKey:   apiKey,
	}
}

// genericDeclaredCredential handles LLM_API_KEY, the vendor-neutral declaration
// an operator may point at any OpenAI- or Anthropic-compatible upstream. The
// protocol decides which family serves it, because the header that carries the
// credential differs between the two. An undeclared protocol falls back to the
// protocol the agent speaks, which is what the CLI would have picked.
func genericDeclaredCredential(items []domain.SandboxEnvVar, canonical Protocol) (declaredCredential, bool) {
	key := envItemFirst(items, "LLM_API_KEY")
	if key == "" {
		return declaredCredential{}, false
	}
	// NormalizeProtocol maps an empty string onto responses, so an undeclared
	// protocol has to be distinguished before normalizing: the agent's own
	// protocol is the right default, not always responses.
	var protocol Protocol
	if raw := envItemFirst(items, "LLM_API_PROTOCOL"); raw != "" {
		if declared := NormalizeProtocol(raw); declared.Valid() {
			protocol = declared
		}
	}
	if !protocol.Valid() {
		protocol = canonical
	}
	if !protocol.Valid() {
		protocol = ProtocolResponses
	}
	spec := declaredCredentialSpec{
		Family:           ProviderFamilyOpenAI,
		Protocol:         protocol,
		Auth:             ProviderAuthBearer,
		Endpoint:         "https://api.openai.com",
		EndpointEnvNames: []string{"LLM_API_ENDPOINT"},
		OfficialHosts:    []string{"api.openai.com"},
		Absorbable:       true,
	}
	if protocol == ProtocolMessages {
		spec.Family = ProviderFamilyAnthropic
		spec.Auth = ProviderAuthXAPIKey
		spec.Endpoint = "https://api.anthropic.com"
		spec.OfficialHosts = []string{"api.anthropic.com"}
	}
	return newDeclaredCredential(spec, "LLM_API_KEY", key, envItemFirst(items, "LLM_API_ENDPOINT")), true
}

func isOfficialEndpoint(endpoint string, hosts []string) bool {
	if strings.TrimSpace(endpoint) == "" || len(hosts) == 0 {
		return false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, official := range hosts {
		if host == strings.ToLower(official) {
			return true
		}
	}
	return false
}

// DeclaredUpstream is the upstream an agent declared in its own environment,
// resolved into the connection the daemon persists to proxy it.
type DeclaredUpstream struct {
	// Provider is the run-scoped connection that owns the credential. Its id is
	// reserved, so the connection is addressable only by the run that declared
	// it.
	Provider Provider
	// Model is the model the declaration names, or "" when it names none.
	Model string
	// Credential is the classification the run and a project check share.
	Credential DeclaredCredential
}

// DeclaredUpstreamFromAgentEnv reports the first-party upstream an agent
// declared in its own environment, as a connection the daemon owns.
//
// A credential the daemon cannot proxy is not reported: the caller falls back
// to the catalog, and the driver environment layer keeps the credential out of
// the guest either way.
func DeclaredUpstreamFromAgentEnv(sandboxID string, env []domain.SandboxEnvVar, dialect Dialect, requestedModel string) (DeclaredUpstream, bool) {
	credential, ok := recognizeDeclaredCredential(env, dialect.Canonical)
	if !ok || !credential.Absorbed {
		return DeclaredUpstream{}, false
	}
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = declaredModelFromEnv(dialect.Kind, env)
	}
	authHeader, authScheme := ProviderAuthWire(credential.auth)
	return DeclaredUpstream{
		Provider: Provider{
			ID:             DeclaredConnectionID(sandboxID, credential.Family),
			Name:           "declared " + credential.Family,
			ProviderType:   credential.Family,
			DefaultWireAPI: string(credential.protocol),
			BaseURL:        credential.Endpoint,
			APIKey:         credential.apiKey,
			AuthHeader:     authHeader,
			AuthScheme:     authScheme,
			HeadersJSON:    ManagedProviderHeadersJSON(string(credential.protocol)),
			Enabled:        true,
			Scope:          ProviderScopeDeclared,
		},
		Model:      model,
		Credential: credential.DeclaredCredential,
	}, true
}

// NormalizeDeclaredConnection fills the conventions of a connection derived
// from an agent declaration. It mirrors the normalization the environment
// bootstrap applies, so a declared upstream is stored the way an operator
// configured one would be.
func NormalizeDeclaredConnection(provider Provider) Provider {
	provider.ProviderType = NormalizeProviderType(provider.ProviderType)
	provider.DefaultWireAPI = NormalizeWireAPI(provider.DefaultWireAPI)
	if provider.ProviderType == ProviderFamilyAnthropic {
		provider.BaseURL = NormalizeAnthropicAPIBaseURL(provider.BaseURL)
	} else {
		provider.BaseURL = NormalizeAPIBaseURL(provider.BaseURL, provider.DefaultWireAPI)
	}
	header, scheme := ProviderProtocolAuth(provider.DefaultWireAPI)
	provider.AuthHeader = firstNonEmpty(strings.TrimSpace(provider.AuthHeader), header)
	provider.AuthScheme = firstNonEmpty(strings.TrimSpace(provider.AuthScheme), scheme)
	provider.HeadersJSON = firstNonEmpty(strings.TrimSpace(provider.HeadersJSON), "{}")
	provider.Name = firstNonEmpty(strings.TrimSpace(provider.Name), "declared")
	provider.Scope = firstNonEmpty(strings.TrimSpace(provider.Scope), ProviderScopeDeclared)
	provider.Enabled = true
	return provider
}

// declaredModelFromEnv reads the model an agent declares in its own
// environment. The agent's own key comes first because it is the most specific,
// and the generic names follow because a generic LLM_* environment may drive
// any of these CLIs.
func declaredModelFromEnv(kind string, env []domain.SandboxEnvVar) string {
	if model := envItemFirst(env, agentSpecificModelKeys(kind)...); model != "" {
		return model
	}
	return envItemFirst(env, "LLM_MODEL", "OPENAI_MODEL")
}

func agentSpecificModelKeys(kind string) []string {
	switch kind {
	case "codex":
		return []string{"CODEX_MODEL"}
	case "claude":
		return []string{"ANTHROPIC_MODEL", "CLAUDE_MODEL"}
	case "opencode":
		return []string{"OPENCODE_MODEL"}
	default:
		return nil
	}
}

// envItemFirst returns the first non-empty value among the named environment
// items, in the order given, or "" when none is set.
func envItemFirst(items []domain.SandboxEnvVar, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(EnvItemValue(items, name)); value != "" {
			return value
		}
	}
	return ""
}
