package database

import (
	"context"
	"database/sql"
)

type Outcome int

const (
	RolledBack Outcome = iota
	Committed
	Uncertain
)

type Transaction func(context.Context, func(*sql.Tx) error) Outcome

// Commit 的错误不能当作确定回滚；调用方必须冻结/重载安全状态。
func TransactionFor(db *sql.DB) Transaction {
	return func(ctx context.Context, operation func(*sql.Tx) error) Outcome {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return RolledBack
		}
		if err := operation(tx); err != nil {
			if err := tx.Rollback(); err != nil {
				return Uncertain
			}
			return RolledBack
		}
		if err := tx.Commit(); err != nil {
			return Uncertain
		}
		return Committed
	}
}
