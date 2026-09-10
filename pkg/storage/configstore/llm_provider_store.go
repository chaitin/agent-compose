package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const providerColumns = `id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at`

// CreateLLMProvider creates an API-owned upstream provider without changing defaults.
func (s *llmStore) CreateLLMProvider(ctx context.Context, input llms.ProviderReplacement) (llms.Provider, error) {
	input, err := llms.NormalizeProviderReplacement(input)
	if err != nil {
		return llms.Provider{}, err
	}
	if input.APIKey == nil {
		return llms.Provider{}, fmt.Errorf("%w: api_key is required", domain.ErrInvalidArgument)
	}
	family, header, scheme := managedProviderAuth(input.Protocol)
	now := time.Now().UTC().Unix()
	row := s.db.QueryRowContext(ctx, `INSERT INTO llm_provider (`+providerColumns+`)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}', 0, 10, ?, ?, ?, ?)
 ON CONFLICT(id) DO NOTHING RETURNING `+providerColumns,
		input.ID, input.Name, family, input.Protocol, input.BaseURL, *input.APIKey, header, scheme, BoolToInt(input.Enabled), llms.ProviderScopeAPI, now, now)
	provider, err := llms.ScanProvider(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return llms.Provider{}, fmt.Errorf("%w: provider id already exists", domain.ErrAlreadyExists)
	}
	if err != nil {
		return llms.Provider{}, fmt.Errorf("create llm provider: %w", err)
	}
	return provider, nil
}

// GetManagedLLMProvider reads API-owned configuration, including disabled providers.
func (s *llmStore) GetManagedLLMProvider(ctx context.Context, id string) (llms.Provider, error) {
	if err := llms.ValidateManagedProviderID(id); err != nil {
		return llms.Provider{}, err
	}
	provider, err := llms.ScanProvider(s.db.QueryRowContext(ctx, `SELECT `+providerColumns+` FROM llm_provider WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return llms.Provider{}, fmt.Errorf("%w: llm provider not found", domain.ErrNotFound)
	}
	if err != nil {
		return llms.Provider{}, fmt.Errorf("get llm provider: %w", err)
	}
	if provider.Scope != llms.ProviderScopeAPI {
		return llms.Provider{}, fmt.Errorf("%w: provider is not API-managed", domain.ErrFailedPrecondition)
	}
	return provider, nil
}

// ListManagedLLMProviders lists API-owned providers in stable ID order.
func (s *llmStore) ListManagedLLMProviders(ctx context.Context) ([]llms.Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerColumns+` FROM llm_provider WHERE scope = ? ORDER BY id`, llms.ProviderScopeAPI)
	if err != nil {
		return nil, fmt.Errorf("list managed llm providers: %w", err)
	}
	// Read-only rows: iteration errors are reported by rows.Err below.
	defer func() { _ = rows.Close() }()
	providers := make([]llms.Provider, 0)
	for rows.Next() {
		provider, err := llms.ScanProvider(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan managed llm provider: %w", err)
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed llm providers: %w", err)
	}
	return providers, nil
}

// UpdateLLMProvider atomically replaces configuration and optionally rotates its key.
func (s *llmStore) UpdateLLMProvider(ctx context.Context, input llms.ProviderReplacement) (llms.Provider, error) {
	input, err := llms.NormalizeProviderReplacement(input)
	if err != nil {
		return llms.Provider{}, err
	}
	family, header, scheme := managedProviderAuth(input.Protocol)
	row := s.db.QueryRowContext(ctx, `UPDATE llm_provider SET name = ?, provider_type = ?, default_wire_api = ?, base_url = ?, api_key = COALESCE(?, api_key), auth_header = ?, auth_scheme = ?, enabled = ?, updated_at = ?
 WHERE id = ? AND scope = ? RETURNING `+providerColumns,
		input.Name, family, input.Protocol, input.BaseURL, input.APIKey, header, scheme, BoolToInt(input.Enabled), time.Now().UTC().Unix(), input.ID, llms.ProviderScopeAPI)
	provider, err := llms.ScanProvider(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.GetManagedLLMProvider(ctx, input.ID)
		if err == nil {
			err = fmt.Errorf("%w: provider changed during update", domain.ErrConflict)
		}
		return llms.Provider{}, err
	}
	if err != nil {
		return llms.Provider{}, fmt.Errorf("update llm provider: %w", err)
	}
	return provider, nil
}

// DeleteLLMProvider removes an API provider and its tokens atomically. Removing
// tokens ensures reusing an ID cannot revive credentials issued for the old provider.
func (s *llmStore) DeleteLLMProvider(ctx context.Context, id string) error {
	if err := llms.ValidateManagedProviderID(id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider deletion: %w", err)
	}
	// Rollback after commit is harmless; on errors it releases the transaction.
	defer func() { _ = tx.Rollback() }()
	var scope string
	err = tx.QueryRowContext(ctx, `SELECT scope FROM llm_provider WHERE id = ?`, id).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: llm provider not found", domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read provider ownership: %w", err)
	}
	if scope != llms.ProviderScopeAPI {
		return fmt.Errorf("%w: provider is not API-managed", domain.ErrFailedPrecondition)
	}
	for _, statement := range []string{
		`DELETE FROM llm_facade_token WHERE provider_id = ?`,
		`DELETE FROM llm_provider_model WHERE provider_id = ?`,
		`DELETE FROM llm_provider WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return fmt.Errorf("delete llm provider configuration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider deletion: %w", err)
	}
	return nil
}

func managedProviderAuth(protocol string) (family, header, scheme string) {
	if protocol == llms.APIProtocolMessages {
		return llms.ProviderFamilyAnthropic, "x-api-key", ""
	}
	return llms.ProviderFamilyOpenAI, "Authorization", "Bearer"
}
