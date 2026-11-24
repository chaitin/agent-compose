package llms

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const ModelsCatalogFilename = "models.json"

var catalogEnvironmentReference = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$|^\$([A-Za-z_][A-Za-z0-9_]*)$`)

// ModelCatalog is the daemon-owned provider and model catalog. Models carry
// optional behavior; they are not an allowlist for upstream model IDs.
type ModelCatalog struct {
	// Default is the file's default model reference. An absent key (nil) leaves
	// the stored reference alone, so a default chosen at runtime survives a
	// restart. A present but empty value is the file explicitly declaring that it
	// has no default, which clears the stored reference; any other value must name
	// a connection the file declares.
	Default   *string                    `json:"default,omitempty"`
	Providers map[string]CatalogProvider `json:"providers"`
}

// CatalogDefault returns the value a ModelCatalog declaration carries for a
// default model: a non-empty reference declares that default, and an empty one
// declares that the file has none, which clears the stored reference. A caller
// that leaves the key absent keeps the stored reference instead.
func CatalogDefault(reference string) *string { return &reference }

// CatalogProvider defines one upstream provider. Pointer fields distinguish
// omission from an explicitly empty value during decoding and validation.
type CatalogProvider struct {
	Name     *string `json:"name,omitempty"`
	BaseURL  *string `json:"baseUrl,omitempty"`
	Protocol *string `json:"protocol,omitempty"`
	APIKey   *string `json:"apiKey,omitempty"`
	// Enabled overrides the credential-derived default. Absent keeps the rule
	// that a declaration without an apiKey is persisted disabled; true declares a
	// connection that needs no credential, such as a local endpoint, so the file
	// can serve models and a default from it; false disables a connection whose
	// apiKey is present.
	Enabled *bool             `json:"enabled,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Models  []CatalogModel    `json:"models,omitempty"`
}

