package repo

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

const productSelect = `
SELECT p.id, p.name, p.is_active, p.rakuma_keywords,
       (SELECT COUNT(*) FROM purchases x WHERE x.product_id = p.id) + (SELECT COUNT(*) FROM sales y WHERE y.product_id = p.id)
FROM products p`

func scanProduct(row pgx.Row) (models.Product, error) {
	var p models.Product
	err := row.Scan(&p.ID, &p.Name, &p.Active, &p.Keywords, &p.TxCount)
	return p, err
}

func ListProducts(ctx context.Context, db DB) ([]models.Product, error) {
	rows, err := db.Query(ctx, productSelect+` ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.Product, error) { return scanProduct(r) })
}

func GetProduct(ctx context.Context, db DB, id int64) (models.Product, error) {
	return scanProduct(db.QueryRow(ctx, productSelect+` WHERE p.id = $1`, id))
}

// ProductNameTaken checks BR-01 (unique, ignoring leading/trailing spaces).
func ProductNameTaken(ctx context.Context, db DB, name string, exceptID int64) (bool, error) {
	var taken bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM products WHERE btrim(name) = btrim($1) AND id <> $2)`, name, exceptID).Scan(&taken)
	return taken, err
}

func InsertProduct(ctx context.Context, db DB, name string) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO products (name) VALUES (btrim($1)) RETURNING id`, name).Scan(&id)
	return id, err
}

func UpdateProduct(ctx context.Context, db DB, id int64, name string, active bool, keywords string) error {
	_, err := db.Exec(ctx, `UPDATE products SET name = btrim($2), is_active = $3, rakuma_keywords = $4 WHERE id = $1`, id, name, active, keywords)
	return err
}

func DeleteProduct(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	return err
}
