package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// ApplyModelCatalog atomically projects models.json into the runtime
// configuration store.
func (s *llmStore) ApplyModelCatalog(ctx context.Context, catalog llms.ModelCatalog) error {
	now := time.Now().UTC().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model catalog transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE llm_provider SET enabled = 0, updated_at = ? WHERE scope = ?`, now, llms.ProviderScopeCatalog); err != nil {
		return fmt.Errorf("disable stale model catalog providers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE llm_model SET enabled = 0, updated_at = ? WHERE scope = ?`, now, llms.ProviderScopeCatalog); err != nil {
		return fmt.Errorf("disable stale model catalog models: %w", err)
	}
	providerIDs := make([]string, 0, len(catalog.Providers))
	for id := range catalog.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		if err := applyCatalogProvider(ctx, tx, catalogProviderUpsert{
			ProviderID: providerID, Definition: catalog.Providers[providerID], Now: now,
		}); err != nil {
			return err
		}
	}
	if err := applyCatalogDefault(ctx, tx, catalog.Default, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model catalog transaction: %w", err)
	}
	return nil
}

// catalogProviderUpsert bundles one provider catalog entry and the apply timestamp.
type catalogProviderUpsert struct {
	ProviderID string
	Definition llms.CatalogProvider
	Now        int64
}

func applyCatalogProvider(ctx context.Context, tx *sql.Tx, upsert catalogProviderUpsert) error {
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
	result, err := tx.ExecContext(ctx, `INSERT INTO llm_provider(
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
		authHeader, authScheme, headersJSON, BoolToInt(enabled), llms.ProviderScopeCatalog, now, now, llms.ProviderScopeCatalog)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_provider_model WHERE provider_id = ?`, providerID); err != nil {
		return fmt.Errorf("replace provider %q model catalog: %w", providerID, err)
	}
	for _, model := range definition.Models {
		if err := applyCatalogModel(ctx, tx, catalogModelUpsert{
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

func applyCatalogModel(ctx context.Context, tx *sql.Tx, upsert catalogModelUpsert) error {
	providerID, providerProtocol, definition, now := upsert.ProviderID, upsert.ProviderProtocol, upsert.Definition, upsert.Now
	modelID := strings.TrimSpace(definition.ID)
	if err := ensureCatalogModelIdentity(ctx, tx, modelID, now); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider_model(
		provider_id, model_id, wire_api, weight, base_url, headers_json, max_output_tokens, display_name)
		VALUES(?, ?, ?, 10, ?, ?, ?, ?)`, providerID, modelID, protocol, strings.TrimSpace(pointerString(definition.BaseURL)), headersJSON, maxOutputTokens, pointerString(definition.Name)); err != nil {
		return fmt.Errorf("bind provider %q model %q: %w", providerID, modelID, err)
	}
	return nil
}

func ensureCatalogModelIdentity(ctx context.Context, tx *sql.Tx, modelID string, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, '', 0, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET enabled = 1, updated_at = excluded.updated_at
		WHERE llm_model.scope = ?`, modelID, modelID, llms.ProviderScopeCatalog, now, now, llms.ProviderScopeCatalog)
	return err
}

func applyCatalogDefault(ctx context.Context, tx *sql.Tx, reference string, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_catalog_default`); err != nil {
		return fmt.Errorf("clear model catalog default: %w", err)
	}
	providerID, modelID, ok := llms.SplitProviderModelReference(reference)
	if !ok {
		return nil
	}
	var protocol string
	if err := tx.QueryRowContext(ctx, `SELECT default_wire_api FROM llm_provider WHERE id = ?`, providerID).Scan(&protocol); err != nil {
		return fmt.Errorf("resolve model catalog default provider %q: %w", providerID, err)
	}
	if err := ensureCatalogModelIdentity(ctx, tx, modelID, now); err != nil {
		return fmt.Errorf("upsert model catalog default model: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_provider_model(
		provider_id, model_id, wire_api, weight, base_url, headers_json, max_output_tokens, display_name)
		VALUES(?, ?, ?, 10, '', '{}', 0, '')
		ON CONFLICT(provider_id, model_id) DO NOTHING`, providerID, modelID, protocol); err != nil {
		return fmt.Errorf("bind model catalog default: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_catalog_default(singleton, provider_id, model_id, updated_at) VALUES(1, ?, ?, ?)`, providerID, modelID, now); err != nil {
		return fmt.Errorf("store model catalog default: %w", err)
	}
	return nil
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

// DefaultLLMModelReference returns the exact provider/model default declared
// by models.json.
func (s *llmStore) DefaultLLMModelReference(ctx context.Context) (providerID, modelID string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT provider_id, model_id FROM llm_catalog_default WHERE singleton = 1`).Scan(&providerID, &modelID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("query model catalog default: %w", err)
	}
	return providerID, modelID, true, nil
}

// LLMProviderModelConfig returns all effective model-binding overrides.
func (s *llmStore) LLMProviderModelConfig(ctx context.Context, providerID, modelID string) (llms.ProviderModelConfig, bool, error) {
	var config llms.ProviderModelConfig
	err := s.db.QueryRowContext(ctx, `SELECT wire_api, base_url, headers_json, max_output_tokens, display_name
		FROM llm_provider_model WHERE provider_id = ? AND model_id = ?`, strings.TrimSpace(providerID), strings.TrimSpace(modelID)).
		Scan(&config.WireAPI, &config.BaseURL, &config.HeadersJSON, &config.MaxOutputTokens, &config.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return llms.ProviderModelConfig{}, false, nil
	}
	if err != nil {
		return llms.ProviderModelConfig{}, false, fmt.Errorf("query provider model config: %w", err)
	}
	// An unset per-model wire api stays unset. NormalizeWireAPI maps the empty
	// string onto responses, so normalizing unconditionally turned "this binding
	// declares no wire api" into "this binding declares responses", and the
	// target builder then preferred that over the provider's own
	// default_wire_api. A sandbox that publishes LLM_API_PROTOCOL=chat_completions
	// is persisted exactly that way, so the fabricated responses value silently
	// overrode the published protocol: the runtime proxy posted a
	// chat-completions model to /v1/responses and the gateway rejected the pair.
	if strings.TrimSpace(config.WireAPI) != "" {
		config.WireAPI = llms.NormalizeWireAPI(config.WireAPI)
	}
	return config, true, nil
}
