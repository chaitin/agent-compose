package dialect

import "errors"

// SQLite reports constraint violations through extended result codes. Only the
// two below mean "a row with this key already exists"; the other
// SQLITE_CONSTRAINT codes (foreign key, not null, check) describe different
// failures and must not be reported as a duplicate.
//
// The values are part of SQLite's stable public interface. They are spelled out
// here rather than imported from modernc.org/sqlite/lib, which is a very large
// generated package pulled in per GOARCH.
const (
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
)

// postgresUniqueViolation is SQLSTATE 23505 (unique_violation).
const postgresUniqueViolation = "23505"

// The driver errors are recognized through the small interface they implement
// rather than by importing SQLite and pgx here. The storage dialects describe
// SQL differences; importing a driver would make every consumer of this
// package depend on one backend's implementation. Both interfaces are stable
// parts of the respective drivers and survive error wrapping through errors.As.
type sqliteErrorCoder interface {
	Code() int
}

type postgresErrorState interface {
	SQLState() string
}

func (sqliteDialect) IsDuplicateRow(err error) bool {
	var coded sqliteErrorCoder
	if !errors.As(err, &coded) {
		return false
	}
	switch coded.Code() {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return true
	default:
		return false
	}
}

func (postgresDialect) IsDuplicateRow(err error) bool {
	var state postgresErrorState
	if !errors.As(err, &state) {
		return false
	}
	return state.SQLState() == postgresUniqueViolation
}
