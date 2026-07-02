package llm

import (
	"context"
	"strings"
	"testing"

	modeldomain "agent-compose/internal/model"
)

type SessionEnvVar = modeldomain.SessionEnvVar

const llmProviderFamilyOpenAI = "openai"

type testConfigStore struct {
	envItems  []SessionEnvVar
	providers []LLMProvider
	models    []LLMModel
	wireAPI   map[string]string
}

func newTestConfigStore(*testing.T) *testConfigStore {
	return &testConfigStore{wireAPI: map[string]string{}}
}

func (s *testConfigStore) ReplaceGlobalEnv(_ context.Context, items []SessionEnvVar) ([]SessionEnvVar, error) {
	s.envItems = append([]SessionEnvVar(nil), items...)
	return append([]SessionEnvVar(nil), s.envItems...), nil
}

func (s *testConfigStore) ListGlobalEnv(context.Context) ([]SessionEnvVar, error) {
	return append([]SessionEnvVar(nil), s.envItems...), nil
}

func (s *testConfigStore) ListEnabledLLMProviders(context.Context) ([]LLMProvider, error) {
	if len(s.providers) == 0 {
		env := envMap(s.envItems)
		if endpoint := strings.TrimSpace(env["LLM_API_ENDPOINT"]); endpoint != "" {
			return []LLMProvider{{
				ID:             "env-openai",
				Name:           "Env OpenAI",
				ProviderType:   llmProviderFamilyOpenAI,
				DefaultWireAPI: llmAPIProtocolResponses,
				BaseURL:        endpoint,
				APIKey:         firstNonEmpty(env["LLM_API_KEY"], env["OPENAI_API_KEY"]),
				AuthHeader:     "Authorization",
				AuthScheme:     "Bearer",
				Enabled:        true,
			}}, nil
		}
	}
	return append([]LLMProvider(nil), s.providers...), nil
}

func (s *testConfigStore) ListEnabledLLMModels(context.Context) ([]LLMModel, error) {
	if len(s.models) == 0 {
		env := envMap(s.envItems)
		if model := strings.TrimSpace(env["LLM_MODEL"]); model != "" {
			return []LLMModel{{ID: model, Name: model, DefaultModel: true, Enabled: true}}, nil
		}
	}
	return append([]LLMModel(nil), s.models...), nil
}

func (s *testConfigStore) LLMProviderModelWireAPI(_ context.Context, providerID, modelID string) (string, bool, error) {
	value, ok := s.wireAPI[providerID+"|"+modelID]
	return value, ok, nil
}

func envMap(items []SessionEnvVar) map[string]string {
	result := map[string]string{}
	for _, item := range items {
		name := strings.ToUpper(strings.TrimSpace(item.Name))
		if name != "" {
			result[name] = item.Value
		}
	}
	return result
}
