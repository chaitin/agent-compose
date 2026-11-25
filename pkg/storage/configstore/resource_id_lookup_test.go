package configstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/identity"
	"github.com/chaitin/agent-compose/pkg/resources"
)

func TestFindResourceIDsReturnsStoredPrefixCandidates(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := FromDB(db)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	prefix := "abcdef123456"
	projectID := prefix + strings.Repeat("1", 52)
	agentID := prefix + strings.Repeat("2", 52)
	runID := identity.Prefix + prefix + strings.Repeat("3", 52)
	if _, err := db.ExecContext(ctx, `INSERT INTO project(id, name) VALUES(?, ?)`, projectID, "demo"); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_agent(id, name, short_id, project_id, agent_name, created_at, updated_at) VALUES(?, ?, ?, ?, ?, 1, 1)`, agentID, "reviewer", prefix, projectID, "reviewer"); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_run(run_id, project_id, project_name, agent_name, agent_id, created_at, updated_at) VALUES(?, ?, ?, ?, ?, 1, 1)`, runID, projectID, "demo", "reviewer", agentID); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	targets, err := store.FindResourceIDs(ctx, resources.ResolveOptions{ID: prefix, Kinds: []resources.Kind{resources.KindProject, resources.KindAgent, resources.KindRun}})
	if err != nil {
		t.Fatalf("FindResourceIDs returned error: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("targets = %#v, want project, agent, and run", targets)
	}
	if targets[1].Kind != resources.KindAgent || targets[1].AgentName != "reviewer" || targets[1].ProjectID != projectID {
		t.Fatalf("agent target = %#v", targets[1])
	}
}
