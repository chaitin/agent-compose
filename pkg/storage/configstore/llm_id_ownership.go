package configstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/llmmodel"
	"github.com/chaitin/agent-compose/ent/dao/llmprovider"
	"github.com/chaitin/agent-compose/ent/dao/llmprovidermodel"
	"github.com/chaitin/agent-compose/pkg/llms"
)

// envProjectionConflict reports the first provider or model id the daemon
// environment projection would take over from another configuration source. It
// reads inside the projection transaction, so ownership cannot change between
// the check and the upsert that follows it.
//
// The report yields the whole projection, not just the offending id. That is
// deliberate when the conflict is the model: another source already serves the
// model the environment names, so the environment connection has nothing left
// to add, and creating it anyway would only make the two endpoints peers in
// connection selection, where one is picked at random.
//
// A source owns an id only while it can serve it: a connection row that source
// persisted disabled, and a model identity no enabled connection that source owns
// binds, answer no request. Yielding to one left a deployment with no enabled
// connection at all, and runs silently lost the daemon-managed credential even
// though the environment was the only source that could serve the model. models.json
// states that a declaration needs no credential with `"enabled": true`, so a file
// that means to own a credentialless connection says so.
//
// Taking an id over stays symmetric in both directions. The file projection
// reclaims what the bootstrap materialized — CatalogProjectionOwnsScope accepts the
// bootstrap's scopes, and its upsert matches the scope it read — so a declaration
// the file keeps is rewritten on the next startup rather than failing: nothing here
// can leave models.json unable to rewrite its own declaration. The bootstrap in
// turn maintains the rows it materialized, so an id it took over comes back under
// the file as soon as the file can serve it, and the bootstrap then yields with the
// usual warning.
//
// A row the file stops declaring loses that scope: the projection releases it, and
// the environment bootstrap maintains an unset scope. So a stale row cannot keep
// owning an id the file has moved on from, and the bootstrap recovers by itself
// instead of yielding to a source that will never rewrite the row again. The file
// projection is symmetric in the other direction — it rewrites the rows this
// bootstrap maintains as well (see CatalogProjectionOwnsScope) — so an id the
// environment materialized, including one it took over after a release, comes back
// under the file the moment the file declares it again.
func envProjectionConflict(ctx context.Context, client *dao.Client, providerID, modelID string) (*llms.DefaultConfigConflict, error) {
	provider, err := servingProviderOwner(ctx, client, providerID)
	if err != nil {
		return nil, err
	}
	if provider != nil {
		return provider.conflict(), nil
	}
	model, err := servingModelOwner(ctx, client, modelID)
	if err != nil {
		return nil, err
	}
	if model != nil {
		return model.conflict(), nil
	}
	return nil, nil
}

// llmIDOwner is the row that carries an id under a scope the environment
// bootstrap does not own.
type llmIDOwner struct {
	kind  string
	id    string
	scope string
}

func (o *llmIDOwner) conflict() *llms.DefaultConfigConflict {
	return &llms.DefaultConfigConflict{Kind: o.kind, ID: o.id, Scope: o.scope}
}

// servingProviderOwner reads the connection carrying id under a scope the
// environment bootstrap does not own, and only while that connection is enabled.
//
// An enabled connection is what makes an id worth keeping: models.json persists a
// declaration without an apiKey disabled, and an id that serves nothing must not
// suppress the bootstrap. The file projection accepts the bootstrap's scopes, so
// claiming the id cannot strand the file's own declaration: the next startup
// rewrites the row from the file, and once the file can serve it the bootstrap
// yields.
func servingProviderOwner(ctx context.Context, client *dao.Client, id string) (*llmIDOwner, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	entity, err := client.LLMProvider.Query().
		Where(llmprovider.IDEQ(id)).
		Select(llmprovider.FieldScope, llmprovider.FieldEnabled).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read provider %q ownership: %w", id, err)
	}
	scope := strings.TrimSpace(entity.Scope)
	if llms.EnvProjectionOwnsScope(scope) || entity.Enabled == 0 {
		return nil, nil
	}
	return &llmIDOwner{kind: "provider", id: id, scope: scope}, nil
}

// servingModelOwner reads the model identity carrying id under a scope the
// environment bootstrap does not own, and only while an enabled connection
// another source owns binds it. An identity another source cannot serve is not an
// owner: the environment is then the only source that can answer for the model,
// and yielding to an unservable declaration would leave the deployment with no
// enabled connection.
//
// The bootstrap's own connections do not count as another source either. The file
// projection reclaims the identity's scope on every startup
// (CatalogProjectionOwnsScope) while leaving the connection the bootstrap
// materialized enabled, so counting it made the bootstrap read its own connection
// as the owner and release it — the deployment lost the only connection that could
// serve the model on the second start.
func servingModelOwner(ctx context.Context, client *dao.Client, id string) (*llmIDOwner, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	entity, err := client.LLMModel.Query().
		Where(llmmodel.IDEQ(id), llmmodel.EnabledNEQ(0)).
		Select(llmmodel.FieldScope).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read model %q ownership: %w", id, err)
	}
	scope := strings.TrimSpace(entity.Scope)
	if llms.EnvProjectionOwnsScope(scope) {
		return nil, nil
	}
	served, err := servedByAnotherSource(ctx, client, id)
	if err != nil {
		return nil, err
	}
	if !served {
		return nil, nil
	}
	return &llmIDOwner{kind: "model", id: id, scope: scope}, nil
}

// servedByAnotherSource reports whether an enabled connection a source other than
// this bootstrap owns binds modelID.
func servedByAnotherSource(ctx context.Context, client *dao.Client, modelID string) (bool, error) {
	// Bindings carry the provider id as their key, so the join becomes two
	// reads inside the projection transaction; each provider serves the model
	// at most once.
	bindings, err := client.LLMProviderModel.Query().
		Select(llmprovidermodel.FieldID).
		Where(llmprovidermodel.ModelIDEQ(modelID)).
		All(ctx)
	if err != nil {
		return false, fmt.Errorf("read the connections serving model %q: %w", modelID, err)
	}
	if len(bindings) == 0 {
		return false, nil
	}
	providerIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		providerIDs = append(providerIDs, binding.ID)
	}
	providers, err := client.LLMProvider.Query().
		Select(llmprovider.FieldScope).
		Where(
			llmprovider.IDIn(providerIDs...),
			llmprovider.EnabledNEQ(0),
		).
		All(ctx)
	if err != nil {
		return false, fmt.Errorf("read the connections serving model %q: %w", modelID, err)
	}
	for _, provider := range providers {
		if !llms.EnvProjectionOwnsScope(provider.Scope) {
			return true, nil
		}
	}
	return false, nil
}
