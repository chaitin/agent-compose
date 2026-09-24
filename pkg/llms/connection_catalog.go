package llms

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
)

var (
	// ErrNoModel reports that neither the agent nor the catalog declares a model.
	ErrNoModel = errors.New("no llm model configured")
	// ErrNoConnection reports that the daemon has no upstream connection at all.
	// It is the "nothing is configured" counterpart to ErrNoModel, and callers
	// treat it the same way: the agent keeps its own authentication.
	ErrNoConnection = errors.New("no llm connection configured")
	// ErrConnectionNotFound reports a connection id that matches no configured
	// connection.
	ErrConnectionNotFound = errors.New("llm connection not found")
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
// fixed precedence:
//
//  1. the connection the caller names, which is how the facade token's already
//     resolved connection is looked up again at request time;
//  2. among the connections that declare the model, the one serving the
//     caller's most preferred protocol, so a run is served by a passthrough
//     whenever a connection can do that; connections that serve the model over
//     the same protocol are interchangeable and one of them is chosen at
//     random, which spreads runs instead of pinning whichever id sorts first;
//  3. the connection the catalog default model names, when no connection
//     declares the model;
//  4. the only configured connection, when exactly one exists;
//  5. otherwise every configured connection, ranked the same way: a model id is
//     opaque, so a connection that never declared the model may still serve it.
type Catalog struct {
	providers         map[string]Provider
	bindings          map[string]map[string]ProviderModelConfig
	serving           map[string][]string
	defaultConnection string
	defaultModel      string
	chooser           ConnectionChooser
}

// ConnectionChooser picks one index out of connections that are otherwise
// interchangeable to the caller.
type ConnectionChooser func(candidates int) int

// CatalogOption customizes how a loaded Catalog resolves a connection.
type CatalogOption func(*Catalog)

// WithConnectionChooser replaces the random tie-break a Catalog applies when
// several connections serve a model over equally preferred protocols. Tests
// inject a chooser to make that pick observable; production keeps the default.
func WithConnectionChooser(chooser ConnectionChooser) CatalogOption {
	return func(catalog *Catalog) {
		if chooser != nil {
			catalog.chooser = chooser
		}
	}
}

// chooseRandomCandidate is the default tie-break. Connections that serve one
// model over the same protocol are interchangeable, so spreading runs across
// them beats pinning whichever id sorts first.
func chooseRandomCandidate(candidates int) int {
	if candidates <= 1 {
		return 0
	}
	return rand.IntN(candidates)
}

// LoadCatalog builds a Catalog snapshot from the configuration store.
func LoadCatalog(ctx context.Context, store CatalogStore, options ...CatalogOption) (*Catalog, error) {
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
	for _, option := range options {
		option(catalog)
	}
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
		// A connection derived from an agent's own declaration is addressable
		// only by the run that declared it. Adding it to serving would make one
		// agent's credential a candidate for every other agent that names the
		// same model.
		if !IsDeclaredConnectionID(providerID) {
			c.serving[modelID] = append(c.serving[modelID], providerID)
		}
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

// Connections returns the operator-configured connection ids in a stable order.
// Connections derived from an agent declaration are excluded: they belong to one
// run and are never part of the daemon's shared configuration.
func (c *Catalog) Connections() []string {
	return c.configuredConnectionIDs()
}

// configuredConnectionIDs returns the operator-configured connection ids in a
// stable order.
func (c *Catalog) configuredConnectionIDs() []string {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.providers))
	for id := range c.providers {
		if IsDeclaredConnectionID(id) {
			continue
		}
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
//
// preference orders the upstream protocols the caller can serve, best first, so
// a model that several connections serve resolves to a connection the caller
// speaks natively whenever one exists. A caller that names a connection outright
// does not need a preference; nil means "no preference", which leaves only the
// random tie-break between equally ranked connections.
func (c *Catalog) Resolve(connectionID, model string, preference ProtocolPreference) (ResolvedTarget, error) {
	if c == nil {
		return ResolvedTarget{}, errors.New("llm catalog is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ResolvedTarget{}, ErrNoModel
	}
	provider, err := c.connectionFor(connectionID, model, preference)
	if err != nil {
		return ResolvedTarget{}, err
	}
	bound := c.bindingFor(provider.ID, model)
	return NewResolvedTarget(provider, Model{ID: model, Name: model, Enabled: true}, bound)
}

func (c *Catalog) connectionFor(connectionID, model string, preference ProtocolPreference) (Provider, error) {
	if id := strings.TrimSpace(connectionID); id != "" {
		provider, ok := c.providers[id]
		if !ok {
			return Provider{}, fmt.Errorf("%w: %q", ErrConnectionNotFound, id)
		}
		return provider, nil
	}
	// A daemon with no configured connection has nothing to manage: report that
	// plainly rather than as a choice between zero candidates. Connections
	// derived from an agent declaration do not count, because they are not
	// candidates for any run but the one that declared them.
	configured := c.configuredConnectionIDs()
	if len(configured) == 0 {
		return Provider{}, ErrNoConnection
	}
	if err := c.legacyQualifiedModelError(model); err != nil {
		return Provider{}, err
	}
	if candidates := c.serving[model]; len(candidates) != 0 {
		return c.chooseConnection(candidates, model, preference)
	}
	// No connection declares this model. The catalog default is the operator's
	// explicit answer for exactly this model, so it is consulted before the
	// catalog falls back to treating every connection as a candidate.
	if c.defaultModel != "" && c.defaultModel == model && c.defaultConnection != "" {
		if provider, ok := c.providers[c.defaultConnection]; ok {
			return provider, nil
		}
	}
	if len(configured) == 1 {
		return c.providers[configured[0]], nil
	}
	return c.chooseConnection(configured, model, preference)
}

// chooseConnection returns the candidate that serves model over the most
// preferred protocol, breaking ties with the catalog's chooser. Candidates are
// configured connection ids, so at least one of them always qualifies.
func (c *Catalog) chooseConnection(candidates []string, model string, preference ProtocolPreference) (Provider, error) {
	var best []string
	bestRank := 0
	for _, id := range candidates {
		if _, ok := c.providers[id]; !ok {
			continue
		}
		rank := preference.rank(c.effectiveWireAPI(id, model))
		switch {
		case best == nil || rank < bestRank:
			best, bestRank = []string{id}, rank
		case rank == bestRank:
			best = append(best, id)
		}
	}
	if len(best) == 0 {
		return Provider{}, fmt.Errorf("no llm connection serves model %q", model)
	}
	chooser := c.chooser
	if chooser == nil {
		chooser = chooseRandomCandidate
	}
	return c.providers[best[chooser(len(best))]], nil
}

// effectiveWireAPI is the protocol a connection really serves model over: a
// per-model binding may override the connection's own protocol, and the
// preference has to rank what the run would actually use.
func (c *Catalog) effectiveWireAPI(connectionID, model string) Protocol {
	provider := c.providers[connectionID]
	return NormalizeProtocol(firstNonEmptyTrimmed(c.bindingFor(connectionID, model).WireAPI, provider.DefaultWireAPI))
}

// legacyQualifiedModelError diagnoses the retired "<connection>/<model>" syntax.
//
// Nothing reinterprets such a value: guessing would corrupt a legitimate model id,
// which stays opaque. Without this the operator only sees an upstream "unknown
// model" rejection, which does not say that the model string itself needs fixing.
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

func (c *Catalog) bindingFor(connectionID, model string) ProviderModelConfig {
	if bindings := c.bindings[connectionID]; bindings != nil {
		return bindings[model]
	}
	return ProviderModelConfig{}
}
