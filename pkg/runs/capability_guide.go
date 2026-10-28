package runs

import (
	"context"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/capabilities"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// trustedUsernameHeader is the trusted-ingress form of the user identity that
// a capability guide request forwards downstream as x-octobus-ext-username.
// It is best-effort: internal triggers have no caller identity and leave the
// scope empty, so the upstream gateway falls back to the project owner.
const trustedUsernameHeader = "x-mpi-username"

// CapabilityGuideDeps groups the dependencies WriteCapabilityGuide and
// recordCapabilityGuideWarning need to render a guide and report failures.
// Exported so drivers outside this package (pkg/agentcompose/adapters) can
// call WriteCapabilityGuide too, rather than keeping their own copy.
type CapabilityGuideDeps struct {
	Provider capabilities.Provider
	Store    SandboxRuntimeStore
	Streams  *sandboxes.StreamBroker
	// Config resolves the guest-side path the guide is pushed to. Only
	// needed when WriteGuestFile is set.
	Config *appconfig.Config
	// WriteGuestFile pushes the rendered guide into the sandbox for
	// runtimes with no shared filesystem (k8s - see
	// docs/design/k8s_pod_runtime_driver_design.md §2.1). nil for drivers
	// with a real mount (docker/boxlite/microsandbox), which see the host
	// write below for free.
	WriteGuestFile execution.GuestFileWriterFunc
}

func WriteCapabilityGuide(ctx context.Context, deps CapabilityGuideDeps, sandbox *domain.Sandbox, capsetIDs []string) {
	ids := capabilities.NormalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 || deps.Provider == nil || sandbox == nil {
		return
	}
	catalogPath := capabilities.SandboxGuidePath(sandbox)
	if catalogPath == "" {
		return
	}
	var b strings.Builder
	rendered := false
	scope := capabilities.GuideScopeFromSandbox(sandbox)
	scope.Username = domain.TrustedHeaderValue(ctx, trustedUsernameHeader)
	for _, id := range ids {
		guide, err := capabilities.CapabilityGuideForScope(ctx, deps.Provider, scope, id)
		if err != nil {
			slog.Warn("capability guide render skipped", "capset", id, "sandbox_id", sandbox.Summary.ID, "error", err)
			recordCapabilityGuideWarning(ctx, deps, sandbox.Summary.ID, fmt.Sprintf("capability guide render skipped for capset %s", id))
			continue
		}
		if rendered {
			b.WriteString("\n\n")
		}
		b.Write(guide)
		rendered = true
	}
	if !rendered {
		return
	}
	content := b.String()
	if preamble := capabilities.GuidePreamble(capabilities.ProxyTarget(deps.Provider)); preamble != "" {
		content = preamble + content
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		slog.Warn("capability guide dir create failed", "sandbox_id", sandbox.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, deps, sandbox.Summary.ID, "capability guide directory create failed")
		return
	}
	if err := os.WriteFile(catalogPath, []byte(content), 0o644); err != nil {
		slog.Warn("capability guide write failed", "sandbox_id", sandbox.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, deps, sandbox.Summary.ID, "capability guide write failed")
		return
	}
	if deps.WriteGuestFile != nil && deps.Config != nil {
		appconfig.ApplyDefaultGuestPaths(deps.Config)
		guestPath := filepath.Join(deps.Config.GuestRuntimeRoot, "mpi", "catalog.md")
		if err := deps.WriteGuestFile(ctx, guestPath, []byte(content)); err != nil {
			slog.Warn("capability guide guest push failed", "sandbox_id", sandbox.Summary.ID, "error", err)
			recordCapabilityGuideWarning(ctx, deps, sandbox.Summary.ID, "capability guide guest push failed")
		}
	}
}

func recordCapabilityGuideWarning(ctx context.Context, deps CapabilityGuideDeps, sandboxID, message string) {
	if deps.Store == nil || strings.TrimSpace(sandboxID) == "" {
		return
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      "capability.guide.warning",
		Level:     "warning",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	if err := deps.Store.AddEvent(ctx, sandboxID, event); err != nil {
		slog.Warn("capability guide warning event failed", "sandbox_id", sandboxID, "error", err)
		return
	}
	if deps.Streams != nil {
		deps.Streams.PublishEventAdded(sandboxID, event)
	}
}
