package llm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	driverpkg "agent-compose/internal/driver"
)

const (
	APIProtocolResponses       = "responses"
	APIProtocolChatCompletions = "chat_completions"
	APIProtocolMessages        = "messages"

	ProviderFamilyOpenAI    = "openai"
	ProviderFamilyAnthropic = "anthropic"

	ProviderScopeSystem     = "system"
	ProviderScopeEnvDefault = "env_default"
	ProviderScopeSessionEnv = "session_env"
)

type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

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

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	if protocol == APIProtocolChatCompletions && (strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/") {
		parsed.Path = "/v1/chat/completions"
		return parsed.String()
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	if protocol == APIProtocolChatCompletions && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
		parsed.Path = pathpkg.Join(parsed.Path, "/chat/completions")
		return parsed.String()
	}
	if protocol == APIProtocolChatCompletions && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/chat/completions")
		return parsed.String()
	}
	if protocol == APIProtocolResponses && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
		return parsed.String()
	}
	if protocol == APIProtocolResponses && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
		parsed.Path = pathpkg.Join(parsed.Path, "/responses")
		return parsed.String()
	}
	if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
	}
	return parsed.String()
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
	authHeader := FirstNonEmpty(strings.TrimSpace(provider.AuthHeader), "Authorization")
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

func ProviderScopeIsConfigured(scope string) bool {
	switch strings.TrimSpace(scope) {
	case ProviderScopeEnvDefault, ProviderScopeSessionEnv:
		return false
	default:
		return true
	}
}

func NewFacadeToken(sessionID, model, providerID, wireAPI, source, runID string) (string, FacadeToken, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", FacadeToken{}, err
	}
	tokenValue := "ac_llm_" + hex.EncodeToString(raw)
	hash, fingerprint := HashFacadeToken(tokenValue)
	now := time.Now().UTC()
	return tokenValue, FacadeToken{
		SessionID:        strings.TrimSpace(sessionID),
		TokenHash:        hash,
		TokenFingerprint: fingerprint,
		Model:            strings.TrimSpace(model),
		ProviderID:       strings.TrimSpace(providerID),
		WireAPI:          NormalizeWireAPI(wireAPI),
		Source:           strings.TrimSpace(source),
		RunID:            strings.TrimSpace(runID),
		IssuedAt:         now,
	}, nil
}

func HashFacadeToken(value string) (string, string) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
}

func ProviderKeyName(name string) bool {
	return driverpkg.LLMProviderKeyName(name)
}

func NormalizeEnvItems(items []EnvVar) []EnvVar {
	if len(items) == 0 {
		return nil
	}
	merged := make(map[string]EnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		item.Name = name
		merged[name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func LookupEnvItemValue(items []EnvVar, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	for _, item := range NormalizeEnvItems(items) {
		if strings.EqualFold(strings.TrimSpace(item.Name), key) {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}

func FilterPersistedRuntimeEnv(items []EnvVar) []EnvVar {
	result := make([]EnvVar, 0, len(items))
	for _, item := range NormalizeEnvItems(items) {
		if ProviderKeyName(item.Name) {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func RuntimeEnvMap(items []EnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range NormalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" || ProviderKeyName(name) {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func ManagedRuntimeEnvMap(items []EnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range NormalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func MergeManagedExecEnv(base map[string]string, managed map[string]string) map[string]string {
	if len(base) == 0 && len(managed) == 0 {
		return nil
	}
	result := make(map[string]string, len(base)+len(managed))
	for key, value := range base {
		if ProviderKeyName(key) {
			continue
		}
		result[key] = value
	}
	for key, value := range managed {
		result[key] = value
	}
	return result
}

func EnvItemsFromMap(values map[string]string, secret bool) []EnvVar {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]EnvVar, 0, len(keys))
	for _, key := range keys {
		items = append(items, EnvVar{Name: key, Value: values[key], Secret: secret})
	}
	return items
}
