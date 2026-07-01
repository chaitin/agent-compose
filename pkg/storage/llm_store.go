package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	llmAPIProtocolResponses       = "responses"
	llmAPIProtocolChatCompletions = "chat_completions"
	llmAPIProtocolMessages        = "messages"

	llmProviderFamilyOpenAI       = LLMProviderFamilyOpenAI
	llmProviderFamilyAnthropic    = LLMProviderFamilyAnthropic
	llmProviderScopeSystem        = LLMProviderScopeSystem
	llmProviderScopeEnvDefault    = LLMProviderScopeEnvDefault
	llmProviderScopeSessionEnv    = LLMProviderScopeSessionEnv
	llmProviderIDDefaultOpenAI    = LLMProviderIDDefaultOpenAI
	llmProviderIDDefaultAnthropic = LLMProviderIDDefaultAnthropic
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider(id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, provider_type = excluded.provider_type, default_wire_api = excluded.default_wire_api, base_url = excluded.base_url, api_key = excluded.api_key, auth_header = excluded.auth_header, auth_scheme = excluded.auth_scheme, headers_json = excluded.headers_json, use_generic_responses_text_parts = excluded.use_generic_responses_text_parts, weight = excluded.weight, enabled = excluded.enabled, scope = excluded.scope, updated_at = excluded.updated_at`, provider.ID, provider.Name, provider.ProviderType, provider.DefaultWireAPI, provider.BaseURL, provider.APIKey, provider.AuthHeader, provider.AuthScheme, provider.HeadersJSON, boolToInt(provider.UseGenericResponsesTextParts), provider.Weight, provider.Scope, now, now); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at FROM llm_provider WHERE enabled != 0 ORDER BY weight ASC, id ASC`)
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
const LLMFacadeTokenRetention = llmFacadeTokenRetention

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
	var genericResponsesTextParts, enabled int
	var createdAt, updatedAt int64
	if err := scan(&item.ID, &item.Name, &item.ProviderType, &item.DefaultWireAPI, &item.BaseURL, &item.APIKey, &item.AuthHeader, &item.AuthScheme, &item.HeadersJSON, &genericResponsesTextParts, &item.Weight, &enabled, &item.Scope, &createdAt, &updatedAt); err != nil {
		return LLMProvider{}, err
	}
	item.UseGenericResponsesTextParts = genericResponsesTextParts != 0
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

func hashLLMFacadeToken(value string) (string, string) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
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
		return LLMProviderFamilyOpenAI
	case "anthropic", "claude", "anthropic_messages":
		return LLMProviderFamilyAnthropic
	default:
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	}
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
