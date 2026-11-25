package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/llmcatalogdefault"
	"github.com/chaitin/agent-compose/ent/dao/llmmodel"
	"github.com/chaitin/agent-compose/ent/dao/llmprovider"
	"github.com/chaitin/agent-compose/ent/dao/llmprovidermodel"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ApplyModelCatalog atomically projects models.json into the runtime
// configuration store.
func (s *llmStore) ApplyModelCatalog(ctx context.Context, catalog llms.ModelCatalog) error {
	now := time.Now().UTC().Unix()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin model catalog transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	// Release every catalog row before re-declaring what the file declares. A row
	// the file still declares is rewritten below and gets the catalog scope back; a
	// row the file no longer declares stays released (scope unset) and disabled, so
	// it stops owning an id the file has moved on from. The row itself is kept
	// because deleting it would break two promises: llm_catalog_default references
	// the provider row with ON DELETE CASCADE, so an operator's stored default must
	// survive a declaration that no longer serves it, and a later file must be able
	// to restore the id.
	//
	// The released connections give up their model bindings with the id. A binding
	// is what makes a connection serve a model, and it carries the file's own
	// endpoint and header overrides, so a connection that later takes the released
	// id over — the environment bootstrap reclaims the openai/anthropic ids, and
	// that path is supported — must not inherit models it never declared.
	//
	// Materialize the released connection ids first, then delete their model
	// bindings. Both statements share the catalog transaction, so the id set
	// cannot change between them.
	catalogProviders, err := client.LLMProvider.Query().
		Select(llmprovider.FieldID).
		Where(llmprovider.ScopeEQ(llms.ProviderScopeCatalog)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("read released model catalog providers: %w", err)
	}
	if len(catalogProviders) > 0 {
		releasedIDs := make([]string, 0, len(catalogProviders))
		for _, provider := range catalogProviders {
			releasedIDs = append(releasedIDs, provider.ID)
		}
		if _, err := client.LLMProviderModel.Delete().
			Where(llmprovidermodel.IDIn(releasedIDs...)).
			Exec(ctx); err != nil {
			return fmt.Errorf("release stale model catalog bindings: %w", err)
		}
	}
	if _, err := client.LLMProvider.Update().
		Where(llmprovider.ScopeEQ(llms.ProviderScopeCatalog)).
		SetEnabled(0).
		SetScope("").
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		return fmt.Errorf("release stale model catalog providers: %w", err)
	}
	if _, err := client.LLMModel.Update().
		Where(llmmodel.ScopeEQ(llms.ProviderScopeCatalog)).
		SetEnabled(0).
		SetScope("").
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		return fmt.Errorf("release stale model catalog models: %w", err)
	}
	providerIDs := make([]string, 0, len(catalog.Providers))
	for id := range catalog.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		if err := applyCatalogProvider(ctx, client, catalogProviderUpsert{
			ProviderID: providerID, Definition: catalog.Providers[providerID], Now: now,
		}); err != nil {
			return err
		}
	}
	dropped, err := projectCatalogDefault(ctx, client, catalog.Default, now)
	if err != nil {
		return err
	}
	// The file may have dropped a model whose binding was the only thing keeping
	// an API-owned identity alive, and a default reference it replaced may have
	// been the same for a literal model an operator chose. An unreferenced
	// identity still claims the id: the environment bootstrap treats a row that
	// belongs to another source as an owner and yields its whole projection, so a
	// leftover would silently disable LLM_MODEL. The sweep runs here rather than
	// beside each release because only the fully projected state says which ids
	// are still referenced.
	if err := sweepUnreferencedAPIModels(ctx, client); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model catalog transaction: %w", err)
	}
	// A dropped reference is reported after the commit, because the projection it
	// belongs to succeeded: every other declaration in the file is applied, and the
	// caller must not read this as a failed write.
	if dropped != nil {
		return dropped
	}
	return nil
}

// storedProviderScope reads the scope of the stored connection carrying
// providerID, or "" when no row declares that id.
func storedProviderScope(ctx context.Context, client *dao.Client, providerID string) (string, error) {
	entity, err := client.LLMProvider.Query().
		Where(llmprovider.IDEQ(providerID)).
		Select(llmprovider.FieldScope).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read provider %q scope: %w", providerID, err)
	}
	return strings.TrimSpace(entity.Scope), nil
}

