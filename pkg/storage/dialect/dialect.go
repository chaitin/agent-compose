// Package dialect holds the small set of SQL differences between the SQLite
// and PostgreSQL storage backends. Queries are otherwise shared.
package dialect

import entDialect "entgo.io/ent/dialect"

const (
	NameSQLite   = "sqlite"
	NamePostgres = "postgres"
)

// Dialect describes the SQL fragments a backend cannot share. The zero value
// is not valid; use SQLite or Postgres, or OrSQLite for an optional value.
type Dialect interface {
	Name() string
	// Contains returns an expression testing whether column contains the
	// single '?' parameter. Callers pass constants, never user input.
	Contains(column string) string
	// QuickCheckQuery returns a query whose single row/column is "ok" when the
	// database is healthy enough to serve the sandbox projection.
	QuickCheckQuery() string
	// BeginWriteStatement starts a transaction with SQLite's write
	// reservation, or a plain BEGIN on PostgreSQL.
	BeginWriteStatement() string
	// ForUpdateClause is appended to a SELECT that must lock its row.
	ForUpdateClause() string
	// LockEventSequenceStatement makes event sequence allocation and commit
	// order agree. SQLite's single writer already guarantees it.
	LockEventSequenceStatement() string
	// LockRunHistoryTablesStatement gives run-history removal the same
	// whole-writer-set exclusion SQLite gets from its write lock.
	LockRunHistoryTablesStatement() string
	// IsDuplicateRow reports whether err is the backend's unique or primary-key
	// constraint violation, so callers can map it to a resource conflict
	// without knowing which driver produced it.
	IsDuplicateRow(err error) bool
	// EntDialect names the backend for ent clients built over the shared
	// *sql.DB. ent calls SQLite "sqlite3", which is the driver name it uses,
	// not this package's dialect name.
	EntDialect() string
}

func OrSQLite(value Dialect) Dialect {
	if value == nil {
		return SQLite()
	}
	return value
}

type sqliteDialect struct{}

func SQLite() Dialect { return sqliteDialect{} }

func (sqliteDialect) Name() string { return NameSQLite }

func (sqliteDialect) EntDialect() string { return entDialect.SQLite }

func (sqliteDialect) Contains(column string) string {
	return "instr(" + column + ", ?) > 0"
}

func (sqliteDialect) QuickCheckQuery() string { return "PRAGMA quick_check(1)" }

func (sqliteDialect) BeginWriteStatement() string { return "BEGIN IMMEDIATE" }

func (sqliteDialect) ForUpdateClause() string { return "" }

func (sqliteDialect) LockEventSequenceStatement() string { return "" }

func (sqliteDialect) LockRunHistoryTablesStatement() string { return "" }

type postgresDialect struct{}

func Postgres() Dialect { return postgresDialect{} }

func (postgresDialect) Name() string { return NamePostgres }

func (postgresDialect) EntDialect() string { return entDialect.Postgres }

func (postgresDialect) Contains(column string) string {
	return "strpos(" + column + ", ?) > 0"
}

func (postgresDialect) QuickCheckQuery() string { return "SELECT 'ok'" }

func (postgresDialect) BeginWriteStatement() string { return "BEGIN" }

func (postgresDialect) ForUpdateClause() string { return " FOR UPDATE" }

func (postgresDialect) LockEventSequenceStatement() string {
	return "LOCK TABLE event IN SHARE ROW EXCLUSIVE MODE"
}

func (postgresDialect) LockRunHistoryTablesStatement() string {
	return "LOCK TABLE project, project_run, project_run_event, project_run_completion, " +
		"project_scheduler, scheduler_run, scheduler_event, scheduler_sandbox_binding, " +
		"event_sandbox_link IN SHARE ROW EXCLUSIVE MODE"
}
