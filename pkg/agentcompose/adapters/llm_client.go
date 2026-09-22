package adapters

import (
	"context"
	"fmt"
	"net/http"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

// LLMClient is the daemon's own LLM caller, used by the scheduler when a host
// model call is requested. It is not an agent runtime: it resolves against the
// configured connection catalog exactly as the daemon would, and carries no
// per-sandbox or per-scope environment.
type LLMClient struct {
	config *appconfig.Config
	store  *configstore.ConfigStore
	client *http.Client
}

func NewLLMClient(config *appconfig.Config, store *configstore.ConfigStore) *LLMClient {
	var timeout time.Duration
	if config != nil {
		timeout = config.LLMTimeout
	}
	return &LLMClient{
		config: config,
		store:  store,
		client: &http.Client{Timeout: timeout},
	}
}

// Generate makes one daemon-owned LLM call. The model is opaque: an empty model
// selects the catalog default, and connection selection is the catalog's fixed
// precedence. A daemon-owned call has no sandbox and therefore no per-scope
// environment to layer on top of the catalog.
func (c *LLMClient) Generate(ctx context.Context, prompt, model, outputSchemaJSON string) (llms.GenerateResult, error) {
	if c == nil || c.store == nil {
		return llms.GenerateResult{}, fmt.Errorf("llm client is unavailable")
	}
	catalog, err := llms.LoadCatalog(ctx, c.store)
	if err != nil {
		return llms.GenerateResult{}, err
	}
	selected, err := catalog.SelectModel(model)
	if err != nil {
		return llms.GenerateResult{}, err
	}
	target, err := catalog.Resolve("", selected)
	if err != nil {
		return llms.GenerateResult{}, err
	}
	return llms.Generate(ctx, c.client, llms.GenerateRequest{
		Endpoint:         target.Endpoint,
		Protocol:         target.WireAPI,
		Prompt:           prompt,
		Model:            firstNonEmpty(target.Model.ID, target.Model.Name),
		OutputSchemaJSON: outputSchemaJSON,
		Headers:          target.Headers,
		MaxOutputTokens:  firstPositive(target.MaxOutputTokens, configuredMaxOutputTokens(c.config)),
	})
}

func configuredMaxOutputTokens(config *appconfig.Config) int {
	if config == nil {
		return 0
	}
	return config.LLMMaxOutputTokens
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
