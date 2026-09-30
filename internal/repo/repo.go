// Package repo is the data access layer. Every function takes a DB so it works on the pool or inside a transaction.
package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Audit records one write (spec §9). before/after are marshalled to JSONB; nil stays NULL.
func Audit(ctx context.Context, db DB, entity string, entityID any, action, actor string, before, after any) error {
	b, err := toJSON(before)
	if err != nil {
		return err
	}
	a, err := toJSON(after)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO audit_logs (entity, entity_id, action, actor, before, after) VALUES ($1, $2, $3, $4, $5, $6)`,
		entity, fmt.Sprint(entityID), action, actor, b, a)
	return err
}

func toJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
