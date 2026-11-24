// Package helper holds the small transaction and query helpers shared by the
// generated ent DAO's consumers. It contains no business logic.
package helper

import (
	"context"
	"fmt"

	"github.com/chaitin/agent-compose/ent/dao"
)

// WithTx runs fn inside one ent transaction. It rolls back on error or panic
// and commits only after fn returns nil, so callers never leak an open
// transaction on an early return.
func WithTx(ctx context.Context, client *dao.Client, fn func(tx *dao.Tx) error) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if v := recover(); v != nil {
			_ = tx.Rollback()
			panic(v)
		}
	}()
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("%w: rolling back transaction: %v", err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
