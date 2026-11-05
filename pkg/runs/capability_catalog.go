package runs

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func writeCapabilityCatalog(ctx context.Context, deps CapabilityGuideDeps, sandbox *domain.Sandbox, ids []string) {
	filePath := capabilities.SandboxCatalogPath(sandbox)
	if filePath == "" {
		return
	}
	scope := capabilities.GuideScopeFromSandbox(sandbox)
	scope.Username = domain.TrustedHeaderValue(ctx, trustedUsernameHeader)
	catalog := capabilities.BuildRuntimeCatalog(ctx, deps.Provider, scope, sandbox.Summary.ID, ids)
	content, err := json.Marshal(catalog)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(filePath), 0o755)
	}
	if err == nil {
		err = os.WriteFile(filePath, content, 0o644)
	}
	if err == nil && deps.WriteGuestFile != nil && deps.Config != nil {
		appconfig.ApplyDefaultGuestPaths(deps.Config)
		err = deps.WriteGuestFile(ctx, filepath.Join(deps.Config.GuestRuntimeRoot, "mpi", "capabilities.json"), content)
	}
	if err != nil {
		recordCapabilityGuideWarning(ctx, deps, sandbox.Summary.ID, "structured capability catalog write failed")
	} else if len(catalog.Unavailable) > 0 {
		// Preserve the existing guide event contract; SDK discovery reports the
		// structured snapshot's unavailable list instead of silently omitting tools.
		slog.Warn("structured capability catalog incomplete", "sandbox_id", sandbox.Summary.ID)
	}
}
