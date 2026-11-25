package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/llmfacadetoken"
	"github.com/chaitin/agent-compose/ent/dao/llmmodel"
	"github.com/chaitin/agent-compose/ent/dao/llmprovider"
	"github.com/chaitin/agent-compose/ent/dao/llmprovidermodel"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

// llmStore owns LLM provider/model configuration and facade tokens.
type llmStore struct {
	db     *sql.DB
	client *dao.Client
}

func (s *llmStore) HasLLMProviders(ctx context.Context) (bool, error) {
	count, err := s.client.LLMProvider.Query().Count(ctx)
	if err != nil {
		return false, fmt.Errorf("count llm providers: %w", err)
	}
	return count > 0, nil
}

func (s *llmStore) HasLLMProvider(ctx context.Context, providerID string) (bool, error) {
	exists, err := s.client.LLMProvider.Query().
		Where(llmprovider.IDEQ(strings.TrimSpace(providerID))).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("query llm provider %q: %w", providerID, err)
	}
	return exists, nil
}

// UpsertDefaultLLMConfig materializes the daemon environment's LLM connection. A
// projection that yields to another configuration source releases the connection
// it materialized first, so yielding leaves nothing behind: the id it would have
// claimed keeps its existing owner, and no row stays enabled beside it to serve
// the same model as a peer in connection selection.
func (s *llmStore) UpsertDefaultLLMConfig(ctx context.Context, provider llms.Provider, model llms.Model) error {
	now := time.Now().UTC().Unix()
	var ok bool
	provider, model, ok = llms.NormalizeDefaultConfig(provider, model)
	if !ok {
		return nil
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin llm default config tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	// The environment is a bootstrap fallback, not an owner of last resort: a
	// connection or model another source declared (models.json, an API-managed
	// connection) keeps its id and its metadata. Without this guard the
	// projection rewrote the scope of a same-id catalog row and silently
	// replaced the operator's configuration.
	if conflict, err := envProjectionConflict(ctx, client, provider.ID, model.ID); err != nil {
		return err
	} else if conflict != nil {
		// The projection yields, so the connection this bootstrap materialized in
		// an earlier run must stop serving. Left enabled, it would be a peer of the
		// declaring source for the conflicting model, and connection selection would
		// choose between the two at random. Only a row this projection owns —
		// env_default, or the legacy system scope it wrote before that value
		// existed — is released; an id another source owns is not the bootstrap's
		// to release.
		if err := releaseEnvProjectionConnection(ctx, client, provider.ID, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit released llm default config: %w", err)
		}
		return conflict
	}
	// The bootstrap presentation can come from the environment (for example an
	// Anthropic bearer token), so record it with the same intent rule the
	// migration used: only a presentation that differs from the protocol
	// convention is an override worth preserving.
	authIntent := llms.InferLegacyProviderAuth(provider.DefaultWireAPI, provider.AuthHeader, provider.AuthScheme)
	if err := upsertLLMProvider(ctx, client, provider, authIntent, now); err != nil {
		return err
	}
	if model.DefaultModel {
		if _, err := client.LLMModel.Update().
			Where(llmmodel.DefaultModelNEQ(0)).
			SetDefaultModel(0).
			Save(ctx); err != nil {
			return fmt.Errorf("reset default llm models: %w", err)
		}
	}
	// The old INSERT's conflict update set covered name, description,
	// default_model, enabled, scope, and updated_at: every column except the id
	// and created_at, so created_at keeps its stored value.
	description := model.Description
	defaultModel := BoolToInt(model.DefaultModel)
	enabled := BoolToInt(model.Enabled)
	if err := client.LLMModel.Create().
		SetID(model.ID).
		SetName(model.Name).
		SetDescription(description).
		SetDefaultModel(defaultModel).
		SetEnabled(enabled).
		SetScope(model.Scope).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		OnConflict(entsql.ConflictColumns(llmmodel.FieldID)).
		Update(func(u *dao.LLMModelUpsert) {
			u.SetName(model.Name)
			u.SetDescription(description)
			u.SetDefaultModel(defaultModel)
			u.SetEnabled(enabled)
			u.SetScope(model.Scope)
			u.SetUpdatedAt(now)
		}).
		Exec(ctx); err != nil {
		return fmt.Errorf("insert default llm model: %w", err)
	}
	if err := client.LLMProviderModel.Create().
		SetID(provider.ID).
		SetModelID(model.ID).
		SetWireAPI("").
		SetWeight(10).
		OnConflict(
			entsql.ConflictColumns(llmprovidermodel.FieldID, llmprovidermodel.FieldModelID),
			entsql.ResolveWithIgnore(),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("insert default llm provider model: %w", err)
	}
	return tx.Commit()
}

// UpsertDeclaredConnection persists a connection derived from an agent's own
// environment declaration. It is run-scoped: its id is in a reserved namespace,
// it is never part of the daemon's shared catalog, and it is deleted when the
// sandbox's LLM state is revoked.
//
// No llm_model row is written. The model a declared run uses comes from the
// declaration and is opaque to the daemon, so a session connection must not add
// a model to the shared catalog.
func (s *llmStore) UpsertDeclaredConnection(ctx context.Context, provider llms.Provider) error {
	if !llms.IsDeclaredConnectionID(provider.ID) {
		return fmt.Errorf("connection %q is not in the reserved declared namespace", provider.ID)
	}
	provider = llms.NormalizeDeclaredConnection(provider)
	if strings.TrimSpace(provider.APIKey) == "" || strings.TrimSpace(provider.BaseURL) == "" {
		return fmt.Errorf("declared connection %q requires a credential and a base URL", provider.ID)
	}
	now := time.Now().UTC().Unix()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin declared llm connection tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	authIntent := llms.InferLegacyProviderAuth(provider.DefaultWireAPI, provider.AuthHeader, provider.AuthScheme)
	if err := upsertLLMProvider(ctx, client, provider, authIntent, now); err != nil {
		return err
	}
	return tx.Commit()
}

// releaseEnvProjectionConnection disables a connection the environment bootstrap
// materialized and gives its id back, so a later projection can declare it again.
//
// The released scope is the one the stored row carries, and only the scopes the
// bootstrap itself writes qualify (see EnvProjectionOwnsScope): the env_default
// this projection writes today, the legacy system default older versions wrote,
// and the unset scope of a row an earlier projection already released. Reading
// the scope first also keeps the update a compare-and-swap: a row another writer
// re-scoped in the meantime no longer matches, so its configuration stays with
// the source that now owns it.
func releaseEnvProjectionConnection(ctx context.Context, client *dao.Client, providerID string, now int64) error {
	scope, err := storedProviderScope(ctx, client, providerID)
	if err != nil {
		return err
	}
	if !llms.EnvProjectionOwnsScope(scope) {
		return nil
	}
	if _, err := client.LLMProvider.Update().
		Where(llmprovider.IDEQ(providerID), llmprovider.ScopeEQ(scope)).
		SetEnabled(0).
		SetScope("").
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		return fmt.Errorf("release environment connection %q: %w", providerID, err)
	}
	return nil
}

