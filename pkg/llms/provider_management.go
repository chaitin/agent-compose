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
	// Auth is the explicit credential presentation. Nil means unspecified: the
	// create default is the protocol convention and an update preserves the
	// stored override. A non-nil empty presentation clears an override so the
	// connection follows the protocol convention again.
	Auth *ProviderAuth
	// Models is the connection's declared model set. Nil means unspecified: a
	// create declares none and an update preserves the stored set. A non-nil
	// empty set clears it, which is why this is a pointer rather than a slice.
	Models *[]ModelSpec
}

// ModelSpec declares one model a connection serves. Model IDs are literals the
// operator types; the daemon never needs them enumerated to route a request.
type ModelSpec struct {
	ID   string
	Name string
	// Protocol overrides the connection protocol for this model. Empty inherits
	// it, and a set value must stay in the connection's protocol family.
	Protocol string
	// BaseURL overrides the upstream endpoint for this model.
	BaseURL string
	Headers map[string]string
	// MaxOutputTokens caps the model's output. Zero inherits.
	MaxOutputTokens int
}

// ModelReference names one model on one connection.
type ModelReference struct {
	ProviderID string
	ModelID    string
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
	if err := normalizeProviderAuth(&normalized); err != nil {
		return ProviderReplacement{}, err
	}
	if err := normalizeProviderModels(&normalized); err != nil {
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
	if err := normalizeProviderAuth(&normalized); err != nil {
		return ProviderReplacement{}, err
	}
	if err := normalizeProviderModels(&normalized); err != nil {
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

// normalizeProviderAuth accepts only the presentations the daemon can put on the
// wire; an unknown name must not silently fall back to the protocol convention.
// A nil presentation stays unspecified, which is distinct from an explicit empty
// one that clears a stored override.
func normalizeProviderAuth(input *ProviderReplacement) error {
	if input.Auth == nil {
		return nil
	}
	normalized := ProviderAuth(strings.ToLower(strings.TrimSpace(string(*input.Auth))))
	switch normalized {
	case "", ProviderAuthXAPIKey, ProviderAuthBearer:
		input.Auth = &normalized
		return nil
	default:
		return fmt.Errorf("%w: auth must be x-api-key or bearer", domain.ErrInvalidArgument)
	}
}

// ManagedProviderHeadersJSON returns default upstream headers for a protocol.
func ManagedProviderHeadersJSON(protocol string) string {
	if protocol == APIProtocolMessages {
		return AnthropicVersionHeadersJSON
	}
	return "{}"
}

// normalizeProviderModels validates and copies a declared model set. A nil set
// stays nil so storage can tell "unspecified" from "empty".
func normalizeProviderModels(input *ProviderReplacement) error {
	if input.Models == nil {
		return nil
	}
	models := make([]ModelSpec, 0, len(*input.Models))
	seen := make(map[string]struct{}, len(*input.Models))
	for _, model := range *input.Models {
		normalized, err := normalizeModelSpec(model)
		if err != nil {
			return err
		}
		if _, ok := seen[normalized.ID]; ok {
			return fmt.Errorf("%w: model %q is declared more than once", domain.ErrInvalidArgument, normalized.ID)
		}
		seen[normalized.ID] = struct{}{}
		models = append(models, normalized)
	}
	input.Models = &models
	return nil
}

func normalizeModelSpec(model ModelSpec) (ModelSpec, error) {
	model.ID = strings.TrimSpace(model.ID)
	if model.ID == "" || strings.ContainsAny(model.ID, "\r\n") {
		return ModelSpec{}, fmt.Errorf("%w: model id is required and must contain no line breaks", domain.ErrInvalidArgument)
	}
	model.Name = strings.TrimSpace(model.Name)
	model.BaseURL = strings.TrimSpace(model.BaseURL)
	if model.BaseURL != "" {
		endpoint, err := url.Parse(model.BaseURL)
		if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return ModelSpec{}, fmt.Errorf("%w: model %q base_url must be an absolute HTTP(S) URL without credentials, query or fragment", domain.ErrInvalidArgument, model.ID)
		}
	}
	model.Protocol = strings.TrimSpace(model.Protocol)
	if model.Protocol != "" && catalogProtocolFamily(model.Protocol) == "" {
		return ModelSpec{}, fmt.Errorf("%w: model %q has an unsupported protocol", domain.ErrInvalidArgument, model.ID)
	}
	if model.MaxOutputTokens < 0 {
		return ModelSpec{}, fmt.Errorf("%w: model %q max_output_tokens must not be negative", domain.ErrInvalidArgument, model.ID)
	}
	for key, value := range model.Headers {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key+value, "\r\n") {
			return ModelSpec{}, fmt.Errorf("%w: model %q has an invalid header", domain.ErrInvalidArgument, model.ID)
		}
	}
	return model, nil
}

// ValidateModelSpecProtocol reports whether a model protocol fits the connection
// family. An empty model protocol inherits the connection protocol; a set one
// must stay in the same family, so an OpenAI connection cannot declare an
// Anthropic model and the reverse.
func ValidateModelSpecProtocol(providerProtocol, modelProtocol string) error {
	if strings.TrimSpace(modelProtocol) == "" {
		return nil
	}
	if catalogProtocolFamily(modelProtocol) != catalogProtocolFamily(providerProtocol) {
		return fmt.Errorf("%w: model protocol %q is incompatible with the provider protocol family", domain.ErrInvalidArgument, modelProtocol)
	}
	return nil
}
