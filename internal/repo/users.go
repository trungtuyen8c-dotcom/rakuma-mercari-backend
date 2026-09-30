package repo

import "context"

func PasswordHash(ctx context.Context, db DB, email string) (string, error) {
	var h string
	err := db.QueryRow(ctx, `SELECT password_hash FROM users WHERE lower(email) = lower($1)`, email).Scan(&h)
	return h, err
}

func SetPasswordHash(ctx context.Context, db DB, email, hash string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO users (email, password_hash) VALUES (lower($1), $2)
		ON CONFLICT (lower(email)) DO UPDATE SET password_hash = EXCLUDED.password_hash, updated_at = now()`, email, hash)
	return err
}

func HasPassword(ctx context.Context, db DB, email string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower($1))`, email).Scan(&ok)
	return ok, err
}