func upsertLLMProvider(ctx context.Context, client *dao.Client, provider llms.Provider, auth llms.ProviderAuth, now int64) error {
	authValue := string(auth)
	genericResponsesTextParts := BoolToInt(provider.UseGenericResponsesTextParts)
	// The old INSERT's conflict update set covered every non-key column except
	// created_at, which keeps the value stored when the row first appeared.
	if err := client.LLMProvider.Create().
		SetID(provider.ID).
		SetName(provider.Name).
		SetProviderType(provider.ProviderType).
		SetDefaultWireAPI(provider.DefaultWireAPI).
		SetBaseURL(provider.BaseURL).
		SetAPIKey(provider.APIKey).
		SetAuthHeader(provider.AuthHeader).
		SetAuthScheme(provider.AuthScheme).
		SetAuth(authValue).
		SetHeadersJSON(provider.HeadersJSON).
		SetUseGenericResponsesTextParts(genericResponsesTextParts).
		SetWeight(provider.Weight).
		SetEnabled(1).
		SetScope(provider.Scope).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		OnConflict(entsql.ConflictColumns(llmprovider.FieldID)).
		Update(func(u *dao.LLMProviderUpsert) {
			u.SetName(provider.Name)
			u.SetProviderType(provider.ProviderType)
			u.SetDefaultWireAPI(provider.DefaultWireAPI)
			u.SetBaseURL(provider.BaseURL)
			u.SetAPIKey(provider.APIKey)
			u.SetAuthHeader(provider.AuthHeader)
			u.SetAuthScheme(provider.AuthScheme)
			u.SetAuth(authValue)
			u.SetHeadersJSON(provider.HeadersJSON)
			u.SetUseGenericResponsesTextParts(genericResponsesTextParts)
			u.SetWeight(provider.Weight)
			u.SetEnabled(1)
			u.SetScope(provider.Scope)
			u.SetUpdatedAt(now)
		}).
		Exec(ctx); err != nil {
		return fmt.Errorf("upsert llm provider %q: %w", provider.ID, err)
	}
	return nil
}

