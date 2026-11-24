package dialect

import "testing"

func TestOrSQLite(t *testing.T) {
	if got := OrSQLite(nil).Name(); got != NameSQLite {
		t.Fatalf("OrSQLite(nil).Name() = %q", got)
	}
	if got := OrSQLite(Postgres()).Name(); got != NamePostgres {
		t.Fatalf("OrSQLite(Postgres()).Name() = %q", got)
	}
}

func TestSQLiteFragments(t *testing.T) {
	d := SQLite()
	if got := d.Contains("lower(name)"); got != "instr(lower(name), ?) > 0" {
		t.Fatalf("Contains() = %q", got)
	}
	if d.QuickCheckQuery() != "PRAGMA quick_check(1)" {
		t.Fatalf("QuickCheckQuery() = %q", d.QuickCheckQuery())
	}
	if d.BeginWriteStatement() != "BEGIN IMMEDIATE" {
		t.Fatalf("BeginWriteStatement() = %q", d.BeginWriteStatement())
	}
	if d.ForUpdateClause() != "" || d.LockEventSequenceStatement() != "" || d.LockRunHistoryTablesStatement() != "" {
		t.Fatal("SQLite must not add PostgreSQL locking clauses")
	}
}

func TestPostgresFragments(t *testing.T) {
	d := Postgres()
	if got := d.Contains("lower(name)"); got != "strpos(lower(name), ?) > 0" {
		t.Fatalf("Contains() = %q", got)
	}
	if d.QuickCheckQuery() != "SELECT 'ok'" {
		t.Fatalf("QuickCheckQuery() = %q", d.QuickCheckQuery())
	}
	if d.BeginWriteStatement() != "BEGIN" {
		t.Fatalf("BeginWriteStatement() = %q", d.BeginWriteStatement())
	}
	if d.ForUpdateClause() != " FOR UPDATE" {
		t.Fatalf("ForUpdateClause() = %q", d.ForUpdateClause())
	}
	if d.LockEventSequenceStatement() == "" {
		t.Fatal("PostgreSQL must lock event sequence allocation")
	}
	if d.LockRunHistoryTablesStatement() == "" {
		t.Fatal("PostgreSQL must lock run history tables")
	}
}
