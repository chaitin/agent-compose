package llm

import (
	modeldomain "agent-compose/internal/model"
	appconfig "agent-compose/pkg/config"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type ConfigStore interface {
	ListGlobalEnv(context.Context) ([]modeldomain.SessionEnvVar, error)
	ListEnabledLLMProviders(context.Context) ([]LLMProvider, error)
	ListEnabledLLMModels(context.Context) ([]LLMModel, error)
	LLMProviderModelWireAPI(context.Context, string, string) (string, bool, error)
}

func resolveLLMTarget(ctx context.Context, config *appconfig.Config, store ConfigStore, requestedModel string) (LLMResolvedTarget, error) {
	if store != nil {
		providers, _ := store.ListEnabledLLMProviders(ctx)
		models, _ := store.ListEnabledLLMModels(ctx)
		model := selectLLMModel(models, requestedModel)
		provider, wireAPI := selectLLMProvider(ctx, store, providers, model.ID)
		if strings.TrimSpace(provider.ID) != "" && strings.TrimSpace(model.Name) != "" {
			headers, err := providerForwardHeaders(provider)
			if err != nil {
				return LLMResolvedTarget{}, err
			}
			wireAPI = firstNonEmpty(wireAPI, provider.DefaultWireAPI, llmAPIProtocolResponses)
			return LLMResolvedTarget{
				Provider: provider,
				Model:    model,
				WireAPI:  normalizeLLMWireAPI(wireAPI),
				Endpoint: llmEndpointForProvider(provider, wireAPI),
				Headers:  headers,
			}, nil
		}
	}
	if config == nil {
		return LLMResolvedTarget{}, fmt.Errorf("llm config is required")
	}
	env := map[string]string{}
	if store != nil {
		items, _ := store.ListGlobalEnv(ctx)
		for _, item := range items {
			name := strings.ToUpper(strings.TrimSpace(item.Name))
			if name != "" {
				env[name] = item.Value
			}
		}
	}
	endpoint := firstNonEmpty(env["LLM_API_ENDPOINT"], config.LLMAPIEndpoint)
	apiKey := firstNonEmpty(env["LLM_API_KEY"], env["OPENAI_API_KEY"], config.LLMAPIKey)
	protocol := firstNonEmpty(env["LLM_API_PROTOCOL"], config.LLMAPIProtocol)
	modelName := strings.TrimSpace(firstNonEmpty(requestedModel, env["LLM_MODEL"], config.LLMModel))
	return LLMResolvedTarget{
		Provider: LLMProvider{
			ID:         "config",
			Name:       "Config",
			BaseURL:    endpoint,
			APIKey:     apiKey,
			AuthHeader: "Authorization",
			AuthScheme: "Bearer",
		},
		Model:    LLMModel{ID: modelName, Name: modelName},
		WireAPI:  normalizeLLMWireAPI(protocol),
		Endpoint: strings.TrimSpace(endpoint),
		Headers:  bearerHeader(apiKey),
	}, nil
}

func selectLLMModel(models []LLMModel, requested string) LLMModel {
	requested = strings.TrimSpace(requested)
	for _, model := range models {
		if !model.Enabled {
			continue
		}
		if requested != "" && (model.ID == requested || model.Name == requested) {
			return model
		}
	}
	for _, model := range models {
		if model.Enabled && model.DefaultModel {
			return model
		}
	}
	for _, model := range models {
		if model.Enabled {
			return model
		}
	}
	if requested != "" {
		return LLMModel{ID: requested, Name: requested, Enabled: true}
	}
	return LLMModel{}
}

func selectLLMProvider(ctx context.Context, store ConfigStore, providers []LLMProvider, modelID string) (LLMProvider, string) {
	for _, provider := range providers {
		if !provider.Enabled {
			continue
		}
		wireAPI := provider.DefaultWireAPI
		if store != nil && strings.TrimSpace(modelID) != "" {
			if value, ok, err := store.LLMProviderModelWireAPI(ctx, provider.ID, modelID); err == nil && ok {
				wireAPI = value
			}
		}
		return provider, wireAPI
	}
	return LLMProvider{}, ""
}

func providerForwardHeaders(provider LLMProvider) (http.Header, error) {
	headers := http.Header{}
	if strings.TrimSpace(provider.HeadersJSON) != "" {
		var raw map[string]string
		if err := json.Unmarshal([]byte(provider.HeadersJSON), &raw); err != nil {
			return nil, err
		}
		for key, value := range raw {
			if strings.TrimSpace(key) != "" {
				headers.Set(key, value)
			}
		}
	}
	if strings.TrimSpace(provider.APIKey) != "" {
		header := firstNonEmpty(provider.AuthHeader, "Authorization")
		scheme := strings.TrimSpace(provider.AuthScheme)
		value := strings.TrimSpace(provider.APIKey)
		if scheme != "" {
			value = scheme + " " + value
		}
		headers.Set(header, value)
	}
	return headers, nil
}

func bearerHeader(apiKey string) http.Header {
	headers := http.Header{}
	if strings.TrimSpace(apiKey) != "" {
		headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	return headers
}

func normalizeLLMWireAPI(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "responses":
		return llmAPIProtocolResponses
	case "chat", "chat_completion", "chat_completions":
		return llmAPIProtocolChatCompletions
	case "messages":
		return llmAPIProtocolMessages
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func llmEndpointForProvider(provider LLMProvider, wireAPI string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" {
		return ""
	}
	if strings.HasSuffix(baseURL, "/responses") || strings.HasSuffix(baseURL, "/chat/completions") || strings.HasSuffix(baseURL, "/messages") {
		return baseURL
	}
	if strings.EqualFold(strings.TrimSpace(provider.ProviderType), "openai") {
		if !strings.HasSuffix(baseURL, "/v1") {
			baseURL += "/v1"
		}
	}
	switch normalizeLLMWireAPI(wireAPI) {
	case llmAPIProtocolChatCompletions:
		return baseURL + "/chat/completions"
	case llmAPIProtocolMessages:
		return baseURL + "/messages"
	default:
		return baseURL + "/responses"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
