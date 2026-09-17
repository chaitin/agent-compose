package llms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// errNoDefaultConnection reports that no configured connection can act as the
// daemon default for the requested family. It is a control-flow signal rather
// than a user-facing error: callers fall through to their own diagnostics so an
// ambiguous or absent default keeps the message that names the missing input.
var errNoDefaultConnection = errors.New("no default llm connection")

// defaultConfiguredConnection returns the connection that a bare model name
// resolves against. Provider/model bindings are optional metadata, so a request
// that names only a model needs the daemon to have an unambiguous default
// connection, not a binding row.
//
// Selection order:
//  1. the reserved bootstrap connection for the family (default or anthropic);
//  2. the only configured connection when no reserved connection competes.
//
// Session-env connections are request-local credentials and never act as a
// daemon default; they are only selected explicitly.
func defaultConfiguredConnection(ctx context.Context, store ProviderListStore, providerFamily string) (Provider, error) {
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return Provider{}, fmt.Errorf("list enabled llm providers for default connection: %w", err)
	}
	family := NormalizeOptionalProviderType(providerFamily)
	candidates := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if strings.TrimSpace(provider.Scope) == ProviderScopeSessionEnv {
			continue
		}
		if family != "" && NormalizeProviderType(provider.ProviderType) != family {
			continue
		}
		candidates = append(candidates, provider)
	}
	if len(candidates) == 0 {
		return Provider{}, errNoDefaultConnection
	}
	if provider, ok := reservedDefaultConnection(candidates, family); ok {
		return provider, nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return Provider{}, ambiguousDefaultConnectionError(family, candidates)
}

// reservedDefaultConnection prefers the connection the daemon created for
// bootstrap. With no family preference the two reserved connections cannot both
// win, so the caller falls through to the ambiguity check.
func reservedDefaultConnection(candidates []Provider, family string) (Provider, bool) {
	reserved := []string{ProviderIDDefaultOpenAI, ProviderIDDefaultAnthropic}
	switch family {
	case ProviderFamilyOpenAI:
		reserved = []string{ProviderIDDefaultOpenAI}
	case ProviderFamilyAnthropic:
		reserved = []string{ProviderIDDefaultAnthropic}
	}
	selected := Provider{}
	found := false
	for _, provider := range candidates {
		if !slices.Contains(reserved, provider.ID) {
			continue
		}
		if found {
			return Provider{}, false
		}
		selected, found = provider, true
	}
	return selected, found
}

func ambiguousDefaultConnectionError(family string, candidates []Provider) error {
	ids := make([]string, 0, len(candidates))
	for _, provider := range candidates {
		ids = append(ids, provider.ID)
	}
	sort.Strings(ids)
	label := "llm"
	if family != "" {
		label = family
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf(
		"multiple %s connections are configured (%s); qualify the model as <connection>/<model>",
		label, strings.Join(ids, ", ")), nil)
}

// resolveLiteralModelTarget resolves a model name that is not registered in the
// model catalog against the daemon's default connection. Unknown models are not
// an authorization boundary: the upstream accepts or rejects them. The function
// returns (nil, nil) when no unambiguous default exists, leaving the caller's
// existing diagnostics in place.
func resolveLiteralModelTarget(ctx context.Context, store LLMResolverStore, requestedModel, providerFamily string) (*ResolvedTarget, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil, nil
	}
	provider, err := defaultConfiguredConnection(ctx, store, providerFamily)
	if errors.Is(err, errNoDefaultConnection) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	target, ok, err := resolveRuntimeLLMProviderTarget(ctx, store, requestedModel, provider.ID)
	if err != nil || !ok {
		return nil, err
	}
	return &target, nil
}
