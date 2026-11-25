package sqlite

import (
	"context"
	"testing"
)

func TestAgentRunCellMigrationPreservesV22Runs(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:22]); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO project(id,name,created_at,updated_at) VALUES('p','project',1,1)`,
		`INSERT INTO project_agent(id,project_id,agent_name,created_at,updated_at) VALUES('a','p','worker',1,1)`,
		`INSERT INTO project_run(run_id,project_id,agent_id,agent_name,execution_id,detach,detach_kind,log_format,logs_path,created_at,updated_at)
		 VALUES('r','p','a','worker','exec-1',1,'agent','jsonl','/data/runs/r/run.log.jsonl',1,1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyMigrationSet(ctx, db, chain); err != nil {
		t.Fatal(err)
	}
	var cellID, executionID, format, path string
	var detach int
	if err := db.QueryRowContext(ctx, `SELECT agent_cell_id,execution_id,detach,log_format,logs_path FROM project_run WHERE run_id='r'`).Scan(&cellID, &executionID, &detach, &format, &path); err != nil {
		t.Fatal(err)
	}
	if cellID != "" || executionID != "exec-1" || detach != 1 || format != "jsonl" || path != "/data/runs/r/run.log.jsonl" {
		t.Fatalf("cell=%q execution=%q detach=%d format=%q path=%q", cellID, executionID, detach, format, path)
	}
}
