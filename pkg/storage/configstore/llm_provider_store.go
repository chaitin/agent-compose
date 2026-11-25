package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/llmcatalogdefault"
	"github.com/chaitin/agent-compose/ent/dao/llmfacadetoken"
	"github.com/chaitin/agent-compose/ent/dao/llmmodel"
	"github.com/chaitin/agent-compose/ent/dao/llmprovider"
	"github.com/chaitin/agent-compose/ent/dao/llmprovidermodel"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

const providerColumns = `id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, auth, headers_json, use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at`

// isDuplicateLLMRow classifies a unique or primary-key violation without a
// dialect field on the store: both backends' constraint errors are recognized
// through the small driver interfaces the dialect package probes.
func isDuplicateLLMRow(err error) bool {
	return dialect.SQLite().IsDuplicateRow(err) || dialect.Postgres().IsDuplicateRow(err)
}

// CreateLLMProvider creates an API-owned upstream provider with its declared
// models, in one transaction. An explicit authentication choice is stored
// independently of the protocol, even when it currently matches the protocol's
// default.
func (s *llmStore) CreateLLMProvider(ctx context.Context, input llms.ProviderReplacement) (llms.Provider, error) {
	input, err := llms.NormalizeProviderReplacement(input)
	if err != nil {
		return llms.Provider{}, err
	}
	if input.APIKey == nil {
		return llms.Provider{}, fmt.Errorf("%w: api_key is required", domain.ErrInvalidArgument)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	auth := providerAuthValue(input.Auth)
	family, header, scheme := managedProviderAuth(input.Protocol, auth)
	now := time.Now().UTC().Unix()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return llms.Provider{}, fmt.Errorf("begin provider create: %w", err)
	}
	// Rollback after commit is harmless; on errors it releases the transaction.
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	// The old statement was INSERT ... ON CONFLICT(id) DO NOTHING RETURNING, so
	// it inserted every column explicitly and reported an existing id as
	// ErrAlreadyExists without touching the stored row.
	created, err := client.LLMProvider.Create().
		SetID(input.ID).
		SetName(input.Name).
		SetProviderType(family).
		SetDefaultWireAPI(input.Protocol).
		SetBaseURL(input.BaseURL).
		SetAPIKey(*input.APIKey).
		SetAuthHeader(header).
		SetAuthScheme(scheme).
		SetAuth(string(auth)).
		SetHeadersJSON(llms.ManagedProviderHeadersJSON(input.Protocol)).
		SetUseGenericResponsesTextParts(0).
		SetWeight(10).
		SetEnabled(BoolToInt(enabled)).
		SetScope(llms.ProviderScopeAPI).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		if isDuplicateLLMRow(err) {
			return llms.Provider{}, fmt.Errorf("%w: provider id already exists", domain.ErrAlreadyExists)
		}
		return llms.Provider{}, fmt.Errorf("create llm provider: %w", err)
	}
	provider := llmProviderFromEntity(created)
	if err := replaceProviderModels(ctx, client, provider.ID, provider.DefaultWireAPI, input.Models); err != nil {
		return llms.Provider{}, err
	}
	if err := tx.Commit(); err != nil {
		return llms.Provider{}, fmt.Errorf("commit provider create: %w", err)
	}
	return provider, nil
}

// GetManagedLLMProvider reads API-owned configuration, including disabled providers.
func (s *llmStore) GetManagedLLMProvider(ctx context.Context, id string) (llms.Provider, error) {
	if err := llms.ValidateManagedProviderID(id); err != nil {
		return llms.Provider{}, err
	}
	entity, err := s.client.LLMProvider.Query().
		Where(llmprovider.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return llms.Provider{}, fmt.Errorf("%w: llm provider not found", domain.ErrNotFound)
		}
		return llms.Provider{}, fmt.Errorf("get llm provider: %w", err)
	}
	provider := llmProviderFromEntity(entity)
	if provider.Scope != llms.ProviderScopeAPI {
		return llms.Provider{}, fmt.Errorf("%w: provider is not API-managed", domain.ErrFailedPrecondition)
	}
	return provider, nil
}

// ListManagedLLMProviders lists API-owned providers in stable ID order.
func (s *llmStore) ListManagedLLMProviders(ctx context.Context) ([]llms.Provider, error) {
	entities, err := s.client.LLMProvider.Query().
		Where(llmprovider.ScopeEQ(llms.ProviderScopeAPI)).
		Order(llmprovider.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list managed llm providers: %w", err)
	}
	providers := make([]llms.Provider, 0, len(entities))
	for _, entity := range entities {
		providers = append(providers, llmProviderFromEntity(entity))
	}
	return providers, nil
}

