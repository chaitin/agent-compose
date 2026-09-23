package llms

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrNoModel reports that neither the agent nor the catalog declares a model.
	ErrNoModel = errors.New("no llm model configured")
	// ErrNoConnection reports that the daemon has no upstream connection at all.
	// It is the "nothing is configured" counterpart to ErrNoModel, and callers
	// treat it the same way: the agent keeps its own authentication. Ambiguity
	// between configured connections is a different failure and stays fatal.
	ErrNoConnection = errors.New("no llm connection configured")
	// ErrConnectionNotFound reports a connection id that matches no configured
	// connection.
	ErrConnectionNotFound = errors.New("llm connection not found")
	// ErrAmbiguousConnection reports that a model did not identify one connection.
	ErrAmbiguousConnection = errors.New("ambiguous llm connection")
	// ErrLegacyQualifiedModel reports a model written as "<connection>/<model>",
	// the syntax the connection-aware catalog replaced. It is not sniffed from
	// the shape alone: a model id is opaque and may legitimately contain a slash,
	// so this is reported only when the prefix names a connection that serves the
	// remainder, which the old syntax guaranteed and a real model id rarely does.
	ErrLegacyQualifiedModel = errors.New("model uses the retired <connection>/<model> syntax")
)

// ProviderModelBinding is one model served by one connection.
type ProviderModelBinding struct {
	ProviderID string
	ModelID    string
	Config     ProviderModelConfig
}

// CatalogStore reads the persisted LLM configuration that a Catalog snapshots.
// It is the single read boundary for the configuration layer: daemon
// environment variables and models.json are projected into this store at load
// time, so resolution never probes the environment or the filesystem.
type CatalogStore interface {
	ListEnabledLLMProviders(ctx context.Context) ([]Provider, error)
	ListEnabledLLMModels(ctx context.Context) ([]Model, error)
	ListLLMProviderModelConfigs(ctx context.Context) ([]ProviderModelBinding, error)
	DefaultLLMModelReference(ctx context.Context) (providerID, modelID string, ok bool, err error)
}

// Catalog is an immutable snapshot of the configured upstream connections and
// the models they serve.
//
// Model ids are opaque. A Catalog never splits, prefixes, or otherwise reads
// structure out of a model string. Connection selection is a lookup with a
// fixed precedence and no fallback:
//
//  1. the connection the caller names, which is how the facade token's already
//     resolved connection is looked up again at request time;
//  2. the connection declared as the owner of the catalog default model;
//  3. the only connection that serves the model;
//  4. the only configured connection when exactly one exists.
//
// Every other case is an error that names the candidate connections, so an
// operator learns about the ambiguity instead of the daemon guessing.
type Catalog struct {
	providers         map[string]Provider
	bindings          map[string]map[string]ProviderModelConfig
	serving           map[string][]string
	defaultConnection string
	defaultModel      string
}

// LoadCatalog builds a Catalog snapshot from the configuration store.
func LoadCatalog(ctx context.Context, store CatalogStore) (*Catalog, error) {
	if store == nil {
		return nil, errors.New("llm catalog store is required")
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list llm connections: %w", err)
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list llm models: %w", err)
	}
	bindings, err := store.ListLLMProviderModelConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list llm model bindings: %w", err)
	}
	defaultProviderID, defaultModelID, hasDefault, err := store.DefaultLLMModelReference(ctx)
	if err != nil {
		return nil, fmt.Errorf("read llm default model: %w", err)
	}

	catalog := &Catalog{
		providers: make(map[string]Provider, len(providers)),
		bindings:  make(map[string]map[string]ProviderModelConfig),
		serving:   make(map[string][]string),
	}
	catalog.indexProviders(providers)
	catalog.indexBindings(bindings)
	for modelID := range catalog.serving {
		sort.Strings(catalog.serving[modelID])
	}
	catalog.setDefault(defaultProviderID, defaultModelID, hasDefault, models)
	return catalog, nil
}

func (c *Catalog) indexProviders(providers []Provider) {
	for _, provider := range providers {
		if id := strings.TrimSpace(provider.ID); id != "" {
			c.providers[id] = provider
		}
	}
}

func (c *Catalog) indexBindings(bindings []ProviderModelBinding) {
	for _, binding := range bindings {
		providerID := strings.TrimSpace(binding.ProviderID)
		modelID := strings.TrimSpace(binding.ModelID)
		if providerID == "" || modelID == "" {
			continue
		}
		if _, enabled := c.providers[providerID]; !enabled {
			continue
		}
		if c.bindings[providerID] == nil {
			c.bindings[providerID] = make(map[string]ProviderModelConfig)
		}
		c.bindings[providerID][modelID] = binding.Config
		c.serving[modelID] = append(c.serving[modelID], providerID)
	}
}

