package sqlite

import (
	"context"
	"testing"
)

func TestIntegrationRunLogHealthMigrationPreservesV23Runs(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:23]); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO project(id,name,created_at,updated_at) VALUES('p','project',1,1)`,
		`INSERT INTO project_agent(id,project_id,agent_name,created_at,updated_at) VALUES('a','p','worker',1,1)`,
		`INSERT INTO project_run(run_id,project_id,agent_id,agent_name,status,output,agent_cell_id,log_format,created_at,updated_at) VALUES('r','p','a','worker','succeeded','answer','step-1','jsonl',1,1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyMigrationSet(ctx, db, chain); err != nil {
		t.Fatal(err)
	}
	var status, output, cell string
	var incomplete int
	if err := db.QueryRowContext(ctx, `SELECT status,output,agent_cell_id,log_incomplete FROM project_run WHERE run_id='r'`).Scan(&status, &output, &cell, &incomplete); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || output != "answer" || cell != "step-1" || incomplete != 0 {
		t.Fatalf("status=%q output=%q cell=%q incomplete=%d", status, output, cell, incomplete)
	}
}
