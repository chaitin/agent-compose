package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"time"
)

const (
	llmProviderFamilyOpenAI       = "openai"
	llmProviderFamilyAnthropic    = "anthropic"
	llmProviderScopeSystem        = "system"
	llmProviderScopeEnvDefault    = "env_default"
	llmProviderScopeSessionEnv    = "session_env"
	llmProviderIDDefaultOpenAI    = "default"
	llmProviderIDDefaultAnthropic = "anthropic"
)

type LLMProvider struct {
	ID             string
	Name           string
	ProviderType   string
	DefaultWireAPI string
	BaseURL        string
	APIKey         string
	AuthHeader     string
	AuthScheme     string
	HeadersJSON    string
	Weight         int
	Enabled        bool
	Scope          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type LLMModel struct {
	ID           string
	Name         string
	Description  string
	DefaultModel bool
	Enabled      bool
	Scope        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type LLMResolvedTarget struct {
	Provider LLMProvider
	Model    LLMModel
	WireAPI  string
	Endpoint string
	Headers  http.Header
}

type LLMFacadeToken struct {
	SessionID        string
	TokenHash        string
	TokenFingerprint string
	Model            string
	ProviderID       string
	WireAPI          string
	Source           string
	RunID            string
	IssuedAt         time.Time
	ExpiresAt        time.Time
	RevokedAt        time.Time
}

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
	return nil
}

func (s *ConfigStore) HasLLMProviders(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM llm_provider`).Scan(&count); err != nil {
		return false, fmt.Errorf("count llm providers: %w", err)
	}
	return count > 0, nil
}

func (s *ConfigStore) UpsertDefaultLLMConfig(ctx context.Context, provider LLMProvider, model LLMModel) error {
	now := time.Now().UTC().Unix()
	provider.ID = firstNonEmpty(strings.TrimSpace(provider.ID), "default")
	provider.Name = firstNonEmpty(strings.TrimSpace(provider.Name), "default")
	provider.ProviderType = normalizeLLMProviderType(provider.ProviderType)
	provider.DefaultWireAPI = normalizeLLMWireAPI(provider.DefaultWireAPI)
	if provider.ProviderType == llmProviderFamilyAnthropic {
		provider.BaseURL = normalizeAnthropicAPIBaseURL(provider.BaseURL)
	} else {
		provider.BaseURL = normalizeLLMAPIBaseURL(provider.BaseURL, provider.DefaultWireAPI)
	}
	provider.AuthHeader = firstNonEmpty(strings.TrimSpace(provider.AuthHeader), "Authorization")
	provider.AuthScheme = strings.TrimSpace(provider.AuthScheme)
	if provider.AuthScheme == "" && strings.EqualFold(provider.AuthHeader, "Authorization") {
		provider.AuthScheme = "Bearer"
	}
	provider.HeadersJSON = firstNonEmpty(strings.TrimSpace(provider.HeadersJSON), "{}")
	if provider.Weight == 0 {
		provider.Weight = 10
	}
	provider.Scope = firstNonEmpty(strings.TrimSpace(provider.Scope), llmProviderScopeSystem)
	model.ID = firstNonEmpty(strings.TrimSpace(model.ID), strings.TrimSpace(model.Name))
	model.Name = firstNonEmpty(strings.TrimSpace(model.Name), model.ID)
	model.Enabled = true
	model.Scope = firstNonEmpty(strings.TrimSpace(model.Scope), llmProviderScopeSystem)
	if model.ID == "" || model.Name == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin llm default config tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider(id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, provider_type = excluded.provider_type, default_wire_api = excluded.default_wire_api, base_url = excluded.base_url, api_key = excluded.api_key, auth_header = excluded.auth_header, auth_scheme = excluded.auth_scheme, headers_json = excluded.headers_json, weight = excluded.weight, enabled = excluded.enabled, scope = excluded.scope, updated_at = excluded.updated_at`, provider.ID, provider.Name, provider.ProviderType, provider.DefaultWireAPI, provider.BaseURL, provider.APIKey, provider.AuthHeader, provider.AuthScheme, provider.HeadersJSON, provider.Weight, provider.Scope, now, now); err != nil {
		return fmt.Errorf("insert default llm provider: %w", err)
	}
	if model.DefaultModel {
		if _, err := tx.ExecContext(ctx, `UPDATE llm_model SET default_model = 0 WHERE default_model != 0`); err != nil {
			return fmt.Errorf("reset default llm models: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, default_model = excluded.default_model, enabled = excluded.enabled, scope = excluded.scope, updated_at = excluded.updated_at`, model.ID, model.Name, model.Description, boolToInt(model.DefaultModel), boolToInt(model.Enabled), model.Scope, now, now); err != nil {
		return fmt.Errorf("insert default llm model: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider_model(provider_id, model_id, wire_api, weight)
		VALUES(?, ?, '', 10)
		ON CONFLICT(provider_id, model_id) DO NOTHING`, provider.ID, model.ID); err != nil {
		return fmt.Errorf("insert default llm provider model: %w", err)
	}
	return tx.Commit()
}

func (s *ConfigStore) ListEnabledLLMProviders(ctx context.Context) ([]LLMProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, weight, enabled, scope, created_at, updated_at FROM llm_provider WHERE enabled != 0 ORDER BY weight ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query llm providers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var providers []LLMProvider
	for rows.Next() {
		item, err := scanLLMProvider(rows.Scan)
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

func (s *ConfigStore) ListEnabledLLMModels(ctx context.Context) ([]LLMModel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, default_model, enabled, scope, created_at, updated_at FROM llm_model WHERE enabled != 0 ORDER BY default_model DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query llm models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var models []LLMModel
	for rows.Next() {
		item, err := scanLLMModel(rows.Scan)
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
	return normalizeLLMWireAPI(wireAPI), true, nil
}

func (s *ConfigStore) SaveLLMFacadeToken(ctx context.Context, token LLMFacadeToken) error {
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
	hash, _ := hashLLMFacadeToken(rawToken)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM llm_facade_token WHERE token_hash = ?`, hash); err != nil {
		return fmt.Errorf("delete llm facade token: %w", err)
	}
	return nil
}

func (s *ConfigStore) GetLLMFacadeToken(ctx context.Context, rawToken string) (LLMFacadeToken, error) {
	hash, fingerprint := hashLLMFacadeToken(rawToken)
	row := s.db.QueryRowContext(ctx, `SELECT session_id, token_hash, token_fingerprint, model, provider_id, wire_api, source, run_id, issued_at, expires_at, revoked_at FROM llm_facade_token WHERE token_hash = ?`, hash)
	token, err := scanLLMFacadeToken(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LLMFacadeToken{}, fmt.Errorf("llm facade token %s not found: %w", fingerprint, err)
		}
		return LLMFacadeToken{}, err
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

func scanLLMProvider(scan func(dest ...any) error) (LLMProvider, error) {
	var item LLMProvider
	var enabled int
	var createdAt, updatedAt int64
	if err := scan(&item.ID, &item.Name, &item.ProviderType, &item.DefaultWireAPI, &item.BaseURL, &item.APIKey, &item.AuthHeader, &item.AuthScheme, &item.HeadersJSON, &item.Weight, &enabled, &item.Scope, &createdAt, &updatedAt); err != nil {
		return LLMProvider{}, err
	}
	item.Enabled = enabled != 0
	item.ProviderType = normalizeLLMProviderType(item.ProviderType)
	item.DefaultWireAPI = normalizeLLMWireAPI(item.DefaultWireAPI)
	item.CreatedAt = parseStoredTime(createdAt)
	item.UpdatedAt = parseStoredTime(updatedAt)
	return item, nil
}

func scanLLMModel(scan func(dest ...any) error) (LLMModel, error) {
	var item LLMModel
	var defaultModel, enabled int
	var createdAt, updatedAt int64
	if err := scan(&item.ID, &item.Name, &item.Description, &defaultModel, &enabled, &item.Scope, &createdAt, &updatedAt); err != nil {
		return LLMModel{}, err
	}
	item.DefaultModel = defaultModel != 0
	item.Enabled = enabled != 0
	item.CreatedAt = parseStoredTime(createdAt)
	item.UpdatedAt = parseStoredTime(updatedAt)
	return item, nil
}

func scanLLMFacadeToken(scan func(dest ...any) error) (LLMFacadeToken, error) {
	var item LLMFacadeToken
	var issuedAt, expiresAt, revokedAt int64
	if err := scan(&item.SessionID, &item.TokenHash, &item.TokenFingerprint, &item.Model, &item.ProviderID, &item.WireAPI, &item.Source, &item.RunID, &issuedAt, &expiresAt, &revokedAt); err != nil {
		return LLMFacadeToken{}, err
	}
	item.IssuedAt = parseStoredTime(issuedAt)
	item.ExpiresAt = parseStoredTime(expiresAt)
	item.RevokedAt = parseStoredTime(revokedAt)
	return item, nil
}

func bootstrapDefaultLLMConfig(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	if hasConfiguredLLMProviderForFamily(ctx, store, llmProviderFamilyOpenAI) {
		return nil
	}
	return ensureDefaultOpenAIEnvProvider(ctx, config, store, requestedModel)
}

// llmEnvProviderLookup resolves an environment value for LLM provider bootstrap.
// It accepts candidate keys and returns the first non-empty value scanning
// sources in priority order (source-major): an earlier source wins across all
// candidate keys before a later source is consulted. This preserves the exact
// precedence the bootstrap paths relied on when they used nested firstNonEmpty.
type llmEnvProviderLookup func(keys ...string) string

// defaultLLMEnvProviderLookup reads from global env, then the process env, then
// daemon config. Used by the env_default bootstrap providers.
func defaultLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store *ConfigStore) llmEnvProviderLookup {
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
func sessionLLMEnvProviderLookup(envItems []SessionEnvVar) llmEnvProviderLookup {
	return func(keys ...string) string {
		for _, key := range keys {
			if v := lookupEnvItemValue(envItems, key); strings.TrimSpace(v) != "" {
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

// ensureOpenAIEnvProvider upserts an OpenAI-family provider from a resolved env
// lookup. It returns the provider id (empty when nothing was configured).
func ensureOpenAIEnvProvider(ctx context.Context, store *ConfigStore, lookup llmEnvProviderLookup, providerID, name, scope, requestedModel string, defaultModel bool) (string, error) {
	endpoint := firstNonEmpty(lookup("LLM_API_ENDPOINT"), "https://api.openai.com")
	if looksLikeAnthropicMessagesEndpoint(endpoint) {
		return "", nil
	}
	protocol := normalizeLLMWireAPI(lookup("LLM_API_PROTOCOL"))
	apiKey := lookup("LLM_API_KEY", "OPENAI_API_KEY")
	model := strings.TrimSpace(firstNonEmpty(requestedModel, lookup("LLM_MODEL")))
	if providerID == "" || model == "" {
		return "", nil
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, LLMProvider{
		ID:             providerID,
		Name:           name,
		ProviderType:   llmProviderFamilyOpenAI,
		DefaultWireAPI: protocol,
		BaseURL:        endpoint,
		APIKey:         apiKey,
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, LLMModel{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}

// ensureAnthropicEnvProvider upserts an Anthropic-family provider from a resolved
// env lookup. It returns the provider id (empty when nothing was configured).
func ensureAnthropicEnvProvider(ctx context.Context, store *ConfigStore, lookup llmEnvProviderLookup, authHeader, authScheme, providerID, name, scope, requestedModel string, defaultModel bool) (string, error) {
	anthropicEndpoint := lookup("ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT")
	genericEndpoint := lookup("LLM_API_ENDPOINT")
	anthropicKey := lookup("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
	anthropicModel := lookup("ANTHROPIC_MODEL", "CLAUDE_MODEL")
	genericModel := lookup("LLM_MODEL")
	useGenericEndpoint := anthropicEndpoint == "" && looksLikeAnthropicMessagesEndpoint(genericEndpoint)
	if useGenericEndpoint {
		anthropicEndpoint = genericEndpoint
	}
	if genericModel != "" && (useGenericEndpoint || anthropicEndpoint != "" || anthropicKey != "" || anthropicModel != "") {
		anthropicModel = firstNonEmpty(anthropicModel, genericModel)
	}
	if anthropicEndpoint == "" && strings.TrimSpace(anthropicKey) == "" && strings.TrimSpace(anthropicModel) == "" {
		return "", nil
	}
	endpoint := firstNonEmpty(anthropicEndpoint, "https://api.anthropic.com")
	apiKey := firstNonEmpty(anthropicKey, lookup("LLM_API_KEY"))
	model := strings.TrimSpace(firstNonEmpty(requestedModel, anthropicModel))
	if providerID == "" || model == "" {
		return "", nil
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, LLMProvider{
		ID:             providerID,
		Name:           name,
		ProviderType:   llmProviderFamilyAnthropic,
		DefaultWireAPI: llmAPIProtocolMessages,
		BaseURL:        endpoint,
		APIKey:         apiKey,
		AuthHeader:     authHeader,
		AuthScheme:     authScheme,
		HeadersJSON:    `{"anthropic-version":"2023-06-01"}`,
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, LLMModel{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}

func ensureDefaultOpenAIEnvProvider(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	_, err := ensureOpenAIEnvProvider(ctx, store, defaultLLMEnvProviderLookup(ctx, config, store), llmProviderIDDefaultOpenAI, "default", llmProviderScopeEnvDefault, requestedModel, true)
	return err
}

func resolveLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) (LLMResolvedTarget, error) {
	return resolveLLMTargetForProviderFamily(ctx, config, store, llmProviderFamilyOpenAI, requestedModel)
}

func resolveRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel, providerID string) (LLMResolvedTarget, error) {
	return resolveRuntimeLLMTargetWithEnv(ctx, config, store, "", "", requestedModel, providerID, nil)
}

func resolveRuntimeLLMTargetWithEnv(ctx context.Context, config *appconfig.Config, store *ConfigStore, sessionID, preferredProviderFamily, requestedModel, providerID string, envItems []SessionEnvVar) (LLMResolvedTarget, error) {
	sessionID = strings.TrimSpace(sessionID)
	preferredProviderFamily = normalizeOptionalLLMProviderType(preferredProviderFamily)
	requestedModel = strings.TrimSpace(requestedModel)
	providerID = strings.TrimSpace(providerID)
	hasSessionEnvProvider := sessionID != "" && hasSessionEnvProviderInput(envItems)
	sessionProviderID := ""
	// Reuse an already-persisted session-env provider when this session can no
	// longer supply a key from env. The raw key env (Session.ProviderEnvItems) is
	// intentionally not persisted, so after a stop/resume the only durable home
	// for a session-scoped credential is the llm_provider row written at creation.
	// Pin its provider id here so resolution selects it (session-env providers are
	// otherwise skipped without an explicit id) and does not clobber its key with
	// the now-empty env. Only when the env still has no key for the family — an env
	// that carries a (possibly rotated) key must keep re-bootstrapping it.
	if providerID == "" && sessionID != "" && preferredProviderFamily != "" && !envHasProviderKeyForFamily(envItems, preferredProviderFamily) {
		if candidate := sessionEnvProviderID(sessionID, preferredProviderFamily); hasEnabledLLMProviderID(ctx, store, candidate) {
			providerID = candidate
		}
	}
	// Skip the env/default bootstrap entirely when the request already names a
	// provider that exists. The facade hot path always passes a concrete
	// provider id from the token scope, so this avoids a redundant pair of
	// idempotent provider upserts on every LLM request.
	bootstrapProviders := (providerID == "" || !isSessionEnvProviderID(providerID)) && !hasEnabledLLMProviderID(ctx, store, providerID)
	if bootstrapProviders && !hasConfiguredLLMProviderForFamily(ctx, store, llmProviderFamilyOpenAI) {
		openAIModel := firstNonEmpty(requestedModel, lookupEnvItemValue(envItems, "LLM_MODEL"))
		if sessionID != "" && hasOpenAIEnvProviderInput(envItems) {
			id, err := ensureSessionOpenAIEnvProvider(ctx, store, sessionID, openAIModel, envItems)
			if err != nil {
				return LLMResolvedTarget{}, err
			}
			sessionProviderID = chooseSessionEnvProviderID(sessionProviderID, id, llmProviderFamilyOpenAI, preferredProviderFamily)
		} else if !hasSessionEnvProvider {
			if err := ensureDefaultOpenAIEnvProvider(ctx, config, store, openAIModel); err != nil {
				return LLMResolvedTarget{}, err
			}
		}
	}
	if bootstrapProviders && !hasConfiguredLLMProviderForFamily(ctx, store, llmProviderFamilyAnthropic) {
		anthropicModel := firstNonEmpty(requestedModel, sessionAnthropicEnvModel(envItems))
		if sessionID != "" && hasAnthropicEnvProviderInput(envItems) {
			id, err := ensureSessionAnthropicEnvProvider(ctx, store, sessionID, anthropicModel, envItems)
			if err != nil {
				return LLMResolvedTarget{}, err
			}
			sessionProviderID = chooseSessionEnvProviderID(sessionProviderID, id, llmProviderFamilyAnthropic, preferredProviderFamily)
		} else if !hasSessionEnvProvider {
			if err := ensureDefaultAnthropicEnvProvider(ctx, config, store, anthropicModel); err != nil {
				return LLMResolvedTarget{}, err
			}
		}
	}
	providerID = firstNonEmpty(providerID, sessionProviderID)
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	if len(models) == 0 {
		return LLMResolvedTarget{}, fmt.Errorf("llm model is required")
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return LLMResolvedTarget{}, fmt.Errorf("llm provider is not configured")
	}
	model, provider, wireAPI, ok, err := selectLLMModelAndProvider(ctx, store, models, providers, requestedModel, preferredProviderFamily, providerID)
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	if !ok {
		if requestedModel != "" && providerID != "" {
			return LLMResolvedTarget{}, fmt.Errorf("llm model %q is not configured for provider %q", requestedModel, providerID)
		}
		if requestedModel != "" {
			return LLMResolvedTarget{}, fmt.Errorf("llm model %q is not configured", requestedModel)
		}
		if providerID != "" {
			return LLMResolvedTarget{}, fmt.Errorf("llm provider %q is not configured", providerID)
		}
		return LLMResolvedTarget{}, fmt.Errorf("llm provider is not configured")
	}
	endpoint := llmEndpointForProvider(provider, wireAPI)
	headers, err := providerForwardHeaders(provider)
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	return LLMResolvedTarget{Provider: provider, Model: model, WireAPI: wireAPI, Endpoint: endpoint, Headers: headers}, nil
}

func bootstrapAnthropicLLMConfig(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	if hasConfiguredLLMProviderForFamily(ctx, store, llmProviderFamilyAnthropic) {
		return nil
	}
	return ensureDefaultAnthropicEnvProvider(ctx, config, store, requestedModel)
}

func ensureDefaultAnthropicEnvProvider(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) error {
	lookup := defaultLLMEnvProviderLookup(ctx, config, store)
	authHeader, authScheme := anthropicProviderAuthFromLookup(lookup)
	_, err := ensureAnthropicEnvProvider(ctx, store, lookup, authHeader, authScheme, llmProviderIDDefaultAnthropic, "anthropic", llmProviderScopeEnvDefault, requestedModel, false)
	return err
}

func looksLikeAnthropicMessagesEndpoint(endpoint string) bool {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return strings.HasSuffix(endpoint, "/messages")
	}
	return strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/messages")
}

// anthropicProviderAuthFromLookup chooses the Anthropic auth header from the same
// env source(s) the provider's API key is resolved from, so a provider never
// mixes a key from one scope with a header decided by another scope.
func anthropicProviderAuthFromLookup(lookup llmEnvProviderLookup) (string, string) {
	if strings.TrimSpace(lookup("ANTHROPIC_API_KEY")) != "" {
		return "x-api-key", ""
	}
	if strings.TrimSpace(lookup("ANTHROPIC_AUTH_TOKEN")) != "" {
		return "Authorization", "Bearer"
	}
	return "x-api-key", ""
}

func ensureSessionOpenAIEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	providerID := sessionEnvProviderID(sessionID, llmProviderFamilyOpenAI)
	return ensureOpenAIEnvProvider(ctx, store, sessionLLMEnvProviderLookup(envItems), providerID, providerID, llmProviderScopeSessionEnv, requestedModel, false)
}

func ensureSessionAnthropicEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	providerID := sessionEnvProviderID(sessionID, llmProviderFamilyAnthropic)
	lookup := sessionLLMEnvProviderLookup(envItems)
	authHeader, authScheme := anthropicProviderAuthFromLookup(lookup)
	return ensureAnthropicEnvProvider(ctx, store, lookup, authHeader, authScheme, providerID, providerID, llmProviderScopeSessionEnv, requestedModel, false)
}

func hasEnabledLLMProviderID(ctx context.Context, store *ConfigStore, providerID string) bool {
	providerID = strings.TrimSpace(providerID)
	if store == nil || providerID == "" {
		return false
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return false
	}
	for _, provider := range providers {
		if provider.ID == providerID {
			return true
		}
	}
	return false
}

// envHasProviderKeyForFamily reports whether the given env carries a usable
// provider credential for the family. Unlike hasOpenAIEnvProviderInput it checks
// for an actual key (not just an endpoint), so callers can distinguish "the env
// can (re)bootstrap a provider with a fresh key" from "the env has no key and we
// should reuse the already-persisted session-env provider".
func envHasProviderKeyForFamily(envItems []SessionEnvVar, providerFamily string) bool {
	switch normalizeLLMProviderType(providerFamily) {
	case llmProviderFamilyAnthropic:
		return strings.TrimSpace(firstNonEmpty(
			lookupEnvItemValue(envItems, "ANTHROPIC_API_KEY"),
			lookupEnvItemValue(envItems, "ANTHROPIC_AUTH_TOKEN"),
			lookupEnvItemValue(envItems, "LLM_API_KEY"),
		)) != ""
	case llmProviderFamilyOpenAI:
		return strings.TrimSpace(firstNonEmpty(
			lookupEnvItemValue(envItems, "LLM_API_KEY"),
			lookupEnvItemValue(envItems, "OPENAI_API_KEY"),
		)) != ""
	default:
		return false
	}
}

func hasConfiguredLLMProviderForFamily(ctx context.Context, store *ConfigStore, providerFamily string) bool {
	if store == nil {
		return false
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return false
	}
	for _, provider := range providers {
		if normalizeLLMProviderType(provider.ProviderType) != normalizeLLMProviderType(providerFamily) {
			continue
		}
		if llmProviderScopeIsConfigured(provider.Scope) {
			return true
		}
	}
	return false
}

func llmProviderScopeIsConfigured(scope string) bool {
	switch strings.TrimSpace(scope) {
	case llmProviderScopeEnvDefault, llmProviderScopeSessionEnv:
		return false
	default:
		return true
	}
}

func hasOpenAIEnvProviderInput(envItems []SessionEnvVar) bool {
	endpoint := lookupEnvItemValue(envItems, "LLM_API_ENDPOINT")
	if looksLikeAnthropicMessagesEndpoint(endpoint) {
		return false
	}
	return strings.TrimSpace(firstNonEmpty(
		endpoint,
		lookupEnvItemValue(envItems, "LLM_API_KEY"),
		lookupEnvItemValue(envItems, "OPENAI_API_KEY"),
	)) != ""
}

func hasAnthropicEnvProviderInput(envItems []SessionEnvVar) bool {
	return strings.TrimSpace(firstNonEmpty(
		lookupEnvItemValue(envItems, "ANTHROPIC_BASE_URL"),
		lookupEnvItemValue(envItems, "ANTHROPIC_API_ENDPOINT"),
		lookupEnvItemValue(envItems, "ANTHROPIC_API_KEY"),
		lookupEnvItemValue(envItems, "ANTHROPIC_AUTH_TOKEN"),
	)) != "" || looksLikeAnthropicMessagesEndpoint(lookupEnvItemValue(envItems, "LLM_API_ENDPOINT"))
}

func hasSessionEnvProviderInput(envItems []SessionEnvVar) bool {
	return hasOpenAIEnvProviderInput(envItems) || hasAnthropicEnvProviderInput(envItems)
}

func sessionAnthropicEnvModel(envItems []SessionEnvVar) string {
	genericModel := lookupEnvItemValue(envItems, "LLM_MODEL")
	return firstNonEmpty(
		lookupEnvItemValue(envItems, "ANTHROPIC_MODEL"),
		lookupEnvItemValue(envItems, "CLAUDE_MODEL"),
		genericModel,
	)
}

func sessionEnvProviderID(sessionID, providerFamily string) string {
	sessionID = strings.TrimSpace(sessionID)
	providerFamily = normalizeOptionalLLMProviderType(providerFamily)
	if sessionID == "" || providerFamily == "" {
		return ""
	}
	return "session-env:" + sessionID + ":" + providerFamily
}

func isSessionEnvProviderID(providerID string) bool {
	return strings.HasPrefix(strings.TrimSpace(providerID), "session-env:")
}

func chooseSessionEnvProviderID(current, next, nextFamily, preferredFamily string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if strings.TrimSpace(current) == "" {
		return next
	}
	preferredFamily = normalizeOptionalLLMProviderType(preferredFamily)
	if preferredFamily != "" && normalizeLLMProviderType(nextFamily) == preferredFamily {
		return next
	}
	return current
}

func resolveLLMTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store *ConfigStore, providerFamily, requestedModel string) (LLMResolvedTarget, error) {
	if strings.TrimSpace(providerFamily) != "" {
		providerFamily = normalizeLLMProviderType(providerFamily)
	}
	switch providerFamily {
	case llmProviderFamilyAnthropic:
		if err := bootstrapAnthropicLLMConfig(ctx, config, store, strings.TrimSpace(requestedModel)); err != nil {
			return LLMResolvedTarget{}, err
		}
	default:
		if err := bootstrapDefaultLLMConfig(ctx, config, store, strings.TrimSpace(requestedModel)); err != nil {
			return LLMResolvedTarget{}, err
		}
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	if len(models) == 0 {
		return LLMResolvedTarget{}, fmt.Errorf("llm model is required")
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return LLMResolvedTarget{}, fmt.Errorf("llm provider is not configured")
	}
	model, provider, wireAPI, ok, err := selectLLMModelAndProvider(ctx, store, models, providers, requestedModel, providerFamily, "")
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	if !ok {
		if strings.TrimSpace(requestedModel) != "" {
			return LLMResolvedTarget{}, fmt.Errorf("llm model %q is not configured for provider family %q", strings.TrimSpace(requestedModel), providerFamily)
		}
		return LLMResolvedTarget{}, fmt.Errorf("llm provider is not configured for provider family %q", providerFamily)
	}
	endpoint := llmEndpointForProvider(provider, wireAPI)
	headers, err := providerForwardHeaders(provider)
	if err != nil {
		return LLMResolvedTarget{}, err
	}
	return LLMResolvedTarget{Provider: provider, Model: model, WireAPI: wireAPI, Endpoint: endpoint, Headers: headers}, nil
}

func selectLLMModel(models []LLMModel, requested string) LLMModel {
	requested = strings.TrimSpace(requested)
	for _, model := range models {
		if requested != "" && (model.ID == requested || model.Name == requested) {
			return model
		}
	}
	if requested != "" {
		return LLMModel{}
	}
	for _, model := range models {
		if model.DefaultModel {
			return model
		}
	}
	return models[0]
}

func selectLLMModelAndProvider(ctx context.Context, store *ConfigStore, models []LLMModel, providers []LLMProvider, requestedModel, providerFamily, providerID string) (LLMModel, LLMProvider, string, bool, error) {
	if strings.TrimSpace(requestedModel) != "" {
		requested := selectLLMModel(models, requestedModel)
		if strings.TrimSpace(requested.ID) == "" {
			return LLMModel{}, LLMProvider{}, "", false, nil
		}
		provider, wireAPI, ok, err := selectLLMProviderForModel(ctx, store, providers, requested.ID, providerFamily, providerID)
		return requested, provider, wireAPI, ok, err
	}
	ordered := append([]LLMModel(nil), models...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].DefaultModel != ordered[j].DefaultModel {
			return ordered[i].DefaultModel
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, model := range ordered {
		provider, wireAPI, ok, err := selectLLMProviderForModel(ctx, store, providers, model.ID, providerFamily, providerID)
		if err != nil {
			return LLMModel{}, LLMProvider{}, "", false, err
		}
		if ok {
			return model, provider, wireAPI, true, nil
		}
	}
	return LLMModel{}, LLMProvider{}, "", false, nil
}

func selectLLMProviderForModel(ctx context.Context, store *ConfigStore, providers []LLMProvider, modelID, providerFamily, providerID string) (LLMProvider, string, bool, error) {
	type candidate struct {
		provider LLMProvider
		wireAPI  string
		priority int
	}
	if strings.TrimSpace(providerFamily) != "" {
		providerFamily = normalizeLLMProviderType(providerFamily)
	}
	providerID = strings.TrimSpace(providerID)
	var candidates []candidate
	for _, provider := range providers {
		if providerID == "" && providerFamily != "" && normalizeLLMProviderType(provider.ProviderType) != providerFamily {
			continue
		}
		if providerID != "" && provider.ID != providerID {
			continue
		}
		if providerID == "" && strings.TrimSpace(provider.Scope) == llmProviderScopeSessionEnv {
			continue
		}
		wireAPI, ok, err := store.LLMProviderModelWireAPI(ctx, provider.ID, modelID)
		if err != nil {
			return LLMProvider{}, "", false, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{provider: provider, wireAPI: firstNonEmpty(wireAPI, normalizeLLMWireAPI(provider.DefaultWireAPI)), priority: llmProviderSelectionPriority(provider.Scope)})
	}
	if len(candidates) == 0 {
		return LLMProvider{}, "", false, nil
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

func llmProviderSelectionPriority(scope string) int {
	switch strings.TrimSpace(scope) {
	case llmProviderScopeSessionEnv:
		return 2
	case llmProviderScopeEnvDefault:
		return 1
	default:
		return 0
	}
}

func providerForwardHeaders(provider LLMProvider) (http.Header, error) {
	headers := http.Header{}
	if raw := strings.TrimSpace(provider.HeadersJSON); raw != "" && raw != "{}" {
		custom := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &custom); err != nil {
			return nil, fmt.Errorf("decode llm provider headers: %w", err)
		}
		for key, value := range custom {
			if forbiddenProviderHeader(key, provider.AuthHeader) {
				continue
			}
			headers.Set(strings.TrimSpace(key), value)
		}
	}
	authHeader := firstNonEmpty(strings.TrimSpace(provider.AuthHeader), "Authorization")
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey != "" {
		if scheme := strings.TrimSpace(provider.AuthScheme); scheme != "" {
			headers.Set(authHeader, scheme+" "+apiKey)
		} else {
			headers.Set(authHeader, apiKey)
		}
	}
	return headers, nil
}

func forbiddenProviderHeader(name, authHeader string) bool {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if canonical == "" || canonical == strings.ToLower(strings.TrimSpace(authHeader)) {
		return true
	}
	switch canonical {
	case "authorization", "proxy-authorization", "host", "content-length", "content-type", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func normalizeLLMWireAPI(value string) string {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_") {
	case "", llmAPIProtocolResponses:
		return llmAPIProtocolResponses
	case "chat", "chat_completion", llmAPIProtocolChatCompletions:
		return llmAPIProtocolChatCompletions
	case "message", llmAPIProtocolMessages:
		return llmAPIProtocolMessages
	default:
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	}
}

func normalizeLLMProviderType(value string) string {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_") {
	case "", "openai", "openai_compatible":
		return llmProviderFamilyOpenAI
	case "anthropic", "claude", "anthropic_messages":
		return llmProviderFamilyAnthropic
	default:
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	}
}

func normalizeOptionalLLMProviderType(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return normalizeLLMProviderType(value)
}

func normalizeLLMAPIBaseURL(raw, wireAPI string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(cleanPath, "/responses"):
		parsed.Path = strings.TrimSuffix(cleanPath, "/responses")
	case strings.HasSuffix(cleanPath, "/chat/completions"):
		parsed.Path = strings.TrimSuffix(cleanPath, "/chat/completions")
	default:
		parsed.Path = cleanPath
	}
	return strings.TrimRight(parsed.String(), "/")
}

func llmEndpointForWireAPI(baseURL, wireAPI string) string {
	baseURL = normalizeLLMAPIBaseURL(baseURL, wireAPI)
	return normalizeLLMAPIEndpointForProtocol(baseURL, wireAPI)
}

func llmEndpointForProvider(provider LLMProvider, wireAPI string) string {
	if normalizeLLMProviderType(provider.ProviderType) == llmProviderFamilyAnthropic {
		baseURL := normalizeAnthropicAPIBaseURL(provider.BaseURL)
		parsed, err := url.Parse(baseURL)
		if err != nil {
			return strings.TrimRight(baseURL, "/") + "/messages"
		}
		parsed.Path = pathpkg.Join(parsed.Path, "messages")
		return parsed.String()
	}
	return llmEndpointForWireAPI(provider.BaseURL, wireAPI)
}

func normalizeAnthropicAPIBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(cleanPath, "/messages"):
		parsed.Path = strings.TrimSuffix(cleanPath, "/messages")
	case cleanPath == "":
		parsed.Path = "/v1"
	default:
		parsed.Path = cleanPath
	}
	return strings.TrimRight(parsed.String(), "/")
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

func lookupEnvItemValue(items []SessionEnvVar, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	for _, item := range normalizeEnvItems(items) {
		if strings.EqualFold(strings.TrimSpace(item.Name), key) {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}

func newLLMFacadeToken(sessionID, model, providerID, wireAPI, source, runID string) (string, LLMFacadeToken, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", LLMFacadeToken{}, err
	}
	tokenValue := "ac_llm_" + hex.EncodeToString(raw)
	hash, fingerprint := hashLLMFacadeToken(tokenValue)
	now := time.Now().UTC()
	return tokenValue, LLMFacadeToken{
		SessionID:        strings.TrimSpace(sessionID),
		TokenHash:        hash,
		TokenFingerprint: fingerprint,
		Model:            strings.TrimSpace(model),
		ProviderID:       strings.TrimSpace(providerID),
		WireAPI:          normalizeLLMWireAPI(wireAPI),
		Source:           strings.TrimSpace(source),
		RunID:            strings.TrimSpace(runID),
		IssuedAt:         now,
	}, nil
}

func hashLLMFacadeToken(value string) (string, string) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
}

func llmProviderKeyName(name string) bool {
	return driverpkg.LLMProviderKeyName(name)
}

func filterPersistedRuntimeEnv(items []SessionEnvVar) []SessionEnvVar {
	result := make([]SessionEnvVar, 0, len(items))
	for _, item := range normalizeEnvItems(items) {
		if llmProviderKeyName(item.Name) {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func runtimeEnvMap(items []SessionEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range normalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" || llmProviderKeyName(name) {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func managedRuntimeEnvMap(items []SessionEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range normalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
