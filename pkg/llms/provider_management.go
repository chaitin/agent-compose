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

// AnthropicVersionHeadersJSON is the default header set required by Anthropic Messages.
const AnthropicVersionHeadersJSON = `{"anthropic-version":"2023-06-01"}`

var managedProviderIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ProviderReplacement is API-owned provider configuration. Nil or empty optional
// fields are create defaults or, on update, mean "leave the stored value unchanged".
type ProviderReplacement struct {
	ID       string
	Name     string
	BaseURL  string
	Protocol string
	APIKey   *string
	Enabled  *bool
}

// ValidateManagedProviderID rejects IDs reserved for environment bootstrap.
func ValidateManagedProviderID(id string) error {
	if !managedProviderIDPattern.MatchString(id) || id == ProviderIDDefaultOpenAI || id == ProviderIDDefaultAnthropic {
		return fmt.Errorf("%w: provider id must be 1-128 letters, digits, dots, underscores or hyphens, start with a letter or digit, and not be default or anthropic", domain.ErrInvalidArgument)
	}
	return nil
}

// NormalizeProviderReplacement validates create input without exposing credentials
// in errors. It does not mutate caller-owned values. Empty name defaults to the ID;
// a nil enabled flag defaults to true.
func NormalizeProviderReplacement(input ProviderReplacement) (ProviderReplacement, error) {
	normalized, err := normalizeProviderIdentity(input)
	if err != nil {
		return ProviderReplacement{}, err
	}
	normalized.Name = strings.TrimSpace(normalized.Name)
	if normalized.Name == "" {
		normalized.Name = normalized.ID
	}
	if err := normalizeProviderEndpoint(&normalized, true); err != nil {
		return ProviderReplacement{}, err
	}
	if err := normalizeProviderProtocol(&normalized, true); err != nil {
		return ProviderReplacement{}, err
	}
	if err := normalizeProviderAPIKey(&normalized, true); err != nil {
		return ProviderReplacement{}, err
	}
	if normalized.Enabled == nil {
		enabled := true
		normalized.Enabled = &enabled
	}
	return normalized, nil
}

// NormalizeProviderUpdate validates an explicit-field update. Omitted name,
// base URL, protocol, API key, and enabled are left empty/nil so storage can
// preserve the current values.
func NormalizeProviderUpdate(input ProviderReplacement) (ProviderReplacement, error) {
	normalized, err := normalizeProviderIdentity(input)
	if err != nil {
		return ProviderReplacement{}, err
	}
	normalized.Name = strings.TrimSpace(normalized.Name)
	if err := normalizeProviderEndpoint(&normalized, false); err != nil {
		return ProviderReplacement{}, err
	}
	if err := normalizeProviderProtocol(&normalized, false); err != nil {
		return ProviderReplacement{}, err
	}
	if err := normalizeProviderAPIKey(&normalized, false); err != nil {
		return ProviderReplacement{}, err
	}
	return normalized, nil
}

func normalizeProviderIdentity(input ProviderReplacement) (ProviderReplacement, error) {
	if err := ValidateManagedProviderID(input.ID); err != nil {
		return ProviderReplacement{}, err
	}
	return input, nil
}

func normalizeProviderEndpoint(input *ProviderReplacement, required bool) error {
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	if input.BaseURL == "" {
		if required {
			return fmt.Errorf("%w: base_url must be an absolute HTTP(S) URL without credentials, query or fragment", domain.ErrInvalidArgument)
		}
		return nil
	}
	endpoint, err := url.Parse(input.BaseURL)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.ContainsAny(input.BaseURL, "\r\n") {
		return fmt.Errorf("%w: base_url must be an absolute HTTP(S) URL without credentials, query or fragment", domain.ErrInvalidArgument)
	}
	return nil
}

func normalizeProviderProtocol(input *ProviderReplacement, required bool) error {
	input.Protocol = strings.TrimSpace(input.Protocol)
	if input.Protocol == "" {
		if required {
			return fmt.Errorf("%w: unsupported provider protocol", domain.ErrInvalidArgument)
		}
		return nil
	}
	switch input.Protocol {
	case APIProtocolResponses, APIProtocolChatCompletions, APIProtocolMessages:
		return nil
	default:
		return fmt.Errorf("%w: unsupported provider protocol", domain.ErrInvalidArgument)
	}
}

func normalizeProviderAPIKey(input *ProviderReplacement, required bool) error {
	if input.APIKey == nil {
		if required {
			return fmt.Errorf("%w: api_key is required", domain.ErrInvalidArgument)
		}
		return nil
	}
	key := strings.TrimSpace(*input.APIKey)
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return fmt.Errorf("%w: api_key must be nonempty and contain no line breaks", domain.ErrInvalidArgument)
	}
	input.APIKey = &key
	return nil
}

// ManagedProviderHeadersJSON returns default upstream headers for a protocol.
func ManagedProviderHeadersJSON(protocol string) string {
	if protocol == APIProtocolMessages {
		return AnthropicVersionHeadersJSON
	}
	return "{}"
}