func (s *llmStore) ListEnabledLLMProviders(ctx context.Context) ([]llms.Provider, error) {
	entities, err := s.client.LLMProvider.Query().
		Where(llmprovider.EnabledNEQ(0)).
		Order(llmprovider.ByWeight(), llmprovider.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query llm providers: %w", err)
	}
	var providers []llms.Provider
	for _, entity := range entities {
		providers = append(providers, llmProviderFromEntity(entity))
	}
	return providers, nil
}

func (s *llmStore) ListEnabledLLMModels(ctx context.Context) ([]llms.Model, error) {
	entities, err := s.client.LLMModel.Query().
		Where(llmmodel.EnabledNEQ(0)).
		Order(llmmodel.ByDefaultModel(entsql.OrderDesc()), llmmodel.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query llm models: %w", err)
	}
	var models []llms.Model
	for _, entity := range entities {
		models = append(models, llmModelFromEntity(entity))
	}
	return models, nil
}

func (s *llmStore) LLMProviderModelWireAPI(ctx context.Context, providerID, modelID string) (string, bool, error) {
	entity, err := s.client.LLMProviderModel.Query().
		Where(
			llmprovidermodel.IDEQ(strings.TrimSpace(providerID)),
			llmprovidermodel.ModelIDEQ(strings.TrimSpace(modelID)),
		).
		Select(llmprovidermodel.FieldWireAPI).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query llm provider model: %w", err)
	}
	wireAPI := strings.TrimSpace(entity.WireAPI)
	if wireAPI == "" {
		return "", true, nil
	}
	return llms.NormalizeWireAPI(wireAPI), true, nil
}

func (s *llmStore) SaveLLMFacadeToken(ctx context.Context, token llms.FacadeToken) error {
	if strings.TrimSpace(token.TokenHash) == "" || strings.TrimSpace(token.SandboxID) == "" {
		return fmt.Errorf("llm facade token hash and sandbox id are required")
	}
	if token.IssuedAt.IsZero() {
		token.IssuedAt = time.Now().UTC()
	}
	revokedAt := int64(0)
	if !token.RevokedAt.IsZero() {
		revokedAt = token.RevokedAt.Unix()
	}
	expiresAt := int64(0)
	if !token.ExpiresAt.IsZero() {
		expiresAt = token.ExpiresAt.Unix()
	}
	// The old conflict update set covered every non-key column, so the generated
	// new values reproduce it exactly (token_hash stays the stored key).
	if err := s.client.LLMFacadeToken.Create().
		SetID(token.TokenHash).
		SetSandboxID(token.SandboxID).
		SetTokenFingerprint(token.TokenFingerprint).
		SetModel(token.Model).
		SetProviderID(token.ProviderID).
		SetWireAPI(token.WireAPI).
		SetGuestModel(token.GuestModel).
		SetSource(token.Source).
		SetRunID(token.RunID).
		SetIssuedAt(token.IssuedAt.Unix()).
		SetExpiresAt(expiresAt).
		SetRevokedAt(revokedAt).
		OnConflict(entsql.ConflictColumns(llmfacadetoken.FieldID), entsql.ResolveWithNewValues()).
		Exec(ctx); err != nil {
		return fmt.Errorf("save llm facade token: %w", err)
	}
	return nil
}

