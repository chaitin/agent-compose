package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/storage/configstore"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func (s *ConfigStore) ensureLLMSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS llm_provider (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider_type TEXT NOT NULL DEFAULT 'openai_compatible',
			default_wire_api TEXT NOT NULL DEFAULT 'responses',
			base_url TEXT NOT NULL,
			api_key TEXT NOT NULL DEFAULT '',
			auth_header TEXT NOT NULL DEFAULT 'Authorization',
			auth_scheme TEXT NOT NULL DEFAULT 'Bearer',
			headers_json TEXT NOT NULL DEFAULT '{}',
			weight INTEGER NOT NULL DEFAULT 10,
			enabled INTEGER NOT NULL DEFAULT 1,
			scope TEXT NOT NULL DEFAULT 'system',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS llm_model (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			default_model INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			scope TEXT NOT NULL DEFAULT 'system',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS llm_provider_model (
			provider_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			wire_api TEXT NOT NULL DEFAULT '',
			weight INTEGER NOT NULL DEFAULT 10,
			PRIMARY KEY(provider_id, model_id)
		);`,
		`CREATE TABLE IF NOT EXISTS llm_facade_token (
			token_hash TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			token_fingerprint TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL DEFAULT '',
			wire_api TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			run_id TEXT NOT NULL DEFAULT '',
			issued_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			revoked_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_llm_facade_token_session ON llm_facade_token(session_id, revoked_at, expires_at);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create llm schema: %w", err)
		}
	}
	if err := ensureColumn(ctx, s.db, "llm_provider", "use_generic_responses_text_parts", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("ensure llm provider generic responses text parts column: %w", err)
	}
	return nil
}

