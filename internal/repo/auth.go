package repo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

func InsertSession(ctx context.Context, db DB, idHash, email, name string, expires time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO sessions (id, email, name, expires_at) VALUES ($1, $2, $3, $4)`, idHash, email, name, expires)
	return err
}

func GetSession(ctx context.Context, db DB, idHash string) (models.User, error) {
	var u models.User
	err := db.QueryRow(ctx, `
		SELECT email, name, to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM sessions WHERE id = $1 AND expires_at > now()`, idHash).Scan(&u.Email, &u.Name, &u.SignedInAt)
	return u, err
}

func DeleteSession(ctx context.Context, db DB, idHash string) error {
	_, err := db.Exec(ctx, `DELETE FROM sessions WHERE id = $1 OR expires_at < now()`, idHash)
	return err
}

const apiKeySelect = `
SELECT id, name, scope, masked, to_char(created_at, 'YYYY-MM-DD'), to_char(last_used_at, 'YYYY-MM-DD'),
       revoked_at IS NOT NULL, to_char(revoked_at, 'YYYY-MM-DD')
FROM api_keys`

func scanAPIKey(row pgx.Row) (models.APIKey, error) {
	var k models.APIKey
	err := row.Scan(&k.ID, &k.Name, &k.Scope, &k.Masked, &k.CreatedAt, &k.LastUsed, &k.Revoked, &k.RevokedAt)
	return k, err
}

func ListAPIKeys(ctx context.Context, db DB) ([]models.APIKey, error) {
	rows, err := db.Query(ctx, apiKeySelect+` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.APIKey, error) { return scanAPIKey(r) })
}

func GetAPIKey(ctx context.Context, db DB, id int64) (models.APIKey, error) {
	return scanAPIKey(db.QueryRow(ctx, apiKeySelect+` WHERE id = $1`, id))
}

func ActiveAPIKeyNameTaken(ctx context.Context, db DB, name string) (bool, error) {
	var taken bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM api_keys WHERE name = $1 AND revoked_at IS NULL)`, name).Scan(&taken)
	return taken, err
}

func InsertAPIKey(ctx context.Context, db DB, name, scope, hash, masked string) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO api_keys (name, scope, key_hash, masked) VALUES ($1, $2, $3, $4) RETURNING id`,
		name, scope, hash, masked).Scan(&id)
	return id, err
}

func RevokeAPIKey(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

// UseAPIKey resolves an active key by hash and stamps last_used_at.
func UseAPIKey(ctx context.Context, db DB, hash string) (name, scope string, err error) {
	err = db.QueryRow(ctx, `
		UPDATE api_keys SET last_used_at = now() WHERE key_hash = $1 AND revoked_at IS NULL
		RETURNING name, scope`, hash).Scan(&name, &scope)
	return name, scope, err
}
