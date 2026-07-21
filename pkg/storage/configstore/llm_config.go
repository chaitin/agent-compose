package configstore

import (
	"agent-compose/pkg/llms"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// llmStore owns LLM provider/model configuration and facade tokens.
type llmStore struct {
	db *sql.DB
}

func (s *llmStore) ensureLLMSchema(ctx context.Context) error {
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
			sandbox_id TEXT NOT NULL,
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
		`CREATE INDEX IF NOT EXISTS idx_llm_facade_token_sandbox ON llm_facade_token(sandbox_id, revoked_at, expires_at);`,
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

func (s *llmStore) HasLLMProviders(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM llm_provider WHERE scope != ?`, llms.ProviderScopeFacadeEnv).Scan(&count); err != nil {
		return false, fmt.Errorf("count llm providers: %w", err)
	}
	return count > 0, nil
}

func (s *llmStore) UpsertDefaultLLMConfig(ctx context.Context, provider llms.Provider, model llms.Model) error {
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
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, provider_type = excluded.provider_type, default_wire_api = excluded.default_wire_api, base_url = excluded.base_url, api_key = excluded.api_key, auth_header = excluded.auth_header, auth_scheme = excluded.auth_scheme, headers_json = excluded.headers_json, use_generic_responses_text_parts = excluded.use_generic_responses_text_parts, weight = excluded.weight, enabled = excluded.enabled, scope = excluded.scope, updated_at = excluded.updated_at`, provider.ID, provider.Name, provider.ProviderType, provider.DefaultWireAPI, provider.BaseURL, provider.APIKey, provider.AuthHeader, provider.AuthScheme, provider.HeadersJSON, BoolToInt(provider.UseGenericResponsesTextParts), provider.Weight, provider.Scope, now, now); err != nil {
		return fmt.Errorf("insert default llm provider: %w", err)
	}
	if model.DefaultModel && model.Scope != llms.ProviderScopeSessionEnv {
		if _, err := tx.ExecContext(ctx, `UPDATE llm_model SET default_model = 0 WHERE default_model != 0`); err != nil {
			return fmt.Errorf("reset default llm models: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, default_model = excluded.default_model, enabled = excluded.enabled, scope = excluded.scope, updated_at = excluded.updated_at
		WHERE excluded.scope != ? OR llm_model.scope = ?`, model.ID, model.Name, model.Description, BoolToInt(model.DefaultModel), BoolToInt(model.Enabled), model.Scope, now, now, llms.ProviderScopeSessionEnv, llms.ProviderScopeSessionEnv); err != nil {
		return fmt.Errorf("insert default llm model: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider_model(provider_id, model_id, wire_api, weight)
		VALUES(?, ?, '', 10)
		ON CONFLICT(provider_id, model_id) DO NOTHING`, provider.ID, model.ID); err != nil {
		return fmt.Errorf("insert default llm provider model: %w", err)
	}
	return tx.Commit()
}

// DeleteSandboxLLMResources removes session-scoped providers and bindings, then
// prunes session models that became unreferenced.
func (s *llmStore) DeleteSandboxLLMResources(ctx context.Context, sandboxID string) error {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return fmt.Errorf("delete sandbox llm resources: sandbox id is required")
	}
	providerIDs := []string{
		llms.SessionEnvProviderID(sandboxID, llms.ProviderFamilyOpenAI),
		llms.SessionEnvProviderID(sandboxID, llms.ProviderFamilyAnthropic),
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sandbox llm resource cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pm.model_id
		FROM llm_provider_model pm
		JOIN llm_provider p ON p.id = pm.provider_id
		WHERE p.scope = ? AND (p.id = ? OR p.id = ?)`, llms.ProviderScopeSessionEnv, providerIDs[0], providerIDs[1])
	if err != nil {
		return fmt.Errorf("list sandbox llm models: %w", err)
	}
	var modelIDs []string
	var scanErr error
	for rows.Next() {
		var modelID string
		if err := rows.Scan(&modelID); err != nil {
			scanErr = err
			break
		}
		modelIDs = append(modelIDs, modelID)
	}
	iterateErr := rows.Err()
	closeErr := rows.Close()
	if scanErr != nil {
		return fmt.Errorf("scan sandbox llm model: %w", scanErr)
	}
	if iterateErr != nil {
		return fmt.Errorf("iterate sandbox llm models: %w", iterateErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close sandbox llm models: %w", closeErr)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_provider_model
		WHERE provider_id IN (
			SELECT id FROM llm_provider WHERE scope = ? AND (id = ? OR id = ?)
		)`, llms.ProviderScopeSessionEnv, providerIDs[0], providerIDs[1]); err != nil {
		return fmt.Errorf("delete sandbox llm provider bindings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_provider
		WHERE scope = ? AND (id = ? OR id = ?)`, llms.ProviderScopeSessionEnv, providerIDs[0], providerIDs[1]); err != nil {
		return fmt.Errorf("delete sandbox llm providers: %w", err)
	}
	for _, modelID := range modelIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM llm_model
			WHERE id = ? AND scope = ?
			AND NOT EXISTS (SELECT 1 FROM llm_provider_model WHERE model_id = llm_model.id)`, modelID, llms.ProviderScopeSessionEnv); err != nil {
			return fmt.Errorf("delete orphaned sandbox llm model %q: %w", modelID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sandbox llm resource cleanup: %w", err)
	}
	return nil
}

func (s *llmStore) ListEnabledLLMProviders(ctx context.Context) ([]llms.Provider, error) {
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

func (s *llmStore) ListEnabledLLMModels(ctx context.Context) ([]llms.Model, error) {
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

func (s *llmStore) LLMProviderModelWireAPI(ctx context.Context, providerID, modelID string) (string, bool, error) {
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
