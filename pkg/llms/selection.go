package llms

import (
	"context"
	"sort"
	"strings"
)

type ProviderModelWireAPIStore interface {
	LLMProviderModelWireAPI(ctx context.Context, providerID, modelID string) (string, bool, error)
}

func SelectModel(models []Model, requested string) Model {
	requested = strings.TrimSpace(requested)
	for _, model := range models {
		if requested != "" && (model.ID == requested || model.Name == requested) {
			return model
		}
	}
	if requested != "" {
		return Model{}
	}
	for _, model := range models {
		if model.DefaultModel {
			return model
		}
	}
	return models[0]
}

func SelectModelAndProvider(ctx context.Context, store ProviderModelWireAPIStore, models []Model, providers []Provider, requestedModel, providerFamily, providerID string) (Model, Provider, string, bool, error) {
	if strings.TrimSpace(requestedModel) != "" {
		requested := SelectModel(models, requestedModel)
		if strings.TrimSpace(requested.ID) == "" {
			return Model{}, Provider{}, "", false, nil
		}
		provider, wireAPI, ok, err := SelectProviderForModel(ctx, store, providers, requested.ID, providerFamily, providerID)
		return requested, provider, wireAPI, ok, err
	}
	ordered := append([]Model(nil), models...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].DefaultModel != ordered[j].DefaultModel {
			return ordered[i].DefaultModel
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, model := range ordered {
		provider, wireAPI, ok, err := SelectProviderForModel(ctx, store, providers, model.ID, providerFamily, providerID)
		if err != nil {
			return Model{}, Provider{}, "", false, err
		}
		if ok {
			return model, provider, wireAPI, true, nil
		}
	}
	return Model{}, Provider{}, "", false, nil
}

func SelectProviderForModel(ctx context.Context, store ProviderModelWireAPIStore, providers []Provider, modelID, providerFamily, providerID string) (Provider, string, bool, error) {
	type candidate struct {
		provider Provider
		wireAPI  string
		priority int
	}
	if strings.TrimSpace(providerFamily) != "" {
		providerFamily = NormalizeProviderType(providerFamily)
	}
	providerID = strings.TrimSpace(providerID)
	var candidates []candidate
	for _, provider := range providers {
		if providerID == "" && providerFamily != "" && NormalizeProviderType(provider.ProviderType) != providerFamily {
			continue
		}
		if providerID != "" && provider.ID != providerID {
			continue
		}
		if providerID == "" && (strings.TrimSpace(provider.Scope) == ProviderScopeSessionEnv || strings.TrimSpace(provider.Scope) == ProviderScopeFacadeEnv) {
			continue
		}
		wireAPI, ok, err := store.LLMProviderModelWireAPI(ctx, provider.ID, modelID)
		if err != nil {
			return Provider{}, "", false, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{provider: provider, wireAPI: firstNonEmpty(wireAPI, NormalizeWireAPI(provider.DefaultWireAPI)), priority: ProviderSelectionPriority(provider.Scope)})
	}
	if len(candidates) == 0 {
		return Provider{}, "", false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		if candidates[i].provider.Weight == candidates[j].provider.Weight {
			return candidates[i].provider.ID < candidates[j].provider.ID
		}
		return candidates[i].provider.Weight < candidates[j].provider.Weight
	})
	return candidates[0].provider, candidates[0].wireAPI, true, nil
}

func ProviderSelectionPriority(scope string) int {
	switch strings.TrimSpace(scope) {
	case ProviderScopeFacadeEnv:
		return 3
	case ProviderScopeSessionEnv:
		return 2
	case ProviderScopeEnvDefault:
		return 1
	default:
		return 0
	}
}
