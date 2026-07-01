package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"strings"

	"agent-compose/pkg/model"

	"github.com/samber/do/v2"
)

type LLMClient struct {
	config   *appconfig.Config
	configDB *ConfigStore
	client   *http.Client
}

type LLMGenerateResult = model.LLMGenerateResult

const (
	llmAPIProtocolResponses       = "responses"
	llmAPIProtocolChatCompletions = "chat_completions"
	llmAPIProtocolMessages        = "messages"
)

type llmAPIRequest struct {
	Model string             `json:"model"`
	Input string             `json:"input"`
	Text  *llmAPITextOptions `json:"text,omitempty"`
}

type llmAPITextOptions struct {
	Format llmAPITextFormat `json:"format"`
}

type llmAPITextFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type llmAPIResponse struct {
	ID                string               `json:"id"`
	Model             string               `json:"model"`
	Status            string               `json:"status"`
	OutputText        string               `json:"output_text"`
	Output            []llmAPIOutput       `json:"output"`
	Error             *llmAPIError         `json:"error"`
	IncompleteDetails *llmIncompleteReason `json:"incomplete_details"`
}

type llmAPIOutput struct {
	Type         string             `json:"type"`
	Status       string             `json:"status"`
	FinishReason string             `json:"finish_reason"`
	StopReason   string             `json:"stop_reason"`
	Content      []llmAPIOutputText `json:"content"`
}

type llmAPIOutputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type llmAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type llmIncompleteReason struct {
	Reason string `json:"reason"`
}

type llmChatCompletionsRequest struct {
	Model          string                        `json:"model"`
	Messages       []llmChatCompletionsMessage   `json:"messages"`
	ResponseFormat *llmChatCompletionsJSONFormat `json:"response_format,omitempty"`
}

type llmChatCompletionsMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmChatCompletionsJSONFormat struct {
	Type string `json:"type"`
}

type llmChatCompletionsResponse struct {
	ID      string                     `json:"id"`
	Model   string                     `json:"model"`
	Choices []llmChatCompletionsChoice `json:"choices"`
	Error   *llmAPIError               `json:"error"`
}

type llmChatCompletionsChoice struct {
	Message      llmChatCompletionsMessage `json:"message"`
	FinishReason string                    `json:"finish_reason"`
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
		return LLMGenerateResult{}, fmt.Errorf("llm client is unavailable")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return LLMGenerateResult{}, fmt.Errorf("prompt is required")
	}
	target, err := resolveLLMTarget(ctx, c.config, c.configDB, model)
	if err != nil {
		return LLMGenerateResult{}, err
	}
	protocol := normalizeLLMWireAPI(target.WireAPI)
	endpoint := strings.TrimSpace(target.Endpoint)
	if endpoint == "" {
		return LLMGenerateResult{}, fmt.Errorf("llm api endpoint is not configured")
	}
	model = strings.TrimSpace(firstNonEmpty(model, target.Model.Name, target.Model.ID))
	if model == "" {
		return LLMGenerateResult{}, fmt.Errorf("llm model is required")
	}
	if protocol == llmAPIProtocolChatCompletions {
		return c.generateChatCompletions(ctx, endpoint, prompt, model, outputSchemaJSON, target.Headers)
	}
	if protocol != llmAPIProtocolResponses {
		return LLMGenerateResult{}, fmt.Errorf("unsupported llm api protocol %q", protocol)
	}
	request := llmAPIRequest{Model: model, Input: prompt}
	if schema := strings.TrimSpace(outputSchemaJSON); schema != "" {
		if !json.Valid([]byte(schema)) {
			return LLMGenerateResult{}, fmt.Errorf("llm outputSchema must be valid JSON")
		}
		request.Text = &llmAPITextOptions{
			Format: llmAPITextFormat{
				Type:   "json_schema",
				Name:   "agent_compose_llm_output",
				Schema: json.RawMessage(schema),
				Strict: true,
			},
		}
	}
	requestBody, err := json.Marshal(request)
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("encode llm request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("create llm request: %w", err)
	}
	applyLLMForwardHeaders(req.Header, target.Headers)
	resp, err := c.client.Do(req)
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("call llm endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("read llm response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		var failure llmAPIResponse
		if err := json.Unmarshal(body, &failure); err == nil && failure.Error != nil && strings.TrimSpace(failure.Error.Message) != "" {
			message = strings.TrimSpace(failure.Error.Message)
		}
		if message == "" {
			message = fmt.Sprintf("llm endpoint returned %s", resp.Status)
		}
		return LLMGenerateResult{}, fmt.Errorf("llm endpoint returned %s: %s", resp.Status, message)
	}
	var parsed llmAPIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return LLMGenerateResult{}, fmt.Errorf("decode llm response: %w", err)
	}
	text := extractLLMResponseText(parsed)
	if text == "" {
		return LLMGenerateResult{}, fmt.Errorf("llm response did not contain text output")
	}
	return LLMGenerateResult{
		Text:         text,
		Model:        firstNonEmpty(strings.TrimSpace(parsed.Model), model),
		ResponseID:   strings.TrimSpace(parsed.ID),
		FinishReason: extractLLMFinishReason(parsed),
	}, nil
}

