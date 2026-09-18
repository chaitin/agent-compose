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
//  1. a connection of the requested family: the reserved bootstrap connection
//     (default or anthropic) when it competes, otherwise the only one;
//  2. when the requested family has no connection at all, the same choice over
//     every other family, because the runtime bridge translates across
//     protocols. The bootstrap path has always let an OpenAI-family bootstrap
//     connection serve the anthropic family; a configured connection must not
//     be stricter than the bootstrap environment. A caller whose family is a
//     hard requirement (familyIsRequired) skips this step, so it never receives
//     a connection it cannot speak.
//
// Session-env connections are request-local credentials and never act as a
// daemon default; they are only selected explicitly.
func defaultConfiguredConnection(ctx context.Context, store ProviderListStore, providerFamily string, familyIsRequired bool) (Provider, error) {
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return Provider{}, fmt.Errorf("list enabled llm providers for default connection: %w", err)
	}
	family := NormalizeOptionalProviderType(providerFamily)
	selectionFamily := family
	candidates := configuredConnections(providers, family)
	if len(candidates) == 0 && family != "" && !familyIsRequired {
		selectionFamily = ""
		candidates = configuredConnections(providers, "")
	}
	if len(candidates) == 0 {
		return Provider{}, errNoDefaultConnection
	}
	if provider, ok := reservedDefaultConnection(candidates, selectionFamily); ok {
		return provider, nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return Provider{}, ambiguousDefaultConnectionError(selectionFamily, candidates)
}

// configuredConnections returns the connections that can act as a daemon
// default, optionally narrowed to one provider family.
func configuredConnections(providers []Provider, family string) []Provider {
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
	return candidates
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

// ErrAmbiguousDefaultConnection reports that more than one configured
// connection could serve a model that names none of them. It is a configuration
// error the operator must resolve, unlike the absence of any connection, which
// lets an agent fall back to credentials it carries itself.
var ErrAmbiguousDefaultConnection = errors.New("ambiguous default llm connection")

// ambiguousConnectionKind classifies the ambiguity as a failed precondition so
// transport mapping is unchanged, while keeping it distinguishable from the
// failed preconditions that only mean "nothing is configured". It is always
// paired with a reason, so its own text is never the user-facing message.
type ambiguousConnectionKind struct{}

func (ambiguousConnectionKind) Error() string { return domain.ErrFailedPrecondition.Error() }

func (ambiguousConnectionKind) Is(target error) bool {
	return target == domain.ErrFailedPrecondition || target == ErrAmbiguousDefaultConnection
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
	return domain.ClassifyError(ambiguousConnectionKind{}, fmt.Sprintf(
		"multiple %s connections are configured (%s); qualify the model as <connection>/<model>",
		label, strings.Join(ids, ", ")), nil)
}

// resolveLiteralModelTarget resolves a model name that is not registered in the
// model catalog against the daemon's default connection. Unknown models are not
// an authorization boundary: the upstream accepts or rejects them. The function
// returns (nil, nil) when no unambiguous default exists, leaving the caller's
// existing diagnostics in place.
func resolveLiteralModelTarget(ctx context.Context, store LLMResolverStore, requestedModel, providerFamily string, familyIsRequired bool) (*ResolvedTarget, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil, nil
	}
	provider, err := defaultConfiguredConnection(ctx, store, providerFamily, familyIsRequired)
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
