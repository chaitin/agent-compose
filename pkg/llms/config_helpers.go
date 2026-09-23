package llms

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"

	"github.com/chaitin/agent-compose/pkg/storedtime"
)

// firstNonEmptyTrimmed returns the first value that is non-empty after
// trimming, returning the trimmed form. It is intentionally distinct from
// firstNonEmpty (which returns the raw value) because the LLM resolution paths
// normalize the value they finally use.
func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func ScanProvider(scan func(dest ...any) error) (Provider, error) {
	var item Provider
	var genericResponsesTextParts, enabled int
	var auth string
	var createdAt, updatedAt int64
	if err := scan(&item.ID, &item.Name, &item.ProviderType, &item.DefaultWireAPI, &item.BaseURL, &item.APIKey, &item.AuthHeader, &item.AuthScheme, &auth, &item.HeadersJSON, &genericResponsesTextParts, &item.Weight, &enabled, &item.Scope, &createdAt, &updatedAt); err != nil {
		return Provider{}, err
	}
	item.Auth = ProviderAuth(strings.TrimSpace(auth))
	item.UseGenericResponsesTextParts = genericResponsesTextParts != 0
	item.Enabled = enabled != 0
	item.ProviderType = NormalizeProviderType(item.ProviderType)
	item.DefaultWireAPI = NormalizeWireAPI(item.DefaultWireAPI)
	item.CreatedAt = storedtime.ParseStoredTime(createdAt)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAt)
	return item, nil
}

func ScanModel(scan func(dest ...any) error) (Model, error) {
	var item Model
	var defaultModel, enabled int
	var createdAt, updatedAt int64
	if err := scan(&item.ID, &item.Name, &item.Description, &defaultModel, &enabled, &item.Scope, &createdAt, &updatedAt); err != nil {
		return Model{}, err
	}
	item.DefaultModel = defaultModel != 0
	item.Enabled = enabled != 0
	item.CreatedAt = storedtime.ParseStoredTime(createdAt)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAt)
	return item, nil
}

// FacadeTokenColumns is the column list ScanFacadeToken reads, in order. It
// exists so the store's SELECT and this scanner cannot drift apart.
const FacadeTokenColumns = "sandbox_id, token_hash, token_fingerprint, model, provider_id, wire_api, guest_model, source, run_id, issued_at, expires_at, revoked_at"

func ScanFacadeToken(scan func(dest ...any) error) (FacadeToken, error) {
	var item FacadeToken
	var issuedAt, expiresAt, revokedAt int64
	if err := scan(&item.SandboxID, &item.TokenHash, &item.TokenFingerprint, &item.Model, &item.ProviderID, &item.WireAPI, &item.GuestModel, &item.Source, &item.RunID, &issuedAt, &expiresAt, &revokedAt); err != nil {
		return FacadeToken{}, err
	}
	item.IssuedAt = storedtime.ParseStoredTime(issuedAt)
	item.ExpiresAt = storedtime.ParseStoredTime(expiresAt)
	item.RevokedAt = storedtime.ParseStoredTime(revokedAt)
	return item, nil
}

func NormalizeDefaultConfig(provider Provider, model Model) (Provider, Model, bool) {
	provider.ID = firstNonEmpty(strings.TrimSpace(provider.ID), ProviderIDDefaultOpenAI)
	provider.Name = firstNonEmpty(strings.TrimSpace(provider.Name), "default")
	provider.ProviderType = NormalizeProviderType(provider.ProviderType)
	provider.DefaultWireAPI = NormalizeWireAPI(provider.DefaultWireAPI)
	if provider.ProviderType == ProviderFamilyAnthropic {
		provider.BaseURL = NormalizeAnthropicAPIBaseURL(provider.BaseURL)
	} else {
		provider.BaseURL = NormalizeAPIBaseURL(provider.BaseURL, provider.DefaultWireAPI)
	}
	provider.AuthHeader = firstNonEmpty(strings.TrimSpace(provider.AuthHeader), "Authorization")
	provider.AuthScheme = strings.TrimSpace(provider.AuthScheme)
	if provider.AuthScheme == "" && strings.EqualFold(provider.AuthHeader, "Authorization") {
		provider.AuthScheme = "Bearer"
	}
	provider.HeadersJSON = firstNonEmpty(strings.TrimSpace(provider.HeadersJSON), "{}")
	if provider.Weight == 0 {
		provider.Weight = 10
	}
	provider.Scope = firstNonEmpty(strings.TrimSpace(provider.Scope), ProviderScopeSystem)

	model.ID = firstNonEmpty(strings.TrimSpace(model.ID), strings.TrimSpace(model.Name))
	model.Name = firstNonEmpty(strings.TrimSpace(model.Name), model.ID)
	model.Enabled = true
	model.Scope = firstNonEmpty(strings.TrimSpace(model.Scope), ProviderScopeSystem)
	return provider, model, model.ID != "" && model.Name != ""
}

func NormalizeWireAPI(value string) string {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_") {
	case "", APIProtocolResponses:
		return APIProtocolResponses
	case "chat", "chat_completion", APIProtocolChatCompletions:
		return APIProtocolChatCompletions
	case "message", "messages", APIProtocolMessages:
		return APIProtocolMessages
	default:
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	}
}