// catalogProviderUpsert bundles one provider catalog entry and the apply timestamp.
type catalogProviderUpsert struct {
	ProviderID string
	Definition llms.CatalogProvider
	Now        int64
}

func applyCatalogProvider(ctx context.Context, client *dao.Client, upsert catalogProviderUpsert) error {
	providerID, definition, now := upsert.ProviderID, upsert.Definition, upsert.Now
	protocol := llms.NormalizeWireAPI(pointerString(definition.Protocol))
	providerType, authHeader, authScheme := llms.ProviderFamilyOpenAI, "Authorization", "Bearer"
	if protocol == llms.APIProtocolMessages {
		providerType, authHeader, authScheme = llms.ProviderFamilyAnthropic, "x-api-key", ""
	}
	name := firstNonEmptyString(pointerString(definition.Name), providerID)
	apiKey := pointerString(definition.APIKey)
	headersJSON, err := encodeCatalogHeaders(definition.Headers)
	if err != nil {
		return fmt.Errorf("encode provider %q headers: %w", providerID, err)
	}
	enabled := apiKey != ""
	if definition.Enabled != nil {
		// A connection that needs no credential — a local endpoint, for example —
		// says so explicitly; the file is otherwise the source of truth for
		// whether the connection can serve a default.
		enabled = *definition.Enabled
	}
	// The row may be the file's own, one a previous projection released, or one the
	// environment bootstrap materialized. An id an explicit source owns (an
	// LLM-service connection or a run-scoped declared one) is not the file's to
	// rewrite, and neither is a row another writer changed while this transaction
	// ran: the upsert below matches only the scope accepted here, so both fail as a
	// conflict instead of overwriting configuration the file does not control.
	scope, err := storedProviderScope(ctx, client, providerID)
	if err != nil {
		return err
	}
	if !llms.CatalogProjectionOwnsScope(scope) {
		return fmt.Errorf("model catalog provider %q conflicts with an existing non-catalog provider", providerID)
	}
	// Kept as SQL: the upsert updates only a row whose stored scope still matches
	// the scope read above (ON CONFLICT ... DO UPDATE ... WHERE), and the affected
	// row count is what reports the ownership conflict. ent's upsert builder cannot
	// add a WHERE clause to the updating branch.
	result, err := client.ExecContext(ctx, `INSERT INTO llm_provider(
		id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json,
		use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 10, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		name = excluded.name, provider_type = excluded.provider_type, default_wire_api = excluded.default_wire_api,
		base_url = excluded.base_url, api_key = excluded.api_key, auth_header = excluded.auth_header,
		auth_scheme = excluded.auth_scheme, headers_json = excluded.headers_json, enabled = excluded.enabled,
		scope = excluded.scope, updated_at = excluded.updated_at
		WHERE llm_provider.scope = ?`,
		providerID, name, providerType, protocol, strings.TrimSpace(pointerString(definition.BaseURL)), apiKey,
		authHeader, authScheme, headersJSON, BoolToInt(enabled), llms.ProviderScopeCatalog, now, now, scope)
	if err != nil {
		return fmt.Errorf("upsert model catalog provider %q: %w", providerID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect model catalog provider %q upsert: %w", providerID, err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("model catalog provider %q conflicts with an existing non-catalog provider", providerID)
	}
	if _, err := client.LLMProviderModel.Delete().
		Where(llmprovidermodel.IDEQ(providerID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("replace provider %q model catalog: %w", providerID, err)
	}
	for _, model := range definition.Models {
		if err := applyCatalogModel(ctx, client, catalogModelUpsert{
			ProviderID: providerID, ProviderProtocol: protocol, Definition: model, Now: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// catalogModelUpsert bundles one provider's model catalog entry and the apply timestamp.
type catalogModelUpsert struct {
	ProviderID       string
	ProviderProtocol string
	Definition       llms.CatalogModel
	Now              int64
}

func applyCatalogModel(ctx context.Context, client *dao.Client, upsert catalogModelUpsert) error {
	providerID, providerProtocol, definition, now := upsert.ProviderID, upsert.ProviderProtocol, upsert.Definition, upsert.Now
	modelID := strings.TrimSpace(definition.ID)
	if err := ensureCatalogModelIdentity(ctx, client, modelID, now); err != nil {
		return fmt.Errorf("upsert provider %q model %q: %w", providerID, modelID, err)
	}
	protocol := providerProtocol
	if definition.Protocol != nil {
		protocol = llms.NormalizeWireAPI(*definition.Protocol)
	}
	headersJSON, err := encodeCatalogHeaders(definition.Headers)
	if err != nil {
		return fmt.Errorf("encode provider %q model %q headers: %w", providerID, modelID, err)
	}
	maxOutputTokens := 0
	if definition.MaxOutputTokens != nil {
		maxOutputTokens = *definition.MaxOutputTokens
	}
	contextWindow := 0
	if definition.ContextWindow != nil {
		contextWindow = *definition.ContextWindow
	}
	if _, err := client.LLMProviderModel.Create().
		SetID(providerID).
		SetModelID(modelID).
		SetWireAPI(protocol).
		SetWeight(10).
		SetBaseURL(strings.TrimSpace(pointerString(definition.BaseURL))).
		SetHeadersJSON(headersJSON).
		SetMaxOutputTokens(maxOutputTokens).
		SetContextWindow(contextWindow).
		SetDisplayName(pointerString(definition.Name)).
		Save(ctx); err != nil {
		return fmt.Errorf("bind provider %q model %q: %w", providerID, modelID, err)
	}
	return nil
}

// ensureCatalogModelIdentity declares a model identity the file references, and
// reclaims the identity a previous projection released.
//
// It follows the same ownership rule the provider upsert applies
// (CatalogProjectionOwnsScope) rather than accepting only its own rows. The
// identity row is what the environment projection reads to decide whether another
// source already serves the model it names, so a row the bootstrap kept owning
// made the file's declaration invisible to it: the environment materialized a
// second connection for the same model, and connection selection picked between
// the two peers at random. Reclaiming the row is also what the manual promises
// for an id the environment materialized on its own.
//
// An identity an explicit source owns is left alone. Model identities are shared
// by id and the binding written by the caller is what makes the model servable, so
// a model an LLM-service or run-scoped writer declared keeps its scope and its
// metadata.
func ensureCatalogModelIdentity(ctx context.Context, client *dao.Client, modelID string, now int64) error {
	scope, err := storedModelScope(ctx, client, modelID)
	if err != nil {
		return err
	}
	if !llms.CatalogProjectionOwnsScope(scope) {
		return nil
	}
	// Kept as SQL: the upsert takes over only an identity whose stored scope still
	// matches the scope read above (ON CONFLICT ... DO UPDATE ... WHERE); ent's
	// upsert builder cannot add a WHERE clause to the updating branch.
	_, err = client.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, '', 0, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET enabled = 1, scope = excluded.scope, updated_at = excluded.updated_at
		WHERE llm_model.scope = ?`, modelID, modelID, llms.ProviderScopeCatalog, now, now, scope)
	return err
}

// storedModelScope reads the scope of the stored model identity carrying modelID,
// or "" when no row declares that id.
func storedModelScope(ctx context.Context, client *dao.Client, modelID string) (string, error) {
	entity, err := client.LLMModel.Query().
		Where(llmmodel.IDEQ(modelID)).
		Select(llmmodel.FieldScope).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read model %q scope: %w", modelID, err)
	}
	return strings.TrimSpace(entity.Scope), nil
}

func encodeCatalogHeaders(headers map[string]string) (string, error) {
	if len(headers) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(headers)
	return string(data), err
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// DefaultLLMModelReference returns the effective default model: the reference
// models.json declared, or the one an operator set through the LLM service when
// models.json declares none.
//
// The row is the operator's stored choice, not a promise that it can be served:
// a connection removed by a models.json projection leaves the reference in place
// so a later projection can restore it, and resolution treats it as absent until
// then. The reference's own connection is therefore not checked here.
func (s *llmStore) DefaultLLMModelReference(ctx context.Context) (providerID, modelID string, ok bool, err error) {
	entity, err := s.client.LLMCatalogDefault.Query().
		Where(llmcatalogdefault.IDEQ(1)).
		Select(llmcatalogdefault.FieldProviderID, llmcatalogdefault.FieldModelID).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("query model catalog default: %w", err)
	}
	return entity.ProviderID, entity.ModelID, true, nil
}

// clearDefaultReferenceForProvider removes a stored default naming a connection
// the LLM service just deleted or disabled. Unlike a models.json projection,
// which preserves the reference so a later file can restore it, an operator
// action on the connection is deliberate: leaving the row would make
// GetDefaultModel report a default that resolution ignores and no run receives.
func clearDefaultReferenceForProvider(ctx context.Context, client *dao.Client, providerID string) error {
	if _, err := client.LLMCatalogDefault.Delete().
		Where(llmcatalogdefault.ProviderIDEQ(providerID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("clear default model of provider %q: %w", providerID, err)
	}
	return nil
}

// SetDefaultLLMModel records the effective default model, replacing any earlier
// reference. The connection must exist and be enabled, because the reference is
// what the daemon falls back to for runs that declare no model; the model
// itself may be a literal the connection does not enumerate, so only its
// identity row is ensured and no binding is invented.
func (s *llmStore) SetDefaultLLMModel(ctx context.Context, reference llms.ModelReference) (llms.ModelReference, error) {
	providerID := strings.TrimSpace(reference.ProviderID)
	modelID := strings.TrimSpace(reference.ModelID)
	if providerID == "" || modelID == "" {
		return llms.ModelReference{}, fmt.Errorf("%w: provider_id and model_id are required", domain.ErrInvalidArgument)
	}
	if llms.IsDeclaredConnectionID(providerID) {
		// A declared connection carries one sandbox's own credential and is
		// addressable only by the run that declared it. Making it the daemon
		// default would offer that credential to every run that names the model.
		return llms.ModelReference{}, fmt.Errorf("%w: provider %q is a run-scoped declared connection", domain.ErrInvalidArgument, providerID)
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return llms.ModelReference{}, fmt.Errorf("begin default model update: %w", err)
	}
	// Rollback after commit is harmless; on errors it releases the transaction.
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	provider, err := client.LLMProvider.Query().
		Where(llmprovider.IDEQ(providerID)).
		Select(llmprovider.FieldEnabled).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return llms.ModelReference{}, fmt.Errorf("%w: llm provider not found", domain.ErrNotFound)
		}
		return llms.ModelReference{}, fmt.Errorf("read default model provider: %w", err)
	}
	if provider.Enabled == 0 {
		return llms.ModelReference{}, fmt.Errorf("%w: provider %q is disabled", domain.ErrFailedPrecondition, providerID)
	}
	now := time.Now().UTC().Unix()
	if err := client.LLMModel.Create().
		SetID(modelID).
		SetName(modelID).
		SetDescription("").
		SetDefaultModel(0).
		SetEnabled(1).
		SetScope(llms.ProviderScopeAPI).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		OnConflict(entsql.ConflictColumns(llmmodel.FieldID), entsql.ResolveWithIgnore()).
		Exec(ctx); err != nil {
		return llms.ModelReference{}, fmt.Errorf("record default model identity: %w", err)
	}
	if _, err := client.LLMCatalogDefault.Delete().Exec(ctx); err != nil {
		return llms.ModelReference{}, fmt.Errorf("clear llm default model: %w", err)
	}
	if _, err := client.LLMCatalogDefault.Create().
		SetID(1).
		SetProviderID(providerID).
		SetModelID(modelID).
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		return llms.ModelReference{}, fmt.Errorf("store llm default model: %w", err)
	}
	// The reference just stored protects this model's identity row, so a previous
	// default whose identity nothing else uses is swept here rather than left to
	// claim its id against the environment bootstrap.
	if err := sweepUnreferencedAPIModels(ctx, client); err != nil {
		return llms.ModelReference{}, err
	}
	if err := tx.Commit(); err != nil {
		return llms.ModelReference{}, fmt.Errorf("commit default model update: %w", err)
	}
	return llms.ModelReference{ProviderID: providerID, ModelID: modelID}, nil
}

// ClearDefaultLLMModel removes the effective default model. A models.json that
// declares its own default restores that reference at the next startup.
func (s *llmStore) ClearDefaultLLMModel(ctx context.Context) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin default model clear: %w", err)
	}
	// Rollback after commit is harmless; on errors it releases the transaction.
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.LLMCatalogDefault.Delete().Exec(ctx); err != nil {
		return fmt.Errorf("clear llm default model: %w", err)
	}
	// Removing the reference can be what makes a literal model's identity row
	// unreferenced, so the sweep belongs in this transaction too.
	if err := sweepUnreferencedAPIModels(ctx, client); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit default model clear: %w", err)
	}
	return nil
}

// LLMProviderModelConfig returns all effective model-binding overrides.
func (s *llmStore) LLMProviderModelConfig(ctx context.Context, providerID, modelID string) (llms.ProviderModelConfig, bool, error) {
	entity, err := s.client.LLMProviderModel.Query().
		Where(
			llmprovidermodel.IDEQ(strings.TrimSpace(providerID)),
			llmprovidermodel.ModelIDEQ(strings.TrimSpace(modelID)),
		).
		Select(
			llmprovidermodel.FieldWireAPI,
			llmprovidermodel.FieldBaseURL,
			llmprovidermodel.FieldHeadersJSON,
			llmprovidermodel.FieldMaxOutputTokens,
			llmprovidermodel.FieldContextWindow,
			llmprovidermodel.FieldDisplayName,
		).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return llms.ProviderModelConfig{}, false, nil
		}
		return llms.ProviderModelConfig{}, false, fmt.Errorf("query provider model config: %w", err)
	}
	config := llmProviderModelConfigFromEntity(entity)
	return config, true, nil
}

// ListLLMProviderModelConfigs returns every provider-model binding so the
// catalog can be loaded in one pass instead of per connection and model.
func (s *llmStore) ListLLMProviderModelConfigs(ctx context.Context) ([]llms.ProviderModelBinding, error) {
	entities, err := s.client.LLMProviderModel.Query().
		Select(
			llmprovidermodel.FieldID,
			llmprovidermodel.FieldModelID,
			llmprovidermodel.FieldWireAPI,
			llmprovidermodel.FieldBaseURL,
			llmprovidermodel.FieldHeadersJSON,
			llmprovidermodel.FieldMaxOutputTokens,
			llmprovidermodel.FieldContextWindow,
			llmprovidermodel.FieldDisplayName,
		).
		Order(llmprovidermodel.ByID(), llmprovidermodel.ByModelID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query provider model bindings: %w", err)
	}
	var bindings []llms.ProviderModelBinding
	for _, entity := range entities {
		bindings = append(bindings, llms.ProviderModelBinding{
			ProviderID: entity.ID,
			ModelID:    entity.ModelID,
			Config:     llmProviderModelConfigFromEntity(entity),
		})
	}
	return bindings, nil
}

// llmProviderModelConfigFromEntity maps a model-binding row to the effective
// override, normalizing a declared wire api exactly like the old scanner.
func llmProviderModelConfigFromEntity(entity *dao.LLMProviderModel) llms.ProviderModelConfig {
	config := llms.ProviderModelConfig{
		WireAPI:         entity.WireAPI,
		BaseURL:         entity.BaseURL,
		HeadersJSON:     entity.HeadersJSON,
		MaxOutputTokens: entity.MaxOutputTokens,
		ContextWindow:   entity.ContextWindow,
		DisplayName:     entity.DisplayName,
	}
	normalizeBindingWireAPI(&config)
	return config
}

// normalizeBindingWireAPI keeps an unset per-model wire api unset.
// NormalizeWireAPI maps the empty string onto responses, so normalizing
// unconditionally turned "this binding declares no wire api" into "this binding
// declares responses", and the target builder then preferred that over the
// connection's own default_wire_api. A sandbox that publishes
// LLM_API_PROTOCOL=chat_completions is persisted exactly that way, so the
// fabricated responses value silently overrode the published protocol: the
// runtime proxy posted a chat-completions model to /v1/responses and the
// gateway rejected the pair.
func normalizeBindingWireAPI(config *llms.ProviderModelConfig) {
	if config == nil || strings.TrimSpace(config.WireAPI) == "" {
		return
	}
	config.WireAPI = llms.NormalizeWireAPI(config.WireAPI)
}