// generateChatCompletions calls an OpenAI-compatible Chat Completions backend for
// unary prompt-to-response text generation (LLMService, scheduler.llm).
func (c *LLMClient) generateChatCompletions(ctx context.Context, endpoint, prompt, model, outputSchemaJSON string, headers http.Header) (LLMGenerateResult, error) {
	messages := []llmChatCompletionsMessage{{Role: "user", Content: prompt}}
	request := llmChatCompletionsRequest{Model: model, Messages: messages}
	if schema := strings.TrimSpace(outputSchemaJSON); schema != "" {
		if !json.Valid([]byte(schema)) {
			return LLMGenerateResult{}, fmt.Errorf("llm outputSchema must be valid JSON")
		}
		request.Messages = append([]llmChatCompletionsMessage{{
			Role:    "system",
			Content: "Respond with a single JSON object that conforms to this JSON Schema:\n" + schema,
		}}, request.Messages...)
		request.ResponseFormat = &llmChatCompletionsJSONFormat{Type: "json_object"}
	}
	requestBody, err := json.Marshal(request)
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("encode llm chat completions request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("create llm chat completions request: %w", err)
	}
	applyLLMForwardHeaders(req.Header, headers)
	resp, err := c.client.Do(req)
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("call llm chat completions endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return LLMGenerateResult{}, fmt.Errorf("read llm chat completions response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		var failure llmChatCompletionsResponse
		if err := json.Unmarshal(body, &failure); err == nil && failure.Error != nil && strings.TrimSpace(failure.Error.Message) != "" {
			message = strings.TrimSpace(failure.Error.Message)
		}
		if message == "" {
			message = fmt.Sprintf("llm chat completions endpoint returned %s", resp.Status)
		}
		return LLMGenerateResult{}, fmt.Errorf("llm chat completions endpoint returned %s: %s", resp.Status, message)
	}
	var parsed llmChatCompletionsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return LLMGenerateResult{}, fmt.Errorf("decode llm chat completions response: %w", err)
	}
	text := extractLLMChatCompletionsText(parsed)
	if text == "" {
		return LLMGenerateResult{}, fmt.Errorf("llm chat completions response did not contain text output")
	}
	if strings.TrimSpace(outputSchemaJSON) != "" && !json.Valid([]byte(text)) {
		return LLMGenerateResult{}, fmt.Errorf("llm chat completions response did not contain valid JSON")
	}
	return LLMGenerateResult{
		Text:         text,
		Model:        firstNonEmpty(strings.TrimSpace(parsed.Model), model),
		ResponseID:   strings.TrimSpace(parsed.ID),
		FinishReason: extractLLMChatCompletionsFinishReason(parsed),
	}, nil
}

func applyLLMForwardHeaders(dst http.Header, src http.Header) {
	dst.Set("Content-Type", "application/json")
	for key, values := range src {
		for _, value := range values {
			dst.Set(key, value)
		}
	}
}

func (c *LLMClient) resolveSetting(ctx context.Context, fallback string, keys ...string) string {
	if value := strings.TrimSpace(c.lookupGlobalEnv(ctx, keys...)); value != "" {
		return value
	}
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(strings.TrimSpace(key))); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(fallback); value != "" {
		return value
	}
	return ""
}

func (c *LLMClient) resolveEndpoint(ctx context.Context) string {
	return c.resolveEndpointForProtocol(ctx, c.resolveProtocol(ctx))
}