// setDefault records the catalog default model. models.json declares it
// explicitly through llm_catalog_default; the daemon environment declares it
// through the llm_model default flag, whose owning connection is the only
// connection that serves the model.
func (c *Catalog) setDefault(providerID, modelID string, hasReference bool, models []Model) {
	if hasReference {
		c.defaultConnection = strings.TrimSpace(providerID)
		c.defaultModel = strings.TrimSpace(modelID)
	}
	if c.defaultModel == "" {
		c.defaultConnection, c.defaultModel = c.defaultFromModelFlag(models)
	}
	if _, ok := c.providers[c.defaultConnection]; !ok {
		c.defaultConnection = ""
	}
}

func (c *Catalog) defaultFromModelFlag(models []Model) (string, string) {
	for _, item := range models {
		if !item.DefaultModel {
			continue
		}
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		if serving := c.serving[modelID]; len(serving) == 1 {
			return serving[0], modelID
		}
		return "", modelID
	}
	return "", ""
}

// Connections returns the configured connection ids in a stable order.
func (c *Catalog) Connections() []string {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.providers))
	for id := range c.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// DefaultModel returns the configured default model, or "" when none exists.
func (c *Catalog) DefaultModel() string {
	if c == nil {
		return ""
	}
	return c.defaultModel
}

// SelectModel resolves the opaque model string to use for one agent run.
//
// A model the agent declares always wins; the configured default only fills
// the gap. There is no further fallback, so an agent with no model and a
// catalog with no default is reported as a configuration error.
func (c *Catalog) SelectModel(agentModel string) (string, error) {
	if model := strings.TrimSpace(agentModel); model != "" {
		return model, nil
	}
	if defaultModel := c.DefaultModel(); defaultModel != "" {
		return defaultModel, nil
	}
	return "", ErrNoModel
}

// Resolve returns the upstream connection and its effective per-model
// overrides for model. The returned target carries the upstream protocol and
// endpoint the daemon will forward to.
func (c *Catalog) Resolve(connectionID, model string) (ResolvedTarget, error) {
	if c == nil {
		return ResolvedTarget{}, errors.New("llm catalog is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ResolvedTarget{}, ErrNoModel
	}
	provider, err := c.connectionFor(connectionID, model)
	if err != nil {
		return ResolvedTarget{}, err
	}
	bound := c.bindingFor(provider.ID, model)
	return NewResolvedTarget(provider, Model{ID: model, Name: model, Enabled: true}, bound)
}

func (c *Catalog) connectionFor(connectionID, model string) (Provider, error) {
	if id := strings.TrimSpace(connectionID); id != "" {
		provider, ok := c.providers[id]
		if !ok {
			return Provider{}, fmt.Errorf("%w: %q", ErrConnectionNotFound, id)
		}
		return provider, nil
	}
	// A daemon with no connection at all has nothing to manage: report that
	// plainly rather than as an ambiguity between zero candidates.
	if len(c.providers) == 0 {
		return Provider{}, ErrNoConnection
	}
	if c.defaultModel != "" && c.defaultModel == model && c.defaultConnection != "" {
		if provider, ok := c.providers[c.defaultConnection]; ok {
			return provider, nil
		}
	}
	if serving := c.serving[model]; len(serving) == 1 {
		return c.providers[serving[0]], nil
	}
	if err := c.legacyQualifiedModelError(model); err != nil {
		return Provider{}, err
	}
	if len(c.providers) == 1 {
		for _, provider := range c.providers {
			return provider, nil
		}
	}
	return Provider{}, c.ambiguousConnectionError(model)
}

// legacyQualifiedModelError diagnoses the retired "<connection>/<model>" syntax.
//
// Nothing reinterprets such a value: guessing would corrupt a legitimate model id,
// which stays opaque. Without this the operator sees either an upstream "unknown
// model" rejection or an ambiguity error listing connections, and neither says
// that the model string itself is what needs changing.
func (c *Catalog) legacyQualifiedModelError(model string) error {
	if len(c.serving[model]) != 0 {
		return nil
	}
	connection, remainder, found := strings.Cut(model, "/")
	if !found || connection == "" || remainder == "" {
		return nil
	}
	if _, ok := c.providers[connection]; !ok {
		return nil
	}
	if !serves(c.serving[remainder], connection) {
		return nil
	}
	return fmt.Errorf("%w: model %q is served by no connection, but connection %q serves %q; use model: %s and make connection %q serve it",
		ErrLegacyQualifiedModel, model, connection, remainder, remainder, connection)
}

func serves(connectionIDs []string, connectionID string) bool {
	for _, candidate := range connectionIDs {
		if candidate == connectionID {
			return true
		}
	}
	return false
}

func (c *Catalog) ambiguousConnectionError(model string) error {
	candidates := c.serving[model]
	if len(candidates) == 0 {
		candidates = c.Connections()
	}
	return fmt.Errorf("%w: model %q matches %s; bind the model to exactly one connection or set a default model", ErrAmbiguousConnection, model, strings.Join(candidates, ", "))
}

func (c *Catalog) bindingFor(connectionID, model string) ProviderModelConfig {
	if bindings := c.bindings[connectionID]; bindings != nil {
		return bindings[model]
	}
	return ProviderModelConfig{}
}