// UpdateLLMProvider applies explicit fields and preserves omitted values.
// Omitted auth preserves the stored choice across protocol changes. Only an
// explicit empty auth returns the connection to the protocol's default.
func (s *llmStore) UpdateLLMProvider(ctx context.Context, input llms.ProviderReplacement) (llms.Provider, error) {
	input, err := llms.NormalizeProviderUpdate(input)
	if err != nil {
		return llms.Provider{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return llms.Provider{}, fmt.Errorf("begin provider update: %w", err)
	}
	// Rollback after commit is harmless; on errors it releases the transaction.
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	storedEntity, err := client.LLMProvider.Query().
		Where(llmprovider.IDEQ(input.ID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return llms.Provider{}, fmt.Errorf("%w: llm provider not found", domain.ErrNotFound)
		}
		return llms.Provider{}, fmt.Errorf("read llm provider for update: %w", err)
	}
	stored := llmProviderFromEntity(storedEntity)
	if stored.Scope != llms.ProviderScopeAPI {
		return llms.Provider{}, fmt.Errorf("%w: provider is not API-managed", domain.ErrFailedPrecondition)
	}
	presentation := stored.Auth
	protocol := stored.DefaultWireAPI
	if input.Protocol != "" {
		protocol = input.Protocol
	}
	if input.Auth != nil {
		presentation = *input.Auth
	}
	// The family and the protocol headers keep their stored values unless the
	// request replaces the protocol, so an auth-only update cannot rewrite them.
	var family, headersJSON any
	if input.Protocol != "" {
		effectiveFamily, _, _ := managedProviderAuth(input.Protocol, presentation)
		family = effectiveFamily
		headersJSON = llms.ManagedProviderHeadersJSON(input.Protocol)
	}
	_, header, scheme := managedProviderAuth(protocol, presentation)
	// Kept as SQL: every updated value that is omitted from the request keeps
	// the stored row's value through COALESCE/NULLIF of the old row, and the
	// RETURNING result distinguishes the compare-and-swap miss (scope changed
	// under us) from a successful update. ent's update builder computes values
	// from a prior read, which would overwrite a concurrent writer's omitted
	// fields.
	rows, err := client.QueryContext(ctx, `UPDATE llm_provider SET name = COALESCE(NULLIF(?, ''), name), provider_type = COALESCE(?, provider_type), default_wire_api = COALESCE(NULLIF(?, ''), default_wire_api), base_url = COALESCE(NULLIF(?, ''), base_url), api_key = COALESCE(?, api_key), auth_header = ?, auth_scheme = ?, auth = ?, headers_json = COALESCE(?, headers_json), enabled = COALESCE(?, enabled), updated_at = ?
 WHERE id = ? AND scope = ? RETURNING `+providerColumns,
		input.Name, family, input.Protocol, input.BaseURL, input.APIKey, header, scheme, string(presentation), headersJSON, optionalBoolToSQL(input.Enabled), time.Now().UTC().Unix(), input.ID, llms.ProviderScopeAPI)
	if err != nil {
		return llms.Provider{}, fmt.Errorf("update llm provider: %w", err)
	}
	provider, err := scanUpdatedLLMProvider(rows)
	if err != nil {
		return llms.Provider{}, err
	}
	if err := ensureRetainedModelProtocols(ctx, client, provider.ID, provider.DefaultWireAPI, input.Models); err != nil {
		return llms.Provider{}, err
	}
	if err := replaceProviderModels(ctx, client, provider.ID, provider.DefaultWireAPI, input.Models); err != nil {
		return llms.Provider{}, err
	}
	if !provider.Enabled {
		// A disabled connection cannot serve the default, and resolution already
		// treats its reference as absent. Remove the row so GetDefaultModel does
		// not advertise a default no run receives.
		if err := clearDefaultReferenceForProvider(ctx, client, provider.ID); err != nil {
			return llms.Provider{}, err
		}
		// The model identity a literal default created is kept alive by that
		// reference alone, so clearing it can leave the row unreferenced. The
		// sweep inside replaceProviderModels cannot cover this: an update that
		// carries no model set skips it, and it ran before the clear. A leftover
		// enabled api-scope identity would keep claiming its model id, and the
		// environment bootstrap yields its whole projection to a foreign owner.
		if err := sweepUnreferencedAPIModels(ctx, client); err != nil {
			return llms.Provider{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return llms.Provider{}, fmt.Errorf("commit provider update: %w", err)
	}
	return provider, nil
}

// scanUpdatedLLMProvider reads the row returned by the kept conditional UPDATE
// and closes the cursor before the caller issues further queries (SQLite runs
// with one connection, so an open cursor would block them).
func scanUpdatedLLMProvider(rows *sql.Rows) (llms.Provider, error) {
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if queryErr := rows.Err(); queryErr != nil {
			return llms.Provider{}, fmt.Errorf("update llm provider: %w", queryErr)
		}
		// The row was readable and API-managed moments ago, so it changed under us.
		return llms.Provider{}, fmt.Errorf("%w: provider changed during update", domain.ErrConflict)
	}
	provider, err := llms.ScanProvider(rows.Scan)
	if err != nil {
		return llms.Provider{}, fmt.Errorf("update llm provider: %w", err)
	}
	return provider, nil
}

// ensureRetainedModelProtocols checks the bindings an update keeps against the
// connection's new protocol. A binding's explicit protocol outranks the
// connection protocol when a target is built, so a protocol-family change that
// retains a binding from the old family would resolve requests to an endpoint and
// protocol that disagree — a combination this package rejects when a client
// states it in a model spec. A request that carries models states the new intent
// and is validated against that set instead, so only retained bindings are
// checked here.
func ensureRetainedModelProtocols(ctx context.Context, client *dao.Client, providerID, providerProtocol string, replacement *[]llms.ModelSpec) error {
	if replacement != nil {
		return nil
	}
	entities, err := client.LLMProviderModel.Query().
		Where(llmprovidermodel.IDEQ(providerID)).
		Select(llmprovidermodel.FieldModelID, llmprovidermodel.FieldWireAPI).
		Order(llmprovidermodel.ByModelID()).
		All(ctx)
	if err != nil {
		return fmt.Errorf("read retained provider %q models: %w", providerID, err)
	}
	for _, entity := range entities {
		if err := llms.ValidateModelSpecProtocol(providerProtocol, entity.WireAPI); err != nil {
			return fmt.Errorf("retained model %q declared as %q does not fit the new connection protocol %q, resend the model set to restate it: %w",
				entity.ModelID, entity.WireAPI, providerProtocol, err)
		}
	}
	return nil
}

// replaceProviderModels writes a connection's declared model set. A nil set
// leaves the stored set alone; a non-nil set replaces it, so an empty set clears
// every binding. Model identity rows are shared across connections by model ID,
// so an existing row another source owns keeps its scope and metadata and only
// the binding is written.
func replaceProviderModels(ctx context.Context, client *dao.Client, providerID, providerProtocol string, models *[]llms.ModelSpec) error {
	if models == nil {
		return nil
	}
	if _, err := client.LLMProviderModel.Delete().
		Where(llmprovidermodel.IDEQ(providerID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("replace provider %q models: %w", providerID, err)
	}
	now := time.Now().UTC().Unix()
	for _, model := range *models {
		if err := llms.ValidateModelSpecProtocol(providerProtocol, model.Protocol); err != nil {
			return err
		}
		// An unset model protocol stays unset, which makes the target builder
		// inherit the connection protocol instead of fabricating responses.
		protocol := ""
		if model.Protocol != "" {
			protocol = llms.NormalizeWireAPI(model.Protocol)
		}
		headersJSON, err := encodeCatalogHeaders(model.Headers)
		if err != nil {
			return fmt.Errorf("encode provider %q model %q headers: %w", providerID, model.ID, err)
		}
		if err := client.LLMModel.Create().
			SetID(model.ID).
			SetName(firstNonEmptyString(model.Name, model.ID)).
			SetDescription("").
			SetDefaultModel(0).
			SetEnabled(1).
			SetScope(llms.ProviderScopeAPI).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			OnConflict(entsql.ConflictColumns(llmmodel.FieldID), entsql.ResolveWithIgnore()).
			Exec(ctx); err != nil {
			return fmt.Errorf("record provider %q model %q identity: %w", providerID, model.ID, err)
		}
		if _, err := client.LLMProviderModel.Create().
			SetID(providerID).
			SetModelID(model.ID).
			SetWireAPI(protocol).
			SetWeight(10).
			SetBaseURL(model.BaseURL).
			SetHeadersJSON(headersJSON).
			SetMaxOutputTokens(model.MaxOutputTokens).
			SetContextWindow(model.ContextWindow).
			SetDisplayName(model.Name).
			Save(ctx); err != nil {
			return fmt.Errorf("bind provider %q model %q: %w", providerID, model.ID, err)
		}
	}
	return sweepUnreferencedAPIModels(ctx, client)
}

// sweepUnreferencedAPIModels removes API-owned model identities that no
// connection binds and no default names.
//
// llm_model declares a model identity; llm_provider_model is the binding that
// makes it servable. Rewriting a connection's models removes its bindings and
// deleting a connection removes the connection, but neither removed the identity
// row it had created, so a model an operator had dropped kept claiming its id.
// The environment bootstrap treats a foreign-scope row as an owner and yields its
// whole projection, so such a leftover could silently disable LLM_MODEL for every
// later daemon start.
//
// Only API-scope rows are swept. A catalog row belongs to the models.json
// projection, which releases what it no longer declares, and an environment row
// belongs to the environment projection.
func sweepUnreferencedAPIModels(ctx context.Context, client *dao.Client) error {
	// Collect the referenced model ids in the same transaction as the delete so
	// the sweep sees the fully projected reference state.
	bindings, err := client.LLMProviderModel.Query().
		Select(llmprovidermodel.FieldModelID).
		All(ctx)
	if err != nil {
		return fmt.Errorf("read bound model identities: %w", err)
	}
	defaults, err := client.LLMCatalogDefault.Query().
		Select(llmcatalogdefault.FieldModelID).
		All(ctx)
	if err != nil {
		return fmt.Errorf("read default model identities: %w", err)
	}
	referenced := make([]string, 0, len(bindings)+len(defaults))
	seen := make(map[string]struct{}, len(bindings)+len(defaults))
	for _, binding := range bindings {
		if _, ok := seen[binding.ModelID]; ok {
			continue
		}
		seen[binding.ModelID] = struct{}{}
		referenced = append(referenced, binding.ModelID)
	}
	for _, entity := range defaults {
		if _, ok := seen[entity.ModelID]; ok {
			continue
		}
		seen[entity.ModelID] = struct{}{}
		referenced = append(referenced, entity.ModelID)
	}
	deleteQuery := client.LLMModel.Delete().Where(llmmodel.ScopeEQ(llms.ProviderScopeAPI))
	if len(referenced) > 0 {
		deleteQuery = deleteQuery.Where(llmmodel.IDNotIn(referenced...))
	}
	if _, err := deleteQuery.Exec(ctx); err != nil {
		return fmt.Errorf("sweep unreferenced api model identities: %w", err)
	}
	return nil
}

// DeleteLLMProvider removes an API provider and its tokens atomically. Removing
// tokens ensures reusing an ID cannot revive credentials issued for the old provider.
func (s *llmStore) DeleteLLMProvider(ctx context.Context, id string) error {
	if err := llms.ValidateManagedProviderID(id); err != nil {
		return err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin provider deletion: %w", err)
	}
	// Rollback after commit is harmless; on errors it releases the transaction.
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	entity, err := client.LLMProvider.Query().
		Where(llmprovider.IDEQ(id)).
		Select(llmprovider.FieldScope).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return fmt.Errorf("%w: llm provider not found", domain.ErrNotFound)
		}
		return fmt.Errorf("read provider ownership: %w", err)
	}
	if entity.Scope != llms.ProviderScopeAPI {
		return fmt.Errorf("%w: provider is not API-managed", domain.ErrFailedPrecondition)
	}
	if _, err := client.LLMFacadeToken.Delete().
		Where(llmfacadetoken.ProviderIDEQ(id)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete llm provider configuration: %w", err)
	}
	if _, err := client.LLMProviderModel.Delete().
		Where(llmprovidermodel.IDEQ(id)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete llm provider configuration: %w", err)
	}
	if _, err := client.LLMProvider.Delete().
		Where(llmprovider.IDEQ(id)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete llm provider configuration: %w", err)
	}
	// The connection is gone, so a default naming it can never be served; leaving
	// the row would make GetDefaultModel report a default no run receives.
	if err := clearDefaultReferenceForProvider(ctx, client, id); err != nil {
		return err
	}
	// Its model identities are gone too, so nothing else may claim they are still
	// declared.
	if err := sweepUnreferencedAPIModels(ctx, client); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider deletion: %w", err)
	}
	return nil
}

// managedProviderAuth derives the provider family from the protocol and the
// credential presentation from the connection's explicit auth, falling back to
// the protocol convention. A gateway may speak the Anthropic Messages protocol
// while authenticating with a bearer token, so the protocol alone cannot decide
// the header.
func managedProviderAuth(protocol string, auth llms.ProviderAuth) (family, header, scheme string) {
	family = llms.ProviderFamilyOpenAI
	if protocol == llms.APIProtocolMessages {
		family = llms.ProviderFamilyAnthropic
	}
	header, scheme = llms.ResolveProviderAuth(protocol, auth)
	return family, header, scheme
}

// providerAuthValue reports the presentation a replacement names, or the empty
// presentation when the request left it unspecified.
func providerAuthValue(auth *llms.ProviderAuth) llms.ProviderAuth {
	if auth == nil {
		return ""
	}
	return *auth
}

func optionalBoolToSQL(value *bool) any {
	if value == nil {
		return nil
	}
	return BoolToInt(*value)
}
