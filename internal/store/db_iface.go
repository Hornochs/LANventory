package store

import (
	"context"
	"database/sql"
)

// queryer is implemented by *sql.DB and *sql.Tx, so helpers work in and out
// of a transaction.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
