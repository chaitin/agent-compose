package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// openDB opens a PostgreSQL-backed *sql.DB whose driver rebinds SQLite-style
// '?' placeholders. Keeping the rewrite in the driver, rather than in every
// store, lets both backends share one SQL source and leaves the SQLite path
// untouched.
func openDB(dsn string) (*sql.DB, error) {
	connector, err := newConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func newConnector(dsn string) (driver.Connector, error) {
	config, err := parsePostgresConfig(dsn)
	if err != nil {
		return nil, err
	}
	return newConnectorFromConfig(*config), nil
}

// parsePostgresConfig deliberately drops pgx's parse error text: it embeds the
// connection string, which may contain a password.
func parsePostgresConfig(dsn string) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL connection string: invalid URL or DSN")
	}
	return config, nil
}

func newConnectorFromConfig(config pgx.ConnConfig) driver.Connector {
	return &rebindConnector{base: stdlib.GetConnector(config)}
}

type rebindConnector struct {
	base driver.Connector
}

func (c *rebindConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &rebindConn{Conn: conn}, nil
}

func (c *rebindConnector) Driver() driver.Driver {
	return c.base.Driver()
}

// rebindConn translates '?' placeholders before forwarding to the underlying
// driver. database/sql routes Tx and Conn queries through the same driver
// connection, so transactions need no separate wrapper.
type rebindConn struct {
	driver.Conn
}

func (c *rebindConn) Prepare(query string) (driver.Stmt, error) {
	rewritten, err := rebindQuery(query)
	if err != nil {
		return nil, err
	}
	return c.Conn.Prepare(rewritten)
}

func (c *rebindConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	rewritten, err := rebindQuery(query)
	if err != nil {
		return nil, err
	}
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return preparer.PrepareContext(ctx, rewritten)
	}
	return c.Conn.Prepare(rewritten)
}

func (c *rebindConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	rewritten, err := rebindQuery(query)
	if err != nil {
		return nil, err
	}
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, rewritten, args)
	}
	return execWithPreparedStatement(ctx, c.Conn, rewritten, args)
}

func (c *rebindConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rewritten, err := rebindQuery(query)
	if err != nil {
		return nil, err
	}
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, rewritten, args)
	}
	return queryWithPreparedStatement(ctx, c.Conn, rewritten, args)
}

// Begin is kept for drivers that do not implement ConnBeginTx.
//
//nolint:staticcheck // database/sql still calls it for older drivers.
func (c *rebindConn) Begin() (driver.Tx, error) {
	return c.Conn.Begin()
}

func (c *rebindConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	if opts.Isolation != driver.IsolationLevel(sql.LevelDefault) || opts.ReadOnly {
		return nil, fmt.Errorf("driver does not support transaction options")
	}
	return c.Conn.Begin() //nolint:staticcheck // fallback for drivers without ConnBeginTx.
}

func (c *rebindConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *rebindConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *rebindConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (c *rebindConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func execWithPreparedStatement(ctx context.Context, conn driver.Conn, query string, args []driver.NamedValue) (driver.Result, error) {
	stmt, err := prepareStatement(ctx, conn, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stmt.Close() }()
	if execer, ok := stmt.(driver.StmtExecContext); ok {
		return execer.ExecContext(ctx, args)
	}
	values, err := namedValues(args)
	if err != nil {
		return nil, err
	}
	return stmt.Exec(values) //nolint:staticcheck // fallback for drivers without StmtExecContext.
}

func queryWithPreparedStatement(ctx context.Context, conn driver.Conn, query string, args []driver.NamedValue) (driver.Rows, error) {
	stmt, err := prepareStatement(ctx, conn, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stmt.Close() }()
	if queryer, ok := stmt.(driver.StmtQueryContext); ok {
		return queryer.QueryContext(ctx, args)
	}
	values, err := namedValues(args)
	if err != nil {
		return nil, err
	}
	return stmt.Query(values) //nolint:staticcheck // fallback for drivers without StmtQueryContext.
}

func prepareStatement(ctx context.Context, conn driver.Conn, query string) (driver.Stmt, error) {
	if preparer, ok := conn.(driver.ConnPrepareContext); ok {
		return preparer.PrepareContext(ctx, query)
	}
	return conn.Prepare(query)
}

func namedValues(args []driver.NamedValue) ([]driver.Value, error) {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		if arg.Name != "" {
			return nil, fmt.Errorf("named parameters are not supported")
		}
		values[i] = arg.Value
	}
	return values, nil
}

var (
	_ driver.Conn               = (*rebindConn)(nil)
	_ driver.ConnPrepareContext = (*rebindConn)(nil)
	_ driver.ExecerContext      = (*rebindConn)(nil)
	_ driver.QueryerContext     = (*rebindConn)(nil)
	_ driver.ConnBeginTx        = (*rebindConn)(nil)
	_ driver.Pinger             = (*rebindConn)(nil)
	_ driver.SessionResetter    = (*rebindConn)(nil)
	_ driver.Validator          = (*rebindConn)(nil)
	_ driver.NamedValueChecker  = (*rebindConn)(nil)
)
