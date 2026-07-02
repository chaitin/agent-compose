package agentcompose

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	domaincap "agent-compose/internal/agentcompose/capability"
	"agent-compose/internal/capproxy"
)

const (
	capProxyTargetEnvName         = domaincap.ProxyTargetEnvName
	capabilitySessionTokenEnvName = domaincap.SessionTokenEnvName
	// capabilityCapsetTagName is the session tag carrying an allowed capset id.
	// A session bound to multiple capsets gets one tag per id; capproxy reads
	// these (server-side) to validate the guest's requested capset.
	capabilityCapsetTagName = domaincap.CapsetTagName
)

// normalizeCapsetIDs trims, drops empties, and de-duplicates capset ids while
// preserving order.
func normalizeCapsetIDs(ids []string) []string {
	return domaincap.NormalizeCapsetIDs(ids)
}

// buildCapabilityGatewaySessionVars produces the capability env items and tags
// for a session bound to a set of capsets. It is pure and never contacts
// OctoBus, so capability setup can never block session/loader creation. The env
// carries only what the guest needs (proxy target + session token); the allowed
// capset set is recorded as session tags (read server-side by capproxy). No
// capsets means "no capability"; a missing proxy target skips injection.
func buildCapabilityGatewaySessionVars(publicTarget string, capsetIDs []string) ([]SessionEnvVar, []SessionTag) {
	envItems, tagItems := domaincap.BuildGatewaySessionBinding(publicTarget, capsetIDs, uuid.NewString)
	env := make([]SessionEnvVar, 0, len(envItems))
	for _, item := range envItems {
		env = append(env, SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	tags := make([]SessionTag, 0, len(tagItems))
	for _, tag := range tagItems {
		tags = append(tags, SessionTag{Name: tag.Name, Value: tag.Value})
	}
	return env, tags
}

// writeCapabilityGuide renders the guide for each bound capset from OctoBus and
// writes the concatenation as the session's MPI catalog (guest
// /data/runtime/mpi/catalog.md), which agent-compose-runtime-js injects into the agent
// system prompt (codex developer_instructions, claude systemPrompt append). It
// is best-effort: failures are logged and recorded as warning events, but never
// block session/loader startup. Must be called after the session directory
// exists and before the runtime mounts it.
func writeCapabilityGuide(ctx context.Context, provider CapabilityProvider, store *Store, streams *SessionStreamBroker, session *Session, capsetIDs []string) {
	ids := normalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 || provider == nil || session == nil {
		return
	}
	catalogPath := sessionCapabilityGuidePath(session)
	if catalogPath == "" {
		return
	}
	var b strings.Builder
	rendered := false
	for _, id := range ids {
		guide, err := provider.CapabilityGuide(ctx, id)
		if err != nil {
			slog.Warn("capability guide render skipped", "capset", id, "session_id", session.Summary.ID, "error", err)
			recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, fmt.Sprintf("capability guide render skipped for capset %s", id))
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
	if preamble := capabilityGuidePreamble(capabilityGatewayProxyTarget(provider)); preamble != "" {
		content = preamble + content
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		slog.Warn("capability guide dir create failed", "session_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide directory create failed")
		return
	}
	if err := os.WriteFile(catalogPath, []byte(content), 0o644); err != nil {
		slog.Warn("capability guide write failed", "session_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide write failed")
	}
}

func recordCapabilityGuideWarning(ctx context.Context, store *Store, streams *SessionStreamBroker, sessionID, message string) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	event := SessionEvent{
		ID:        uuid.NewString(),
		Type:      domaincap.GuideWarningEventType,
		Level:     "warning",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.AddEvent(ctx, sessionID, event); err != nil {
		slog.Warn("capability guide warning event failed", "session_id", sessionID, "error", err)
		return
	}
	if streams != nil {
		streams.PublishEventAdded(sessionID, event)
	}
}

// capabilityGuidePreamble describes how the guest reaches the capability proxy:
// the gRPC endpoint (proxy target), the per-session auth metadata, and the
// per-method OctoBus routing metadata. It is prepended to the OctoBus-rendered
// catalog so the agent has both the connection details and the method table.
// Returns "" when no proxy target is configured (nothing to connect to).
func capabilityGuidePreamble(target string) string {
	return domaincap.GuidePreamble(target, capproxy.SessionTokenMetadata)
}

// sessionRuntimeDir is the local session runtime directory (sibling of the
// workspace dir under the session root). Returns "" when unknown.
func sessionRuntimeDir(session *Session) string {
	if session == nil {
		return ""
	}
	return domaincap.RuntimeDir(session.Summary.WorkspacePath)
}

// sessionCapabilityGuidePath is the session MPI catalog file the capability
// guide is written to (guest /data/runtime/mpi/catalog.md). Returns "" when the
// session runtime dir is unknown.
func sessionCapabilityGuidePath(session *Session) string {
	if session == nil {
		return ""
	}
	return domaincap.GuidePath(session.Summary.WorkspacePath)
}

func capabilityGatewayProxyTarget(provider CapabilityProvider) string {
	if provider == nil {
		return ""
	}
	return provider.ProxyTarget()
}
