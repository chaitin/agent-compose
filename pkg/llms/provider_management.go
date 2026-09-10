package llms

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ProviderScopeAPI identifies providers managed through the public LLM service.
const ProviderScopeAPI = "api"

var managedProviderIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ProviderReplacement is a complete public configuration replacement. A nil key
// preserves the stored credential on update, without requiring clients to read it.
type ProviderReplacement struct {
	ID       string
	Name     string
	BaseURL  string
	Protocol string
	APIKey   *string
	Enabled  bool
}

// ValidateManagedProviderID rejects IDs reserved for environment bootstrap.
func ValidateManagedProviderID(id string) error {
	if !managedProviderIDPattern.MatchString(id) || id == ProviderIDDefaultOpenAI || id == ProviderIDDefaultAnthropic {
		return fmt.Errorf("%w: provider id must be 1-128 letters, digits, dots, underscores or hyphens, start with a letter or digit, and not be default or anthropic", domain.ErrInvalidArgument)
	}
	return nil
}

// NormalizeProviderReplacement validates external configuration without exposing
// credentials in errors. It does not mutate caller-owned values.
func NormalizeProviderReplacement(input ProviderReplacement) (ProviderReplacement, error) {
	if err := ValidateManagedProviderID(input.ID); err != nil {
		return ProviderReplacement{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		input.Name = input.ID
	}
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	endpoint, err := url.Parse(input.BaseURL)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.ContainsAny(input.BaseURL, "\r\n") {
		return ProviderReplacement{}, fmt.Errorf("%w: base_url must be an absolute HTTP(S) URL without credentials, query or fragment", domain.ErrInvalidArgument)
	}
	switch input.Protocol {
	case APIProtocolResponses, APIProtocolChatCompletions, APIProtocolMessages:
	default:
		return ProviderReplacement{}, fmt.Errorf("%w: unsupported provider protocol", domain.ErrInvalidArgument)
	}
	if input.APIKey != nil {
		key := strings.TrimSpace(*input.APIKey)
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return ProviderReplacement{}, fmt.Errorf("%w: api_key must be nonempty and contain no line breaks", domain.ErrInvalidArgument)
		}
		input.APIKey = &key
	}
	return input, nil
}
