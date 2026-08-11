package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"agent-compose/pkg/llms"
)

func syncEnvDefaultProviderCredentials(ctx context.Context, tx *sql.Tx, resolved *llms.EnvDefaultProviderCredentials) error {
	if resolved == nil {
		return nil
	}
	now := time.Now().UTC().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE llm_provider SET api_key = ?, updated_at = ? WHERE provider_type = ? AND scope = ? AND api_key != ?`, resolved.OpenAIAPIKey, now, llms.ProviderFamilyOpenAI, llms.ProviderScopeEnvDefault, resolved.OpenAIAPIKey); err != nil {
		return fmt.Errorf("sync default OpenAI provider credentials: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE llm_provider SET api_key = ?, auth_header = ?, auth_scheme = ?, updated_at = ? WHERE provider_type = ? AND scope = ? AND (api_key != ? OR auth_header != ? OR auth_scheme != ?)`, resolved.AnthropicAPIKey, resolved.AnthropicAuthHeader, resolved.AnthropicAuthScheme, now, llms.ProviderFamilyAnthropic, llms.ProviderScopeEnvDefault, resolved.AnthropicAPIKey, resolved.AnthropicAuthHeader, resolved.AnthropicAuthScheme); err != nil {
		return fmt.Errorf("sync default Anthropic provider credentials: %w", err)
	}
	return nil
}
