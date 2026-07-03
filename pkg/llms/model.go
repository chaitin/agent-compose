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

type FacadeToken struct {
	SessionID        string
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
