package configstore

import (
	"database/sql"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

// newEntClient builds an ent client over the shared application connection.
// The client is a thin dialect-aware view of the caller-owned pool: it is never
// closed, and every store that needs typed queries shares it.
func newEntClient(db *sql.DB, d dialect.Dialect) *dao.Client {
	return dao.NewClient(dao.Driver(entsql.OpenDB(d.EntDialect(), db)))
}

// newEntTxClient binds a dao client to an in-flight SQL transaction. Paths that
// must keep an explicitly reserved connection (event sequence lock, project run
// event append, run-history fence) use it so typed ent builders and the few
// remaining SQL statements commit atomically on the same *sql.Tx.
func newEntTxClient(d dialect.Dialect, tx *sql.Tx) *dao.Client {
	return dao.NewClient(dao.Driver(entsql.NewDriver(d.EntDialect(), entsql.Conn{ExecQuerier: tx})))
}