// DeleteLLMFacadeToken removes a single facade token by its raw value. It is used
// to retire a per-run agent token as soon as that run completes, so live tokens
// never accumulate over the lifetime of a long-running session.
func (s *llmStore) DeleteLLMFacadeToken(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	hash, _ := llms.HashFacadeToken(rawToken)
	return s.DeleteLLMFacadeTokenHash(ctx, hash)
}

// DeleteLLMFacadeTokenHash removes one facade token by its already-computed
// hash. Command-time facade setup tracks hashes at the persistence boundary so
// it can clean up every token created by one command, including tokens issued
// before a later provider configuration step fails.
func (s *llmStore) DeleteLLMFacadeTokenHash(ctx context.Context, tokenHash string) error {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil
	}
	if _, err := s.client.LLMFacadeToken.Delete().
		Where(llmfacadetoken.IDEQ(tokenHash)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete llm facade token: %w", err)
	}
	return nil
}

// DeleteLLMFacadeTokensForRun removes the facade tokens issued to one run
// from one source. A detached agent run keeps its per-run token across a
// daemon restart, when the raw token is no longer in memory, so the daemon
// that finishes the run retires it by run id instead.
func (s *llmStore) DeleteLLMFacadeTokensForRun(ctx context.Context, runID, source string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	if _, err := s.client.LLMFacadeToken.Delete().
		Where(
			llmfacadetoken.RunIDEQ(runID),
			llmfacadetoken.SourceEQ(strings.TrimSpace(source)),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete llm facade tokens for run: %w", err)
	}
	return nil
}

func (s *llmStore) GetLLMFacadeToken(ctx context.Context, rawToken string) (llms.FacadeToken, error) {
	hash, fingerprint := llms.HashFacadeToken(rawToken)
	entity, err := s.client.LLMFacadeToken.Query().
		Where(llmfacadetoken.IDEQ(hash)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return llms.FacadeToken{}, domain.ResourceError(domain.ErrNotFound, "llm facade token", fingerprint, fmt.Sprintf("llm facade token %s not found", fingerprint), err)
		}
		return llms.FacadeToken{}, err
	}
	return llmFacadeTokenFromEntity(entity), nil
}

// llmFacadeTokenRetention is how long a revoked facade token row is kept before
// it is physically pruned. The grace window keeps recently-revoked tokens around
// for debugging while bounding table growth from completed sandboxes.
const llmFacadeTokenRetention = time.Hour

const LLMFacadeTokenRetention = llmFacadeTokenRetention

// RevokeLLMFacadeTokensForSandbox releases the LLM state one sandbox owns: its
// facade tokens and any connection the daemon derived from the sandbox's own
// environment declaration. A declared connection holds a real upstream
// credential, so it is deleted rather than left for the next run to overwrite.
func (s *llmStore) RevokeLLMFacadeTokensForSandbox(ctx context.Context, sandboxID string) error {
	sandboxID = strings.TrimSpace(sandboxID)
	now := time.Now().UTC()
	if _, err := s.client.LLMFacadeToken.Update().
		Where(
			llmfacadetoken.SandboxIDEQ(sandboxID),
			llmfacadetoken.RevokedAtEQ(0),
		).
		SetRevokedAt(now.Unix()).
		Save(ctx); err != nil {
		return fmt.Errorf("revoke llm facade tokens for sandbox: %w", err)
	}
	// The old statement matched "declared:<sandbox>:%" with an explicit escape
	// character; the prefix predicate escapes the same metacharacters and keeps
	// the same case-insensitive LIKE behaviour on SQLite.
	if _, err := s.client.LLMProvider.Delete().
		Where(entsql.FieldHasPrefix(llmprovider.FieldID, declaredConnectionIDPrefix(sandboxID))).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete declared llm connections for sandbox: %w", err)
	}
	// Opportunistically prune long-dead rows (revoked beyond the retention grace,
	// or expired) so the table stays bounded across sandboxes. Both states already
	// fail closed at the handler, so deleting them changes nothing observable.
	cutoff := now.Add(-llmFacadeTokenRetention).Unix()
	if _, err := s.client.LLMFacadeToken.Delete().
		Where(llmfacadetoken.Or(
			llmfacadetoken.And(
				llmfacadetoken.RevokedAtNEQ(0),
				llmfacadetoken.RevokedAtLT(cutoff),
			),
			llmfacadetoken.And(
				llmfacadetoken.ExpiresAtNEQ(0),
				llmfacadetoken.ExpiresAtLT(now.Unix()),
			),
		)).
		Exec(ctx); err != nil {
		return fmt.Errorf("prune llm facade tokens: %w", err)
	}
	return nil
}

