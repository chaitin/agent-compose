package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func (s *llmStore) SaveLLMFacadeToken(ctx context.Context, token llms.FacadeToken) error {
	return s.SaveLLMFacadeGrant(ctx, llms.FacadeGrant{Token: token})
}

// SaveLLMFacadeGrant atomically persists a token and its token-owned sparse
// Provider Env layer, when env bootstrap selected the upstream.
func (s *llmStore) SaveLLMFacadeGrant(ctx context.Context, grant llms.FacadeGrant) error {
	token := grant.Token
	if strings.TrimSpace(token.TokenHash) == "" || strings.TrimSpace(token.SandboxID) == "" {
		return fmt.Errorf("llm facade token hash and sandbox id are required")
	}
	if grant.Environment == nil && llms.IsFacadeEnvironmentProviderID(token.ProviderID) {
		return fmt.Errorf("llm facade environment is required")
	}
	if grant.Environment != nil {
		expectedID := llms.FacadeEnvironmentProviderID(token.TokenHash, grant.Environment.ProviderType)
		if expectedID == "" || token.ProviderID != expectedID || grant.Environment.ProviderID != expectedID {
			return fmt.Errorf("llm facade environment provider id is invalid")
		}
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin llm facade grant tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var previousProviderID string
	if err := tx.QueryRowContext(ctx, `SELECT provider_id FROM llm_facade_token WHERE token_hash = ?`, token.TokenHash).Scan(&previousProviderID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolve previous llm facade grant: %w", err)
	}
	if grant.Environment != nil {
		if err := saveLLMFacadeEnvironment(ctx, tx, token, *grant.Environment); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO llm_facade_token(token_hash, sandbox_id, token_fingerprint, model, provider_id, wire_api, source, run_id, issued_at, expires_at, revoked_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET sandbox_id = excluded.sandbox_id, token_fingerprint = excluded.token_fingerprint, model = excluded.model, provider_id = excluded.provider_id, wire_api = excluded.wire_api, source = excluded.source, run_id = excluded.run_id, issued_at = excluded.issued_at, expires_at = excluded.expires_at, revoked_at = excluded.revoked_at`,
		token.TokenHash, token.SandboxID, token.TokenFingerprint, token.Model, token.ProviderID, token.WireAPI, token.Source, token.RunID, token.IssuedAt.Unix(), expiresAt, revokedAt)
	if err != nil {
		return fmt.Errorf("save llm facade token: %w", err)
	}
	if previousProviderID != "" && previousProviderID != token.ProviderID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM llm_provider WHERE id = ? AND scope = ?`, previousProviderID, llms.ProviderScopeFacadeEnv); err != nil {
			return fmt.Errorf("delete replaced llm facade environment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit llm facade grant: %w", err)
	}
	return nil
}

func saveLLMFacadeEnvironment(ctx context.Context, tx *sql.Tx, token llms.FacadeToken, environment llms.FacadeEnvironment) error {
	protocol := strings.TrimSpace(environment.Protocol)
	if protocol != "" {
		protocol = llms.NormalizeWireAPI(protocol)
	}
	now := token.IssuedAt.Unix()
	result, err := tx.ExecContext(ctx, `INSERT INTO llm_provider(id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, '{}', 0, 10, 0, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, provider_type = excluded.provider_type, default_wire_api = excluded.default_wire_api, base_url = excluded.base_url, api_key = excluded.api_key, auth_header = excluded.auth_header, auth_scheme = excluded.auth_scheme, headers_json = excluded.headers_json, use_generic_responses_text_parts = excluded.use_generic_responses_text_parts, weight = excluded.weight, enabled = excluded.enabled, updated_at = excluded.updated_at
		WHERE llm_provider.scope = excluded.scope`,
		environment.ProviderID,
		"facade-env:"+token.TokenFingerprint,
		llms.NormalizeProviderType(environment.ProviderType),
		protocol,
		strings.TrimSpace(environment.Endpoint),
		strings.TrimSpace(environment.APIKey),
		strings.TrimSpace(environment.AuthHeader),
		strings.TrimSpace(environment.AuthScheme),
		llms.ProviderScopeFacadeEnv,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("save llm facade environment: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count saved llm facade environment: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("save llm facade environment: provider id is already owned")
	}
	return nil
}

// GetLLMFacadeEnvironment loads only token-owned Provider Env rows. These rows
// are disabled and never participate in configured provider selection.
func (s *llmStore) GetLLMFacadeEnvironment(ctx context.Context, providerID string) (llms.FacadeEnvironment, bool, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return llms.FacadeEnvironment{}, false, nil
	}
	var environment llms.FacadeEnvironment
	err := s.db.QueryRowContext(ctx, `SELECT id, provider_type, base_url, default_wire_api, api_key, auth_header, auth_scheme
		FROM llm_provider WHERE id = ? AND scope = ?`, providerID, llms.ProviderScopeFacadeEnv).Scan(
		&environment.ProviderID,
		&environment.ProviderType,
		&environment.Endpoint,
		&environment.Protocol,
		&environment.APIKey,
		&environment.AuthHeader,
		&environment.AuthScheme,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return llms.FacadeEnvironment{}, false, nil
	}
	if err != nil {
		return llms.FacadeEnvironment{}, false, fmt.Errorf("get llm facade environment: %w", err)
	}
	environment.ProviderType = llms.NormalizeProviderType(environment.ProviderType)
	if strings.TrimSpace(environment.Protocol) != "" {
		environment.Protocol = llms.NormalizeWireAPI(environment.Protocol)
	}
	return environment, true, nil
}

// DeleteLLMFacadeToken removes a single facade token by its raw value. It is used
// to retire a per-run agent token as soon as that run completes, so live tokens
// never accumulate over the lifetime of a long-running session.
func (s *llmStore) DeleteLLMFacadeToken(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	hash, _ := llms.HashFacadeToken(rawToken)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete llm facade token tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var providerID string
	err = tx.QueryRowContext(ctx, `SELECT provider_id FROM llm_facade_token WHERE token_hash = ?`, hash).Scan(&providerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolve llm facade token provider: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_facade_token WHERE token_hash = ?`, hash); err != nil {
		return fmt.Errorf("delete llm facade token: %w", err)
	}
	if providerID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM llm_provider WHERE id = ? AND scope = ?`, providerID, llms.ProviderScopeFacadeEnv); err != nil {
			return fmt.Errorf("delete llm facade environment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete llm facade token: %w", err)
	}
	return nil
}

func (s *llmStore) GetLLMFacadeToken(ctx context.Context, rawToken string) (llms.FacadeToken, error) {
	hash, fingerprint := llms.HashFacadeToken(rawToken)
	row := s.db.QueryRowContext(ctx, `SELECT sandbox_id, token_hash, token_fingerprint, model, provider_id, wire_api, source, run_id, issued_at, expires_at, revoked_at FROM llm_facade_token WHERE token_hash = ?`, hash)
	token, err := llms.ScanFacadeToken(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return llms.FacadeToken{}, domain.ResourceError(domain.ErrNotFound, "llm facade token", fingerprint, fmt.Sprintf("llm facade token %s not found", fingerprint), err)
		}
		return llms.FacadeToken{}, err
	}
	return token, nil
}

// llmFacadeTokenRetention is how long a revoked facade token row is kept before
// it is physically pruned. The grace window keeps recently-revoked tokens around
// for debugging while bounding table growth from completed sessions.
const llmFacadeTokenRetention = time.Hour

const LLMFacadeTokenRetention = llmFacadeTokenRetention

func (s *llmStore) RevokeLLMFacadeTokensForSandbox(ctx context.Context, sandboxID string) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke llm facade tokens tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	sandboxID = strings.TrimSpace(sandboxID)
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_provider
		WHERE scope = ? AND id IN (SELECT provider_id FROM llm_facade_token WHERE sandbox_id = ?)`, llms.ProviderScopeFacadeEnv, sandboxID); err != nil {
		return fmt.Errorf("delete revoked llm facade environments: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE llm_facade_token SET revoked_at = ? WHERE sandbox_id = ? AND revoked_at = 0`, now.Unix(), sandboxID); err != nil {
		return fmt.Errorf("revoke llm facade tokens for sandbox: %w", err)
	}
	// Opportunistically prune long-dead rows (revoked beyond the retention grace,
	// or expired) so the table stays bounded across sessions. Both states already
	// fail closed at the handler, so deleting them changes nothing observable.
	cutoff := now.Add(-llmFacadeTokenRetention).Unix()
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_provider
		WHERE scope = ? AND id IN (
			SELECT provider_id FROM llm_facade_token
			WHERE (revoked_at != 0 AND revoked_at < ?) OR (expires_at != 0 AND expires_at < ?)
		)`, llms.ProviderScopeFacadeEnv, cutoff, now.Unix()); err != nil {
		return fmt.Errorf("prune llm facade environments: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_facade_token WHERE (revoked_at != 0 AND revoked_at < ?) OR (expires_at != 0 AND expires_at < ?)`, cutoff, now.Unix()); err != nil {
		return fmt.Errorf("prune llm facade tokens: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit revoke llm facade tokens: %w", err)
	}
	return nil
}
