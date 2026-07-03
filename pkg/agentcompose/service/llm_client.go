package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	"context"
	"net/http"

	"github.com/samber/do/v2"
)

type LLMClient struct {
	config   *appconfig.Config
	configDB *ConfigStore
	client   *http.Client
}

type LLMGenerateResult struct {
	Text         string
	Model        string
	ResponseID   string
	FinishReason string
}

func NewLLMClient(di do.Injector) (*LLMClient, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	return &LLMClient{
		config:   config,
		configDB: do.MustInvoke[*ConfigStore](di),
		client: &http.Client{
			Timeout: config.LLMTimeout,
		},
	}, nil
}

func (c *LLMClient) Generate(ctx context.Context, prompt, model, outputSchemaJSON string) (LLMGenerateResult, error) {
	if c == nil {
		return LLMGenerateResult{}, llmClientUnavailableError()
	}
	target, err := resolveLLMTarget(ctx, c.config, c.configDB, model)
	if err != nil {
		return LLMGenerateResult{}, err
	}
	result, err := llms.Generate(ctx, c.client, llms.GenerateRequest{
		Endpoint:         target.Endpoint,
		Protocol:         target.WireAPI,
		Prompt:           prompt,
		Model:            firstNonEmpty(model, target.Model.Name, target.Model.ID),
		OutputSchemaJSON: outputSchemaJSON,
		Headers:          target.Headers,
	})
	if err != nil {
		return LLMGenerateResult{}, err
	}
	return LLMGenerateResult{
		Text:         result.Text,
		Model:        result.Model,
		ResponseID:   result.ResponseID,
		FinishReason: result.FinishReason,
	}, nil
}

func llmClientUnavailableError() error {
	return &llmClientError{message: "llm client is unavailable"}
}

type llmClientError struct {
	message string
}

func (e *llmClientError) Error() string {
	return e.message
}

func (c *LLMClient) resolveSetting(ctx context.Context, fallback string, keys ...string) string {
	return llms.ResolveSetting(ctx, c.globalEnvStore(), fallback, keys...)
}

func (c *LLMClient) resolveEndpoint(ctx context.Context) string {
	return llms.ResolveEndpoint(ctx, c.globalEnvStore(), c.clientConfig())
}

func (c *LLMClient) resolveProtocol(ctx context.Context) string {
	return llms.ResolveProtocol(ctx, c.globalEnvStore(), c.clientConfig())
}

func (c *LLMClient) globalEnvStore() llms.GlobalEnvStore {
	if c == nil {
		return nil
	}
	return c.configDB
}

func (c *LLMClient) clientConfig() llms.ClientConfig {
	if c == nil || c.config == nil {
		return llms.ClientConfig{}
	}
	return llms.ClientConfig{
		Endpoint: c.config.LLMAPIEndpoint,
		Protocol: c.config.LLMAPIProtocol,
	}
}