// declaredConnectionIDPrefix is the literal id prefix every declared connection
// of one sandbox shares. The stored ids continue with ":" and a run-scoped
// suffix, so the prefix matches exactly the set the old LIKE pattern did.
func declaredConnectionIDPrefix(sandboxID string) string {
	return llms.DeclaredConnectionPrefix + sandboxID + ":"
}

// llmProviderFromEntity maps a stored connection row to the domain provider. It
// reproduces llms.ScanProvider's normalization and timestamp parsing.
func llmProviderFromEntity(entity *dao.LLMProvider) llms.Provider {
	return llms.Provider{
		ID:                           entity.ID,
		Name:                         entity.Name,
		ProviderType:                 llms.NormalizeProviderType(entity.ProviderType),
		DefaultWireAPI:               llms.NormalizeWireAPI(entity.DefaultWireAPI),
		BaseURL:                      entity.BaseURL,
		APIKey:                       entity.APIKey,
		AuthHeader:                   entity.AuthHeader,
		AuthScheme:                   entity.AuthScheme,
		HeadersJSON:                  entity.HeadersJSON,
		UseGenericResponsesTextParts: entity.UseGenericResponsesTextParts != 0,
		Weight:                       entity.Weight,
		Enabled:                      entity.Enabled != 0,
		Scope:                        entity.Scope,
		CreatedAt:                    storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:                    storedtime.ParseStoredTime(entity.UpdatedAt),
		Auth:                         llms.ProviderAuth(strings.TrimSpace(entity.Auth)),
	}
}

// llmModelFromEntity maps a stored model row to the domain model. It reproduces
// llms.ScanModel's normalization and timestamp parsing.
func llmModelFromEntity(entity *dao.LLMModel) llms.Model {
	return llms.Model{
		ID:           entity.ID,
		Name:         entity.Name,
		Description:  entity.Description,
		DefaultModel: entity.DefaultModel != 0,
		Enabled:      entity.Enabled != 0,
		Scope:        entity.Scope,
		CreatedAt:    storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:    storedtime.ParseStoredTime(entity.UpdatedAt),
	}
}

// llmFacadeTokenFromEntity maps a stored facade token row to the domain token.
// It reproduces llms.ScanFacadeToken's timestamp parsing.
func llmFacadeTokenFromEntity(entity *dao.LLMFacadeToken) llms.FacadeToken {
	return llms.FacadeToken{
		SandboxID:        entity.SandboxID,
		TokenHash:        entity.ID,
		TokenFingerprint: entity.TokenFingerprint,
		Model:            entity.Model,
		ProviderID:       entity.ProviderID,
		WireAPI:          entity.WireAPI,
		GuestModel:       entity.GuestModel,
		Source:           entity.Source,
		RunID:            entity.RunID,
		IssuedAt:         storedtime.ParseStoredTime(entity.IssuedAt),
		ExpiresAt:        storedtime.ParseStoredTime(entity.ExpiresAt),
		RevokedAt:        storedtime.ParseStoredTime(entity.RevokedAt),
	}
}
