package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

// EnsureOpenPeriod creates the current calendar month as the open period on an empty database.
// The Excel import (cmd/import-excel) then sets the real opening figures.
func EnsureOpenPeriod(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM periods`).Scan(&n); err != nil || n > 0 {
		return err
	}
	label := now.Format("01/2006")
	start, end, err := MonthRange(label)
	if err != nil {
		return err
	}
	_, err = repo.InsertPeriod(ctx, pool, label, start, end, 0, 0)
	return err
}