// CatalogModel contains optional per-model connection and output behavior.
type CatalogModel struct {
	ID              string            `json:"id"`
	Name            *string           `json:"name,omitempty"`
	BaseURL         *string           `json:"baseUrl,omitempty"`
	Protocol        *string           `json:"protocol,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	MaxOutputTokens *int              `json:"maxOutputTokens,omitempty"`
	ContextWindow   *int              `json:"contextWindow,omitempty"`
}

// ProviderModelConfig is the stored behavior for one provider/model binding.
type ProviderModelConfig struct {
	WireAPI         string
	BaseURL         string
	HeadersJSON     string
	MaxOutputTokens int
	ContextWindow   int
	DisplayName     string
}

// LoadModelCatalog loads DATA_ROOT/models.json. A missing file returns an empty
// catalog. Invalid JSON and unresolved environment references fail.
func LoadModelCatalog(path string, lookup func(string) (string, bool)) (ModelCatalog, error) {
	empty := ModelCatalog{Providers: map[string]CatalogProvider{}}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return ModelCatalog{}, fmt.Errorf("open model catalog %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	declared, err := decodeModelCatalog(file)
	if err != nil {
		return ModelCatalog{}, fmt.Errorf("decode model catalog %s: %w", path, err)
	}
	resolved, err := resolveCatalogValues(declared, lookup)
	if err != nil {
		return ModelCatalog{}, fmt.Errorf("resolve model catalog %s: %w", path, err)
	}
	return MergeModelCatalog(empty, resolved)
}

func decodeModelCatalog(reader io.Reader) (ModelCatalog, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var catalog ModelCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return ModelCatalog{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return ModelCatalog{}, fmt.Errorf("multiple JSON values are not allowed")
		}
		return ModelCatalog{}, err
	}
	if catalog.Providers == nil {
		catalog.Providers = map[string]CatalogProvider{}
	}
	return catalog, nil
}

// MergeModelCatalog overlays providers field-by-field and models by ID.
func MergeModelCatalog(base, overlay ModelCatalog) (ModelCatalog, error) {
	result := cloneModelCatalog(base)
	if overlay.Default != nil {
		value := strings.TrimSpace(*overlay.Default)
		result.Default = &value
	}
	providerIDs := make([]string, 0, len(overlay.Providers))
	for id := range overlay.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, rawID := range providerIDs {
		id := strings.TrimSpace(rawID)
		if id == "" || id != rawID || strings.Contains(id, "/") {
			return ModelCatalog{}, fmt.Errorf("invalid provider ID %q", rawID)
		}
		if err := validateDeclaredCatalogModels(id, overlay.Providers[rawID].Models); err != nil {
			return ModelCatalog{}, err
		}
		provider := mergeCatalogProvider(result.Providers[id], overlay.Providers[rawID])
		if err := validateCatalogProvider(id, provider); err != nil {
			return ModelCatalog{}, err
		}
		result.Providers[id] = provider
	}
	if result.Default != nil && *result.Default != "" {
		providerID, _, ok := SplitProviderModelReference(*result.Default)
		if !ok {
			return ModelCatalog{}, fmt.Errorf("default %q must use provider/model", *result.Default)
		}
		if _, ok := result.Providers[providerID]; !ok {
			return ModelCatalog{}, fmt.Errorf("default references unknown provider %q", providerID)
		}
	}
	return result, nil
}

func mergeCatalogProvider(base, overlay CatalogProvider) CatalogProvider {
	if overlay.Name != nil {
		base.Name = stringPointer(*overlay.Name)
	}
	if overlay.BaseURL != nil {
		base.BaseURL = stringPointer(*overlay.BaseURL)
	}
	if overlay.Protocol != nil {
		base.Protocol = stringPointer(*overlay.Protocol)
	}
	if overlay.APIKey != nil {
		base.APIKey = stringPointer(*overlay.APIKey)
	}
	if overlay.Enabled != nil {
		value := *overlay.Enabled
		base.Enabled = &value
	}
	if overlay.Headers != nil {
		base.Headers = cloneStringMap(overlay.Headers)
	}
	indexes := make(map[string]int, len(base.Models))
	for index, model := range base.Models {
		indexes[model.ID] = index
	}
	for _, model := range overlay.Models {
		if index, ok := indexes[model.ID]; ok {
			base.Models[index] = mergeCatalogModel(base.Models[index], model)
			continue
		}
		indexes[model.ID] = len(base.Models)
		base.Models = append(base.Models, cloneCatalogModel(model))
	}
	return base
}

func mergeCatalogModel(base, overlay CatalogModel) CatalogModel {
	if overlay.Name != nil {
		base.Name = stringPointer(*overlay.Name)
	}
	if overlay.BaseURL != nil {
		base.BaseURL = stringPointer(*overlay.BaseURL)
	}
	if overlay.Protocol != nil {
		base.Protocol = stringPointer(*overlay.Protocol)
	}
	if overlay.Headers != nil {
		base.Headers = cloneStringMap(overlay.Headers)
	}
	if overlay.MaxOutputTokens != nil {
		value := *overlay.MaxOutputTokens
		base.MaxOutputTokens = &value
	}
	if overlay.ContextWindow != nil {
		value := *overlay.ContextWindow
		base.ContextWindow = &value
	}
	return base
}

func validateCatalogProvider(id string, provider CatalogProvider) error {
	if provider.BaseURL == nil || strings.TrimSpace(*provider.BaseURL) == "" {
		return fmt.Errorf("provider %q requires baseUrl", id)
	}
	if provider.Protocol == nil || !supportedCatalogProtocol(*provider.Protocol) {
		return fmt.Errorf("provider %q requires a supported protocol", id)
	}
	if err := checkHeaderNames(provider.Headers); err != nil {
		return fmt.Errorf("provider %q: %w", id, err)
	}
	seen := make(map[string]struct{}, len(provider.Models))
	for _, model := range provider.Models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" || modelID != model.ID {
			return fmt.Errorf("provider %q has a model without an ID", id)
		}
		if _, ok := seen[modelID]; ok {
			return fmt.Errorf("provider %q declares model %q more than once", id, modelID)
		}
		seen[modelID] = struct{}{}
		if model.Protocol != nil {
			modelFamily := catalogProtocolFamily(*model.Protocol)
			if modelFamily == "" {
				return fmt.Errorf("provider %q model %q has an unsupported protocol", id, modelID)
			}
			providerFamily := catalogProtocolFamily(*provider.Protocol)
			if modelFamily != providerFamily {
				return fmt.Errorf("provider %q model %q protocol %q is incompatible with provider family %q", id, modelID, *model.Protocol, providerFamily)
			}
		}
		if model.MaxOutputTokens != nil && *model.MaxOutputTokens <= 0 {
			return fmt.Errorf("provider %q model %q maxOutputTokens must be positive", id, modelID)
		}
		if err := checkHeaderNames(model.Headers); err != nil {
			return fmt.Errorf("provider %q model %q: %w", id, modelID, err)
		}
		// The catalog declares the cap as a JSON number while the public contract
		// reports it as a 32-bit integer, so a larger cap would reach a client as a
		// different number than the catalog declares.
		if model.MaxOutputTokens != nil && *model.MaxOutputTokens > MaxModelOutputTokens {
			return fmt.Errorf("provider %q model %q maxOutputTokens must not exceed %d", id, modelID, MaxModelOutputTokens)
		}
		if model.ContextWindow != nil && (*model.ContextWindow < 0 || *model.ContextWindow > MaxModelContextWindow) {
			return fmt.Errorf("provider %q model %q contextWindow must be between 0 and %d", id, modelID, MaxModelContextWindow)
		}
	}
	return nil
}

func validateDeclaredCatalogModels(providerID string, models []CatalogModel) error {
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if _, ok := seen[modelID]; ok {
			return fmt.Errorf("provider %q declares model %q more than once", providerID, modelID)
		}
		seen[modelID] = struct{}{}
	}
	return nil
}

func supportedCatalogProtocol(value string) bool {
	return catalogProtocolFamily(value) != ""
}

func catalogProtocolFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case APIProtocolResponses, APIProtocolChatCompletions:
		return ProviderFamilyOpenAI
	case APIProtocolMessages:
		return ProviderFamilyAnthropic
	default:
		return ""
	}
}

func resolveCatalogValues(catalog ModelCatalog, lookup func(string) (string, bool)) (ModelCatalog, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	result := cloneModelCatalog(catalog)
	for id, provider := range result.Providers {
		if provider.APIKey != nil {
			value, err := resolveCatalogString(*provider.APIKey, lookup)
			if err != nil {
				return ModelCatalog{}, fmt.Errorf("provider %q apiKey: %w", id, err)
			}
			provider.APIKey = stringPointer(value)
		}
		resolved, err := resolveCatalogHeaders(provider.Headers, lookup)
		if err != nil {
			return ModelCatalog{}, fmt.Errorf("provider %q headers: %w", id, err)
		}
		provider.Headers = resolved
		for index := range provider.Models {
			resolved, err := resolveCatalogHeaders(provider.Models[index].Headers, lookup)
			if err != nil {
				return ModelCatalog{}, fmt.Errorf("provider %q model %q headers: %w", id, provider.Models[index].ID, err)
			}
			provider.Models[index].Headers = resolved
		}
		result.Providers[id] = provider
	}
	return result, nil
}

func resolveCatalogHeaders(headers map[string]string, lookup func(string) (string, bool)) (map[string]string, error) {
	if headers == nil {
		return nil, nil
	}
	resolved := make(map[string]string, len(headers))
	for key, raw := range headers {
		value, err := resolveCatalogString(raw, lookup)
		if err != nil {
			return nil, fmt.Errorf("header %q: %w", key, err)
		}
		resolved[key] = value
	}
	return resolved, nil
}

func resolveCatalogString(value string, lookup func(string) (string, bool)) (string, error) {
	matches := catalogEnvironmentReference.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return value, nil
	}
	name := firstNonEmptyTrimmed(matches[1], matches[2])
	resolved, ok := lookup(name)
	if !ok || strings.TrimSpace(resolved) == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return resolved, nil
}

func cloneModelCatalog(catalog ModelCatalog) ModelCatalog {
	clone := ModelCatalog{Providers: make(map[string]CatalogProvider, len(catalog.Providers))}
	if catalog.Default != nil {
		value := *catalog.Default
		clone.Default = &value
	}
	for id, provider := range catalog.Providers {
		copyProvider := provider
		if provider.Name != nil {
			copyProvider.Name = stringPointer(*provider.Name)
		}
		if provider.BaseURL != nil {
			copyProvider.BaseURL = stringPointer(*provider.BaseURL)
		}
		if provider.Protocol != nil {
			copyProvider.Protocol = stringPointer(*provider.Protocol)
		}
		if provider.APIKey != nil {
			copyProvider.APIKey = stringPointer(*provider.APIKey)
		}
		if provider.Enabled != nil {
			value := *provider.Enabled
			copyProvider.Enabled = &value
		}
		copyProvider.Headers = cloneStringMap(provider.Headers)
		copyProvider.Models = make([]CatalogModel, len(provider.Models))
		for index, model := range provider.Models {
			copyProvider.Models[index] = cloneCatalogModel(model)
		}
		clone.Providers[id] = copyProvider
	}
	return clone
}

func cloneCatalogModel(model CatalogModel) CatalogModel {
	clone := model
	if model.Name != nil {
		clone.Name = stringPointer(*model.Name)
	}
	if model.BaseURL != nil {
		clone.BaseURL = stringPointer(*model.BaseURL)
	}
	if model.Protocol != nil {
		clone.Protocol = stringPointer(*model.Protocol)
	}
	clone.Headers = cloneStringMap(model.Headers)
	if model.MaxOutputTokens != nil {
		value := *model.MaxOutputTokens
		clone.MaxOutputTokens = &value
	}
	if model.ContextWindow != nil {
		value := *model.ContextWindow
		clone.ContextWindow = &value
	}
	return clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func stringPointer(value string) *string { return &value }

// SplitProviderModelReference parses provider/model while allowing additional
// slashes in the literal upstream model ID.
func SplitProviderModelReference(value string) (providerID, modelID string, ok bool) {
	providerID, modelID, ok = strings.Cut(strings.TrimSpace(value), "/")
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	return providerID, modelID, ok && providerID != "" && modelID != ""
}