func (s *ConfigStore) HasLLMProviders(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM llm_provider`).Scan(&count); err != nil {
		return false, fmt.Errorf("count llm providers: %w", err)
	}
	return count > 0, nil
}

func (s *ConfigStore) UpsertDefaultLLMConfig(ctx context.Context, provider llms.Provider, model llms.Model) error {
	now := time.Now().UTC().Unix()
	var ok bool
	provider, model, ok = llms.NormalizeDefaultConfig(provider, model)
	if !ok {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin llm default config tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider(id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, provider_type = excluded.provider_type, default_wire_api = excluded.default_wire_api, base_url = excluded.base_url, api_key = excluded.api_key, auth_header = excluded.auth_header, auth_scheme = excluded.auth_scheme, headers_json = excluded.headers_json, use_generic_responses_text_parts = excluded.use_generic_responses_text_parts, weight = excluded.weight, enabled = excluded.enabled, scope = excluded.scope, updated_at = excluded.updated_at`, provider.ID, provider.Name, provider.ProviderType, provider.DefaultWireAPI, provider.BaseURL, provider.APIKey, provider.AuthHeader, provider.AuthScheme, provider.HeadersJSON, configstore.BoolToInt(provider.UseGenericResponsesTextParts), provider.Weight, provider.Scope, now, now); err != nil {
		return fmt.Errorf("insert default llm provider: %w", err)
	}
	if model.DefaultModel {
		if _, err := tx.ExecContext(ctx, `UPDATE llm_model SET default_model = 0 WHERE default_model != 0`); err != nil {
			return fmt.Errorf("reset default llm models: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, default_model = excluded.default_model, enabled = excluded.enabled, scope = excluded.scope, updated_at = excluded.updated_at`, model.ID, model.Name, model.Description, configstore.BoolToInt(model.DefaultModel), configstore.BoolToInt(model.Enabled), model.Scope, now, now); err != nil {
		return fmt.Errorf("insert default llm model: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider_model(provider_id, model_id, wire_api, weight)
		VALUES(?, ?, '', 10)
		ON CONFLICT(provider_id, model_id) DO NOTHING`, provider.ID, model.ID); err != nil {
		return fmt.Errorf("insert default llm provider model: %w", err)
	}
	return tx.Commit()
}

func (s *ConfigStore) ListEnabledLLMProviders(ctx context.Context) ([]llms.Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at FROM llm_provider WHERE enabled != 0 ORDER BY weight ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query llm providers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var providers []llms.Provider
	for rows.Next() {
		item, err := llms.ScanProvider(rows.Scan)
		if err != nil {
			return nil, err
		}
		providers = append(providers, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm providers: %w", err)
	}
	return providers, nil
}

func (s *ConfigStore) ListEnabledLLMModels(ctx context.Context) ([]llms.Model, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, default_model, enabled, scope, created_at, updated_at FROM llm_model WHERE enabled != 0 ORDER BY default_model DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query llm models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var models []llms.Model
	for rows.Next() {
		item, err := llms.ScanModel(rows.Scan)
		if err != nil {
			return nil, err
		}
		models = append(models, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm models: %w", err)
	}
	return models, nil
}

func (s *ConfigStore) LLMProviderModelWireAPI(ctx context.Context, providerID, modelID string) (string, bool, error) {
	var wireAPI string
	err := s.db.QueryRowContext(ctx, `SELECT wire_api FROM llm_provider_model WHERE provider_id = ? AND model_id = ?`, strings.TrimSpace(providerID), strings.TrimSpace(modelID)).Scan(&wireAPI)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("query llm provider model: %w", err)
	}
	wireAPI = strings.TrimSpace(wireAPI)
	if wireAPI == "" {
		return "", true, nil
	}
	return llms.NormalizeWireAPI(wireAPI), true, nil
}

func (s *ConfigStore) SaveLLMFacadeToken(ctx context.Context, token llms.FacadeToken) error {
	if strings.TrimSpace(token.TokenHash) == "" || strings.TrimSpace(token.SessionID) == "" {
		return fmt.Errorf("llm facade token hash and session id are required")
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO llm_facade_token(token_hash, session_id, token_fingerprint, model, provider_id, wire_api, source, run_id, issued_at, expires_at, revoked_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET session_id = excluded.session_id, token_fingerprint = excluded.token_fingerprint, model = excluded.model, provider_id = excluded.provider_id, wire_api = excluded.wire_api, source = excluded.source, run_id = excluded.run_id, issued_at = excluded.issued_at, expires_at = excluded.expires_at, revoked_at = excluded.revoked_at`,
		token.TokenHash, token.SessionID, token.TokenFingerprint, token.Model, token.ProviderID, token.WireAPI, token.Source, token.RunID, token.IssuedAt.Unix(), expiresAt, revokedAt)
	if err != nil {
		return fmt.Errorf("save llm facade token: %w", err)
	}
	return nil
}

// DeleteLLMFacadeToken removes a single facade token by its raw value. It is used
// to retire a per-run agent token as soon as that run completes, so live tokens
// never accumulate over the lifetime of a long-running session.
func (s *ConfigStore) DeleteLLMFacadeToken(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	hash, _ := llms.HashFacadeToken(rawToken)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM llm_facade_token WHERE token_hash = ?`, hash); err != nil {
		return fmt.Errorf("delete llm facade token: %w", err)
	}
	return nil
}

func (s *ConfigStore) GetLLMFacadeToken(ctx context.Context, rawToken string) (llms.FacadeToken, error) {
	hash, fingerprint := llms.HashFacadeToken(rawToken)
	row := s.db.QueryRowContext(ctx, `SELECT session_id, token_hash, token_fingerprint, model, provider_id, wire_api, source, run_id, issued_at, expires_at, revoked_at FROM llm_facade_token WHERE token_hash = ?`, hash)
	token, err := llms.ScanFacadeToken(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return llms.FacadeToken{}, resourceError(ErrNotFound, "llm facade token", fingerprint, fmt.Sprintf("llm facade token %s not found", fingerprint), err)
		}
		return llms.FacadeToken{}, err
	}
	return token, nil
}

// llmFacadeTokenRetention is how long a revoked facade token row is kept before
// it is physically pruned. The grace window keeps recently-revoked tokens around
// for debugging while bounding table growth from completed sessions.
const llmFacadeTokenRetention = time.Hour

func (s *ConfigStore) RevokeLLMFacadeTokensForSession(ctx context.Context, sessionID string) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE llm_facade_token SET revoked_at = ? WHERE session_id = ? AND revoked_at = 0`, now.Unix(), strings.TrimSpace(sessionID)); err != nil {
		return fmt.Errorf("revoke llm facade tokens for session: %w", err)
	}
	// Opportunistically prune long-dead rows (revoked beyond the retention grace,
	// or expired) so the table stays bounded across sessions. Both states already
	// fail closed at the handler, so deleting them changes nothing observable.
	cutoff := now.Add(-llmFacadeTokenRetention).Unix()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM llm_facade_token WHERE (revoked_at != 0 AND revoked_at < ?) OR (expires_at != 0 AND expires_at < ?)`, cutoff, now.Unix()); err != nil {
		return fmt.Errorf("prune llm facade tokens: %w", err)
	}
	return nil
}

func bootstrapDefaultLLMConfig(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	if hasConfiguredLLMProviderForFamily(ctx, store, llms.ProviderFamilyOpenAI) {
		return nil
	}
	return ensureDefaultOpenAIEnvProvider(ctx, config, store, requestedModel)
}

// llms.EnvProviderLookup resolves an environment value for LLM provider bootstrap.
// It accepts candidate keys and returns the first non-empty value scanning
// sources in priority order (source-major): an earlier source wins across all
// candidate keys before a later source is consulted. This preserves the exact
// precedence the bootstrap paths relied on when they used nested firstNonEmpty.
// defaultLLMEnvProviderLookup reads from global env, then the process env, then
// daemon config. Used by the env_default bootstrap providers.
func defaultLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store *ConfigStore) llms.EnvProviderLookup {
	return func(keys ...string) string {
		for _, key := range keys {
			if v := lookupEnvValue(ctx, store, key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		for _, key := range keys {
			if v := os.Getenv(key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		for _, key := range keys {
			if v := configLLMEnvValue(config, key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
}

// sessionLLMEnvProviderLookup reads only from per-session env items. Used by the
// session_env bootstrap providers.
func sessionLLMEnvProviderLookup(envItems []SessionEnvVar) llms.EnvProviderLookup {
	return func(keys ...string) string {
		for _, key := range keys {
			if v := llms.EnvItemValue(envItems, key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
}

func configLLMEnvValue(config *appconfig.Config, key string) string {
	if config == nil {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "LLM_API_ENDPOINT":
		return config.LLMAPIEndpoint
	case "LLM_API_PROTOCOL":
		return config.LLMAPIProtocol
	case "LLM_API_KEY":
		return config.LLMAPIKey
	case "LLM_MODEL":
		return config.LLMModel
	default:
		return ""
	}
}

func ensureDefaultOpenAIEnvProvider(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	_, err := llms.EnsureOpenAIEnvProvider(ctx, store, defaultLLMEnvProviderLookup(ctx, config, store), llms.ProviderIDDefaultOpenAI, "default", llms.ProviderScopeEnvDefault, requestedModel, true)
	return err
}

func resolveLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) (llms.ResolvedTarget, error) {
	return resolveLLMTargetForProviderFamily(ctx, config, store, llms.ProviderFamilyOpenAI, requestedModel)
}

func resolveRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel, providerID string) (llms.ResolvedTarget, error) {
	return resolveRuntimeLLMTargetWithEnv(ctx, config, store, "", "", requestedModel, providerID, nil)
}

func resolveRuntimeLLMTargetWithEnv(ctx context.Context, config *appconfig.Config, store *ConfigStore, sessionID, preferredProviderFamily, requestedModel, providerID string, envItems []SessionEnvVar) (llms.ResolvedTarget, error) {
	sessionID = strings.TrimSpace(sessionID)
	preferredProviderFamily = llms.NormalizeOptionalProviderType(preferredProviderFamily)
	requestedModel = strings.TrimSpace(requestedModel)
	providerID = strings.TrimSpace(providerID)
	hasSessionEnvProvider := sessionID != "" && llms.HasSessionEnvProviderInput(envItems)
	sessionProviderID := ""
	// Reuse an already-persisted session-env provider when this session can no
	// longer supply a key from env. The raw key env (Session.ProviderEnvItems) is
	// intentionally not persisted, so after a stop/resume the only durable home
	// for a session-scoped credential is the llm_provider row written at creation.
	// Pin its provider id here so resolution selects it (session-env providers are
	// otherwise skipped without an explicit id) and does not clobber its key with
	// the now-empty env. Only when the env still has no key for the family — an env
	// that carries a (possibly rotated) key must keep re-bootstrapping it.
	if providerID == "" && sessionID != "" && preferredProviderFamily != "" && !llms.EnvHasProviderKeyForFamily(envItems, preferredProviderFamily) {
		if candidate := llms.SessionEnvProviderID(sessionID, preferredProviderFamily); hasEnabledLLMProviderID(ctx, store, candidate) {
			providerID = candidate
		}
	}
	// Skip the env/default bootstrap entirely when the request already names a
	// provider that exists. The facade hot path always passes a concrete
	// provider id from the token scope, so this avoids a redundant pair of
	// idempotent provider upserts on every LLM request.
	bootstrapProviders := (providerID == "" || !llms.IsSessionEnvProviderID(providerID)) && !hasEnabledLLMProviderID(ctx, store, providerID)
	if bootstrapProviders && !hasConfiguredLLMProviderForFamily(ctx, store, llms.ProviderFamilyOpenAI) {
		openAIModel := firstNonEmpty(requestedModel, llms.EnvItemValue(envItems, "LLM_MODEL"))
		if sessionID != "" && llms.HasOpenAIEnvProviderInput(envItems) {
			id, err := ensureSessionOpenAIEnvProvider(ctx, store, sessionID, openAIModel, envItems)
			if err != nil {
				return llms.ResolvedTarget{}, err
			}
			sessionProviderID = llms.ChooseSessionEnvProviderID(sessionProviderID, id, llms.ProviderFamilyOpenAI, preferredProviderFamily)
		} else if !hasSessionEnvProvider {
			if err := ensureDefaultOpenAIEnvProvider(ctx, config, store, openAIModel); err != nil {
				return llms.ResolvedTarget{}, err
			}
		}
	}
	if bootstrapProviders && !hasConfiguredLLMProviderForFamily(ctx, store, llms.ProviderFamilyAnthropic) {
		anthropicModel := firstNonEmpty(requestedModel, llms.SessionAnthropicEnvModel(envItems))
		if sessionID != "" && llms.HasAnthropicEnvProviderInput(envItems) {
			id, err := ensureSessionAnthropicEnvProvider(ctx, store, sessionID, anthropicModel, envItems)
			if err != nil {
				return llms.ResolvedTarget{}, err
			}
			sessionProviderID = llms.ChooseSessionEnvProviderID(sessionProviderID, id, llms.ProviderFamilyAnthropic, preferredProviderFamily)
		} else if !hasSessionEnvProvider {
			if err := ensureDefaultAnthropicEnvProvider(ctx, config, store, anthropicModel); err != nil {
				return llms.ResolvedTarget{}, err
			}
		}
	}
	providerID = firstNonEmpty(providerID, sessionProviderID)
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	if len(models) == 0 {
		return llms.ResolvedTarget{}, classifyError(ErrRequired, "llm model is required", nil)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	model, provider, wireAPI, ok, err := llms.SelectModelAndProvider(ctx, store, models, providers, requestedModel, preferredProviderFamily, providerID)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	if !ok {
		if requestedModel != "" && providerID != "" {
			return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured for provider %q", requestedModel, providerID), nil)
		}
		if requestedModel != "" {
			return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured", requestedModel), nil)
		}
		if providerID != "" {
			return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, fmt.Sprintf("llm provider %q is not configured", providerID), nil)
		}
		return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	endpoint := llms.EndpointForProvider(provider, wireAPI)
	headers, err := llms.ProviderForwardHeaders(provider)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	return llms.ResolvedTarget{Provider: provider, Model: model, WireAPI: wireAPI, Endpoint: endpoint, Headers: headers}, nil
}

func bootstrapAnthropicLLMConfig(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	if hasConfiguredLLMProviderForFamily(ctx, store, llms.ProviderFamilyAnthropic) {
		return nil
	}
	return ensureDefaultAnthropicEnvProvider(ctx, config, store, requestedModel)
}

func ensureDefaultAnthropicEnvProvider(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	lookup := defaultLLMEnvProviderLookup(ctx, config, store)
	authHeader, authScheme := llms.AnthropicProviderAuthFromLookup(lookup)
	_, err := llms.EnsureAnthropicEnvProvider(ctx, store, lookup, authHeader, authScheme, llms.ProviderIDDefaultAnthropic, "anthropic", llms.ProviderScopeEnvDefault, requestedModel, false)
	return err
}

func ensureSessionOpenAIEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	providerID := llms.SessionEnvProviderID(sessionID, llms.ProviderFamilyOpenAI)
	return llms.EnsureOpenAIEnvProvider(ctx, store, sessionLLMEnvProviderLookup(envItems), providerID, providerID, llms.ProviderScopeSessionEnv, requestedModel, false)
}

func ensureSessionAnthropicEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	providerID := llms.SessionEnvProviderID(sessionID, llms.ProviderFamilyAnthropic)
	lookup := sessionLLMEnvProviderLookup(envItems)
	authHeader, authScheme := llms.AnthropicProviderAuthFromLookup(lookup)
	return llms.EnsureAnthropicEnvProvider(ctx, store, lookup, authHeader, authScheme, providerID, providerID, llms.ProviderScopeSessionEnv, requestedModel, false)
}

func hasEnabledLLMProviderID(ctx context.Context, store *ConfigStore, providerID string) bool {
	return llms.HasEnabledProviderID(ctx, store, providerID)
}

func hasConfiguredLLMProviderForFamily(ctx context.Context, store *ConfigStore, providerFamily string) bool {
	return llms.HasConfiguredProviderForFamily(ctx, store, providerFamily)
}

func resolveLLMTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store *ConfigStore, providerFamily, requestedModel string) (llms.ResolvedTarget, error) {
	if strings.TrimSpace(providerFamily) != "" {
		providerFamily = llms.NormalizeProviderType(providerFamily)
	}
	switch providerFamily {
	case llms.ProviderFamilyAnthropic:
		if err := bootstrapAnthropicLLMConfig(ctx, config, store, strings.TrimSpace(requestedModel)); err != nil {
			return llms.ResolvedTarget{}, err
		}
	default:
		if err := bootstrapDefaultLLMConfig(ctx, config, store, strings.TrimSpace(requestedModel)); err != nil {
			return llms.ResolvedTarget{}, err
		}
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	if len(models) == 0 {
		return llms.ResolvedTarget{}, classifyError(ErrRequired, "llm model is required", nil)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	model, provider, wireAPI, ok, err := llms.SelectModelAndProvider(ctx, store, models, providers, requestedModel, providerFamily, "")
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	if !ok {
		if strings.TrimSpace(requestedModel) != "" {
			return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured for provider family %q", strings.TrimSpace(requestedModel), providerFamily), nil)
		}
		return llms.ResolvedTarget{}, classifyError(ErrFailedPrecondition, fmt.Sprintf("llm provider is not configured for provider family %q", providerFamily), nil)
	}
	endpoint := llms.EndpointForProvider(provider, wireAPI)
	headers, err := llms.ProviderForwardHeaders(provider)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	return llms.ResolvedTarget{Provider: provider, Model: model, WireAPI: wireAPI, Endpoint: endpoint, Headers: headers}, nil
}

func lookupEnvValue(ctx context.Context, store *ConfigStore, key string) string {
	if store == nil {
		return ""
	}
	items, err := store.ListGlobalEnv(ctx)
	if err != nil {
		return ""
	}
	for _, item := range items {
		if item.Name == key {
			return item.Value
		}
	}
	return ""
}
