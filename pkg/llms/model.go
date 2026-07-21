package llms

import (
	"net/http"
	"time"
)

const (
	ProviderFamilyOpenAI    = "openai"
	ProviderFamilyAnthropic = "anthropic"

	ProviderScopeSystem     = "system"
	ProviderScopeEnvDefault = "env_default"
	ProviderScopeSessionEnv = "session_env"
	ProviderScopeFacadeEnv  = "facade_env"

	ProviderIDDefaultOpenAI    = "default"
	ProviderIDDefaultAnthropic = "anthropic"

	APIProtocolResponses       = "responses"
	APIProtocolChatCompletions = "chat_completions"
	APIProtocolMessages        = "messages"
)

type Provider struct {
	ID                           string
	Name                         string
	ProviderType                 string
	DefaultWireAPI               string
	BaseURL                      string
	APIKey                       string
	AuthHeader                   string
	AuthScheme                   string
	HeadersJSON                  string
	UseGenericResponsesTextParts bool
	Weight                       int
	Enabled                      bool
	Scope                        string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

type Model struct {
	ID           string
	Name         string
	Description  string
	DefaultModel bool
	Enabled      bool
	Scope        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ResolvedTarget struct {
	Provider Provider
	Model    Model
	WireAPI  string
	Endpoint string
	Headers  http.Header
}

// FacadeEnvironment is a sparse, token-owned Provider Env layer. Empty fields
// inherit from the current Global Env and daemon configuration at request time.
type FacadeEnvironment struct {
	ProviderID   string
	ProviderType string
	Endpoint     string
	Protocol     string
	APIKey       string
	AuthHeader   string
	AuthScheme   string
}

// FacadeTarget is the upstream selected while preparing one guest execution.
// Environment is non-nil only when provider selection reached env bootstrap.
type FacadeTarget struct {
	Target      ResolvedTarget
	Environment *FacadeEnvironment
}

type FacadeToken struct {
	SandboxID        string
	TokenHash        string
	TokenFingerprint string
	Model            string
	ProviderID       string
	WireAPI          string
	Source           string
	RunID            string
	IssuedAt         time.Time
	ExpiresAt        time.Time
	RevokedAt        time.Time
}

// FacadeGrant persists a scoped token and its optional token-owned Provider Env
// layer atomically. System-provider grants have no Environment.
type FacadeGrant struct {
	Token       FacadeToken
	Environment *FacadeEnvironment
}
