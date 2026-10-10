package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
)

// modelCatalogStore is the surface that projecting models.json needs.
type modelCatalogStore interface {
	ApplyModelCatalog(context.Context, llms.ModelCatalog) error
}

// llmConfigStore is the startup surface that materializing LLM configuration
// needs: the write surfaces for models.json and the daemon environment, plus the
// catalog reads that report whether a default model exists afterwards.
type llmConfigStore interface {
	modelCatalogStore
	llms.DefaultConfigStore
	llms.CatalogStore
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
		conflict, ok := llms.AsDefaultConfigConflict(err)
		if !ok {
			return fmt.Errorf("project daemon llm environment: %w", err)
		}
		// The environment is a fallback, so another source naming the same id
		// wins. Report it, but never fail startup over it.
		slog.Warn("daemon llm environment was not applied because another configuration source owns the id",
			"kind", conflict.Kind, "id", conflict.ID, "owner_scope", conflict.Scope)
	}
	missing, err := defaultModelMissing(ctx, store)
	if err != nil {
		return err
	}
	if missing {
		slog.Warn("no default llm model is configured; agents that declare no model keep their own authentication. Configure a model in models.json or through the LLM RPC/UI")
	}
	return nil
}

// defaultModelMissing reports whether a run that declares no model has nothing
// to fall back to. It is deliberately non-blocking: models can also be
// configured later through the LLM RPC, and an agent that declares its own
// upstream is unaffected.
func defaultModelMissing(ctx context.Context, store llms.CatalogStore) (bool, error) {
	catalog, err := llms.LoadCatalog(ctx, store)
	if err != nil {
		return false, fmt.Errorf("load llm catalog after projection: %w", err)
	}
	return strings.TrimSpace(catalog.DefaultModel()) == "", nil
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