func (c *LLMClient) resolveEndpointForProtocol(ctx context.Context, protocol string) string {
	if value := strings.TrimSpace(c.lookupGlobalEnv(ctx, "LLM_API_ENDPOINT")); value != "" {
		return normalizeLLMAPIEndpointForProtocol(value, protocol)
	}
	if value := strings.TrimSpace(os.Getenv("LLM_API_ENDPOINT")); value != "" {
		return normalizeLLMAPIEndpointForProtocol(value, protocol)
	}
	if c != nil && c.config != nil {
		if value := strings.TrimSpace(c.config.LLMAPIEndpoint); value != "" {
			return normalizeLLMAPIEndpointForProtocol(value, protocol)
		}
	}
	return normalizeLLMAPIEndpointForProtocol("https://api.openai.com", protocol)
}

func (c *LLMClient) resolveProtocol(ctx context.Context) string {
	protocol := strings.ToLower(strings.TrimSpace(c.lookupGlobalEnv(ctx, "LLM_API_PROTOCOL")))
	if protocol == "" {
		protocol = strings.ToLower(strings.TrimSpace(os.Getenv("LLM_API_PROTOCOL")))
	}
	if protocol == "" && c != nil && c.config != nil {
		protocol = strings.ToLower(strings.TrimSpace(c.config.LLMAPIProtocol))
	}
	switch strings.ReplaceAll(protocol, "-", "_") {
	case "", llmAPIProtocolResponses:
		return llmAPIProtocolResponses
	case "chat", "chat_completions", "chat_completion":
		return llmAPIProtocolChatCompletions
	default:
		return protocol
	}
}

func (c *LLMClient) lookupGlobalEnv(ctx context.Context, keys ...string) string {
	if c == nil || c.configDB == nil || len(keys) == 0 {
		return ""
	}
	items, err := c.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return ""
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		for _, item := range items {
			if !strings.EqualFold(strings.TrimSpace(item.Name), key) {
				continue
			}
			if value := strings.TrimSpace(item.Value); value != "" {
				return value
			}
		}
	}
	return ""
}

func extractLLMResponseText(response llmAPIResponse) string {
	if text := strings.TrimSpace(response.OutputText); text != "" {
		return text
	}
	parts := make([]string, 0)
	for _, item := range response.Output {
		for _, content := range item.Content {
			text := strings.TrimSpace(content.Text)
			if text == "" {
				continue
			}
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractLLMFinishReason(response llmAPIResponse) string {
	if response.IncompleteDetails != nil {
		if reason := strings.TrimSpace(response.IncompleteDetails.Reason); reason != "" {
			return reason
		}
	}
	for _, item := range response.Output {
		if reason := strings.TrimSpace(item.FinishReason); reason != "" {
			return reason
		}
		if reason := strings.TrimSpace(item.StopReason); reason != "" {
			return reason
		}
	}
	return strings.TrimSpace(response.Status)
}

func extractLLMChatCompletionsText(response llmChatCompletionsResponse) string {
	parts := make([]string, 0, len(response.Choices))
	for _, choice := range response.Choices {
		text := strings.TrimSpace(choice.Message.Content)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractLLMChatCompletionsFinishReason(response llmChatCompletionsResponse) string {
	for _, choice := range response.Choices {
		if reason := strings.TrimSpace(choice.FinishReason); reason != "" {
			return reason
		}
	}
	return ""
}

func normalizeLLMAPIEndpoint(raw string) string {
	return normalizeLLMAPIEndpointForProtocol(raw, llmAPIProtocolResponses)
}

func normalizeLLMAPIEndpointForProtocol(raw, protocol string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if protocol == llmAPIProtocolChatCompletions && (strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/") {
		parsed.Path = "/v1/chat/completions"
		return parsed.String()
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	if protocol == llmAPIProtocolChatCompletions && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
		parsed.Path = pathpkg.Join(parsed.Path, "/chat/completions")
		return parsed.String()
	}
	if protocol == llmAPIProtocolChatCompletions && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/chat/completions")
		return parsed.String()
	}
	if protocol == llmAPIProtocolResponses && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
		return parsed.String()
	}
	if protocol == llmAPIProtocolResponses && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
		parsed.Path = pathpkg.Join(parsed.Path, "/responses")
		return parsed.String()
	}
	if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
	}
	return parsed.String()
}
