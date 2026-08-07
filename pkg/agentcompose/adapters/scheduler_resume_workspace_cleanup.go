package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

const (
	resumeCleanupGitIndexLockEnv = "AGENT_COMPOSE_RESUME_CLEANUP_GIT_INDEX_LOCK"
	resumeWorkspaceCleanupEvent  = "sandbox.workspace_cleanup"
)

// cleanupResumeWorkspace applies the one explicitly supported pre-resume
// repair. The caller holds the sandbox lifecycle lock and has already made the
// workspace ready; this method limits cleanup to the exact stopped state.
func (r *SchedulerSandboxRunner) cleanupResumeWorkspace(ctx context.Context, session *domain.Sandbox) error {
	if session == nil {
		return fmt.Errorf("sandbox is required")
	}
	if session.Summary.VMStatus != domain.VMStatusStopped {
		return nil
	}
	enabled, err := r.resumeGitIndexLockCleanupEnabled(ctx, session)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}

	cleanup := r.resumeWorkspaceCleanup
	if cleanup == nil {
		cleanup = removeStaleGitIndexLock
	}
	removed, err := cleanup(session.Summary.WorkspacePath)
	if err != nil {
		return fmt.Errorf("clean scheduler resume workspace: %w", err)
	}
	if !removed {
		return nil
	}

	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      resumeWorkspaceCleanupEvent,
		Level:     "info",
		Message:   "removed stale Git index lock before sandbox resume",
		CreatedAt: time.Now().UTC(),
	}
	if err := r.Store.AddEvent(ctx, session.Summary.ID, event); err == nil && r.Streams != nil {
		r.Streams.PublishEventAdded(session.Summary.ID, event)
	}
	return nil
}

func (r *SchedulerSandboxRunner) resumeGitIndexLockCleanupEnabled(ctx context.Context, session *domain.Sandbox) (bool, error) {
	if session == nil {
		return false, fmt.Errorf("sandbox is required")
	}
	agentID := strings.TrimSpace(execution.SessionTagValue(session.Summary.Tags, domain.AgentSandboxTagID))
	if agentID == "" || !domain.SandboxHasAgentTag(session, agentID) {
		return false, nil
	}
	if r.ConfigDB == nil {
		return false, fmt.Errorf("resolve resume cleanup agent definition %s: config store is required", agentID)
	}
	definition, err := r.ConfigDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return false, fmt.Errorf("resolve resume cleanup agent definition %s: %w", agentID, err)
	}
	if !definition.Enabled {
		return false, nil
	}
	return domain.SandboxEnvMap(definition.EnvItems)[resumeCleanupGitIndexLockEnv] == "1", nil
}

func removeStaleGitIndexLock(workspacePath string) (bool, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return false, fmt.Errorf("workspace path is empty")
	}
	workspaceInfo, err := os.Lstat(workspacePath)
	if err != nil {
		return false, fmt.Errorf("inspect workspace directory: %w", err)
	}
	if !workspaceInfo.Mode().IsDir() {
		return false, fmt.Errorf("workspace path is not a real directory")
	}

	gitPath := filepath.Join(workspacePath, ".git")
	gitInfo, err := os.Lstat(gitPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Git metadata directory: %w", err)
	}
	if !gitInfo.Mode().IsDir() {
		return false, fmt.Errorf("Git metadata path is not a real directory")
	}

	lockPath := filepath.Join(gitPath, "index.lock")
	info, err := os.Lstat(lockPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Git index lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("Git index lock is not a regular file")
	}
	if err := os.Remove(lockPath); err != nil {
		return false, fmt.Errorf("remove Git index lock: %w", err)
	}
	return true, nil
}
