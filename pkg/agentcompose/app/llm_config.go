package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
)

// modelCatalogStore is the surface that projecting models.json needs.
type modelCatalogStore interface {
	ApplyModelCatalog(context.Context, llms.ModelCatalog) error
}

// llmConfigStore is the startup surface that materializing LLM configuration
// needs: the persisted connection catalog and the default connection slot.
type llmConfigStore interface {
	modelCatalogStore
	llms.DefaultConfigStore
}

// loadLLMConfig materializes every configured LLM connection into the store
// before any agent resolves one.
//
// The daemon has two ways to be told about an upstream connection: models.json
// on disk, and the LLM_API_* / ANTHROPIC_* environment. Both are projected here,
// once, so that afterwards the catalog is the single source every resolution
// reads — a request path never writes configuration, and a connection cannot
// change while the process is running.
func loadLLMConfig(ctx context.Context, config *appconfig.Config, store llmConfigStore) error {
	if config == nil || store == nil {
		return fmt.Errorf("llm configuration and store are required")
	}
	if err := loadModelCatalog(ctx, config, store); err != nil {
		return err
	}
	if err := llms.ProjectDaemonLLMConfig(ctx, config, store); err != nil {
		return fmt.Errorf("project daemon llm environment: %w", err)
	}
	return nil
}

func loadModelCatalog(ctx context.Context, config *appconfig.Config, store modelCatalogStore) error {
	if config == nil || store == nil {
		return fmt.Errorf("model catalog configuration and store are required")
	}
	path := filepath.Join(config.DataRoot, llms.ModelsCatalogFilename)
	catalog, err := llms.LoadModelCatalog(path, os.LookupEnv)
	if err != nil {
		return err
	}
	if err := store.ApplyModelCatalog(ctx, catalog); err != nil {
		return fmt.Errorf("apply model catalog %s: %w", path, err)
	}
	return nil
}