func NormalizeAPIEndpointForProtocol(raw, protocol string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	normalizedProtocol := NormalizeWireAPI(protocol)
	if normalizedProtocol == APIProtocolChatCompletions && (strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/") {
		parsed.Path = "/v1/chat/completions"
		return parsed.String()
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	if normalizedProtocol == APIProtocolChatCompletions && strings.HasSuffix(cleanPath, "/v1") {
		parsed.Path = pathpkg.Join(parsed.Path, "/chat/completions")
		return parsed.String()
	}
	if normalizedProtocol == APIProtocolChatCompletions && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/chat/completions")
		return parsed.String()
	}
	if normalizedProtocol == APIProtocolResponses && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
		return parsed.String()
	}
	if normalizedProtocol == APIProtocolResponses && strings.HasSuffix(cleanPath, "/v1") {
		parsed.Path = pathpkg.Join(parsed.Path, "/responses")
		return parsed.String()
	}
	if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
	}
	return parsed.String()
}

func NormalizeProviderType(value string) string {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_") {
	case "", "openai", "openai_compatible":
		return ProviderFamilyOpenAI
	case "anthropic", "claude", "anthropic_messages":
		return ProviderFamilyAnthropic
	default:
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	}
}

func NormalizeAPIBaseURL(raw, wireAPI string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(cleanPath, "/responses"):
		parsed.Path = strings.TrimSuffix(cleanPath, "/responses")
	case strings.HasSuffix(cleanPath, "/chat/completions"):
		parsed.Path = strings.TrimSuffix(cleanPath, "/chat/completions")
	default:
		parsed.Path = cleanPath
	}
	return strings.TrimRight(parsed.String(), "/")
}

func NormalizeAnthropicAPIBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(cleanPath, "/messages"):
		parsed.Path = strings.TrimSuffix(cleanPath, "/messages")
	case cleanPath == "":
		parsed.Path = "/v1"
	default:
		parsed.Path = cleanPath
	}
	return strings.TrimRight(parsed.String(), "/")
}

func EndpointForProvider(provider Provider, wireAPI string) string {
	if NormalizeProviderType(provider.ProviderType) == ProviderFamilyAnthropic {
		baseURL := NormalizeAnthropicAPIBaseURL(provider.BaseURL)
		parsed, err := url.Parse(baseURL)
		if err != nil {
			return strings.TrimRight(baseURL, "/") + "/messages"
		}
		parsed.Path = pathpkg.Join(parsed.Path, "messages")
		return parsed.String()
	}
	if endpointAlreadyMatchesProtocol(provider.BaseURL, wireAPI) {
		return strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	}
	baseURL := NormalizeAPIBaseURL(provider.BaseURL, wireAPI)
	if !ProviderScopeIsConfigured(provider.Scope) {
		return NormalizeAPIEndpointForProtocol(baseURL, wireAPI)
	}
	return AppendAPIEndpointToBaseURL(baseURL, wireAPI)
}

func endpointAlreadyMatchesProtocol(raw, wireAPI string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch NormalizeWireAPI(wireAPI) {
	case APIProtocolChatCompletions:
		return strings.HasSuffix(path, "/chat/completions")
	case APIProtocolResponses:
		return strings.HasSuffix(path, "/responses")
	default:
		return false
	}
}

// ProviderScopeIsConfigured reports whether a connection's stored base URL is a
// complete endpoint that needs no protocol path appended. The daemon
// environment projection stores a bare base URL and is the only scope that is
// not an operator-configured endpoint.
func ProviderScopeIsConfigured(scope string) bool {
	return strings.TrimSpace(scope) != ProviderScopeEnvDefault
}

func ProviderForwardHeaders(provider Provider) (http.Header, error) {
	headers := http.Header{}
	if raw := strings.TrimSpace(provider.HeadersJSON); raw != "" && raw != "{}" {
		custom := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &custom); err != nil {
			return nil, fmt.Errorf("decode llm provider headers: %w", err)
		}
		for key, value := range custom {
			if ForbiddenProviderHeader(key, provider.AuthHeader) {
				continue
			}
			headers.Set(strings.TrimSpace(key), value)
		}
	}
	authHeader := firstNonEmpty(strings.TrimSpace(provider.AuthHeader), "Authorization")
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey != "" {
		if scheme := strings.TrimSpace(provider.AuthScheme); scheme != "" {
			headers.Set(authHeader, scheme+" "+apiKey)
		} else {
			headers.Set(authHeader, apiKey)
		}
	}
	return headers, nil
}

func ForbiddenProviderHeader(name, authHeader string) bool {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if canonical == "" || canonical == strings.ToLower(strings.TrimSpace(authHeader)) {
		return true
	}
	switch canonical {
	case "authorization", "proxy-authorization", "host", "content-length", "content-type", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func AppendAPIEndpointToBaseURL(baseURL, wireAPI string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		switch NormalizeWireAPI(wireAPI) {
		case APIProtocolChatCompletions:
			return baseURL + "/v1/chat/completions"
		default:
			return baseURL + "/v1/responses"
		}
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	switch NormalizeWireAPI(wireAPI) {
	case APIProtocolChatCompletions:
		if cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/v1") {
			joinAPIBasePath(parsed, cleanPath, "chat/completions")
		} else {
			joinAPIBasePath(parsed, cleanPath, "v1/chat/completions")
		}
	default:
		if cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/v1") {
			joinAPIBasePath(parsed, cleanPath, "responses")
		} else {
			joinAPIBasePath(parsed, cleanPath, "v1/responses")
		}
	}
	return parsed.String()
}

func joinAPIBasePath(parsed *url.URL, basePath, suffix string) {
	if parsed == nil {
		return
	}
	joined := pathpkg.Join(basePath, suffix)
	if parsed.Host != "" && !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	parsed.Path = joined
}
