package llms

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"

	"agent-compose/pkg/agentcompose/configstore"
)

func ScanProvider(scan func(dest ...any) error) (Provider, error) {
	var item Provider
	var genericResponsesTextParts, enabled int
	var createdAt, updatedAt int64
	if err := scan(&item.ID, &item.Name, &item.ProviderType, &item.DefaultWireAPI, &item.BaseURL, &item.APIKey, &item.AuthHeader, &item.AuthScheme, &item.HeadersJSON, &genericResponsesTextParts, &item.Weight, &enabled, &item.Scope, &createdAt, &updatedAt); err != nil {
		return Provider{}, err
	}
	item.UseGenericResponsesTextParts = genericResponsesTextParts != 0
	item.Enabled = enabled != 0
	item.ProviderType = NormalizeProviderType(item.ProviderType)
	item.DefaultWireAPI = NormalizeWireAPI(item.DefaultWireAPI)
	item.CreatedAt = configstore.ParseStoredTime(createdAt)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAt)
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
	item.CreatedAt = configstore.ParseStoredTime(createdAt)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAt)
	return item, nil
}

func ScanFacadeToken(scan func(dest ...any) error) (FacadeToken, error) {
	var item FacadeToken
	var issuedAt, expiresAt, revokedAt int64
	if err := scan(&item.SessionID, &item.TokenHash, &item.TokenFingerprint, &item.Model, &item.ProviderID, &item.WireAPI, &item.Source, &item.RunID, &issuedAt, &expiresAt, &revokedAt); err != nil {
		return FacadeToken{}, err
	}
	item.IssuedAt = configstore.ParseStoredTime(issuedAt)
	item.ExpiresAt = configstore.ParseStoredTime(expiresAt)
	item.RevokedAt = configstore.ParseStoredTime(revokedAt)
	return item, nil
}

func NormalizeWireAPI(value string) string {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_") {
	case "", APIProtocolResponses:
		return APIProtocolResponses
	case "chat", "chat_completion", APIProtocolChatCompletions:
		return APIProtocolChatCompletions
	case "message", APIProtocolMessages:
		return APIProtocolMessages
	default:
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	}
}

func NormalizeAPIEndpoint(raw string) string {
	return NormalizeAPIEndpointForProtocol(raw, APIProtocolResponses)
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
	if normalizedProtocol == APIProtocolChatCompletions && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
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
	if normalizedProtocol == APIProtocolResponses && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
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

func NormalizeOptionalProviderType(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return NormalizeProviderType(value)
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
	baseURL := NormalizeAPIBaseURL(provider.BaseURL, wireAPI)
	if !ProviderScopeIsConfigured(provider.Scope) {
		return NormalizeAPIEndpointForProtocol(baseURL, wireAPI)
	}
	return AppendAPIEndpointToBaseURL(baseURL, wireAPI)
}

func ProviderScopeIsConfigured(scope string) bool {
	switch strings.TrimSpace(scope) {
	case ProviderScopeEnvDefault, ProviderScopeSessionEnv:
		return false
	default:
		return true
	}
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
