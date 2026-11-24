package configstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/llmcatalogdefault"
	"github.com/chaitin/agent-compose/ent/dao/llmprovider"
	"github.com/chaitin/agent-compose/ent/dao/llmprovidermodel"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// projectCatalogDefault applies the `default` key of one models.json projection
// and reports the effective reference it could not leave in place, if any.
//
// Three cases. An absent key (nil) leaves the stored reference alone, so a default
// an operator chose through the LLM service survives every restart and a later
// file can restore a declaration it dropped. A present but empty value is the file
// declaring that it has no default, which clears the stored reference. Any other
// value must name a connection the file declares and enables.
//
// A declaration the store cannot serve is not a reason to refuse the whole file —
// every other declaration in it is valid, and refusing would keep the daemon from
// starting — so it is reported instead. The stored reference is only removed when
// it cannot resolve either: the file naming an id it does not control says nothing
// about whether the reference already stored is usable. Only a store failure is
// returned as an error.
func projectCatalogDefault(ctx context.Context, client *dao.Client, reference *string, now int64) (*llms.CatalogDefaultDropped, error) {
	if reference == nil {
		return nil, nil
	}
	declared := strings.TrimSpace(*reference)
	if declared == "" {
		previous, ok, err := storedCatalogDefault(ctx, client)
		if err != nil {
			return nil, err
		}
		if err := clearCatalogDefault(ctx, client); err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		return &llms.CatalogDefaultDropped{Stored: previous, Reason: "models.json declares no default"}, nil
	}
	if err := applyCatalogDefault(ctx, client, declared, now); err != nil {
		if !errors.Is(err, domain.ErrInvalidArgument) {
			return nil, err
		}
		return refuseCatalogDefault(ctx, client, declared, err)
	}
	return nil, nil
}

// refuseCatalogDefault handles a `default` the file declared but the store cannot
// serve.
func refuseCatalogDefault(ctx context.Context, client *dao.Client, declared string, cause error) (*llms.CatalogDefaultDropped, error) {
	previous, ok, err := storedCatalogDefault(ctx, client)
	if err != nil {
		return nil, err
	}
	if ok {
		resolves, err := storedDefaultResolves(ctx, client, previous)
		if err != nil {
			return nil, err
		}
		if resolves {
			return &llms.CatalogDefaultDropped{Declared: declared, Reason: cause.Error()}, nil
		}
	}
	if err := clearCatalogDefault(ctx, client); err != nil {
		return nil, err
	}
	return &llms.CatalogDefaultDropped{Declared: declared, Stored: previous, Reason: cause.Error()}, nil
}

// storedDefaultResolves reports whether the connection a stored reference names is
// still enabled, which is what makes the reference usable as the default.
//
// The model does not have to be one the connection enumerates: the catalog
// resolves a default by naming its connection outright and keeps the model opaque,
// so a literal model is served with the connection's defaults. Requiring a binding
// here deleted an operator's working default whenever the file declared one it
// could not apply.
func storedDefaultResolves(ctx context.Context, client *dao.Client, reference string) (bool, error) {
	providerID, _, ok := llms.SplitProviderModelReference(reference)
	if !ok {
		return false, nil
	}
	count, err := client.LLMProvider.Query().
		Where(llmprovider.IDEQ(providerID), llmprovider.EnabledNEQ(0)).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("read whether the stored llm default resolves: %w", err)
	}
	return count > 0, nil
}

// storedCatalogDefault reads the effective default reference, or reports that the
// store holds none.
func storedCatalogDefault(ctx context.Context, client *dao.Client) (string, bool, error) {
	entity, err := client.LLMCatalogDefault.Query().
		Where(llmcatalogdefault.IDEQ(1)).
		Select(llmcatalogdefault.FieldProviderID, llmcatalogdefault.FieldModelID).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read the stored llm default: %w", err)
	}
	providerID, modelID := strings.TrimSpace(entity.ProviderID), strings.TrimSpace(entity.ModelID)
	if providerID == "" || modelID == "" {
		return "", false, nil
	}
	return providerID + "/" + modelID, true, nil
}

func clearCatalogDefault(ctx context.Context, client *dao.Client) error {
	if _, err := client.LLMCatalogDefault.Delete().Exec(ctx); err != nil {
		return fmt.Errorf("clear model catalog default: %w", err)
	}
	return nil
}

func applyCatalogDefault(ctx context.Context, client *dao.Client, reference string, now int64) error {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		// projectCatalogDefault decides what an empty declaration means; nothing is
		// stored from one.
		return nil
	}
	providerID, modelID, ok := llms.SplitProviderModelReference(reference)
	if !ok {
		return fmt.Errorf("%w: models.json default %q is not a <provider>/<model> reference", domain.ErrInvalidArgument, reference)
	}
	// The default must name a connection models.json itself declared and that can
	// actually serve a request. A reference to a connection this file lost control
	// of, or to one without a credential, would otherwise be stored as the
	// effective default while resolution ignores it: GetDefaultModel would report
	// a default no run receives, and a run that declares no model would be routed
	// by the connection fallback instead.
	entity, err := client.LLMProvider.Query().
		Where(llmprovider.IDEQ(providerID)).
		Select(llmprovider.FieldScope, llmprovider.FieldEnabled, llmprovider.FieldDefaultWireAPI).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return fmt.Errorf("%w: models.json default %q names connection %q, which models.json does not declare", domain.ErrInvalidArgument, reference, providerID)
		}
		return fmt.Errorf("resolve model catalog default provider %q: %w", providerID, err)
	}
	// A released row (unset scope) is a connection a previous models.json declared
	// and this one does not: the row survives so a later file can restore it, but it
	// cannot serve the default this file is asking for.
	owner := strings.TrimSpace(entity.Scope)
	if owner == "" {
		return fmt.Errorf("%w: models.json default %q names connection %q, which models.json does not declare", domain.ErrInvalidArgument, reference, providerID)
	}
	if owner != llms.ProviderScopeCatalog {
		return fmt.Errorf("%w: models.json default %q names connection %q, which scope %q owns", domain.ErrInvalidArgument, reference, providerID, owner)
	}
	if entity.Enabled == 0 {
		return fmt.Errorf("%w: models.json default %q names connection %q, which is disabled: declare an apiKey, or \"enabled\": true for a connection that needs no credential", domain.ErrInvalidArgument, reference, providerID)
	}
	if err := ensureCatalogModelIdentity(ctx, client, modelID, now); err != nil {
		return fmt.Errorf("upsert model catalog default model: %w", err)
	}
	if err := client.LLMProviderModel.Create().
		SetID(providerID).
		SetModelID(modelID).
		SetWireAPI(entity.DefaultWireAPI).
		SetWeight(10).
		SetBaseURL("").
		SetHeadersJSON("{}").
		SetMaxOutputTokens(0).
		SetDisplayName("").
		OnConflict(
			entsql.ConflictColumns(llmprovidermodel.FieldID, llmprovidermodel.FieldModelID),
			entsql.ResolveWithIgnore(),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("bind model catalog default: %w", err)
	}
	if err := clearCatalogDefault(ctx, client); err != nil {
		return err
	}
	if _, err := client.LLMCatalogDefault.Create().
		SetID(1).
		SetProviderID(providerID).
		SetModelID(modelID).
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		return fmt.Errorf("store model catalog default: %w", err)
	}
	return nil
}
