package repo

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

func Stock(ctx context.Context, db DB, periodID int64) ([]models.StockRow, error) {
	rows, err := db.Query(ctx, `
		SELECT row_number() OVER (ORDER BY product_id)::int, product_id, name, is_active, opening, incoming, sold, current
		FROM stock_by_period WHERE period_id = $1 ORDER BY product_id`, periodID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.StockRow, error) {
		var s models.StockRow
		err := r.Scan(&s.STT, &s.ProductID, &s.Name, &s.Active, &s.Opening, &s.Incoming, &s.Sold, &s.Current)
		return s, err
	})
}

// Openings returns period id -> product id -> opening qty, keyed by string ids for JSON.
func Openings(ctx context.Context, db DB) (map[string]map[string]int, error) {
	rows, err := db.Query(ctx, `SELECT period_id, product_id, qty FROM stock_openings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var per, prod int64
		var qty int
		if err := rows.Scan(&per, &prod, &qty); err != nil {
			return nil, err
		}
		k := strconv.FormatInt(per, 10)
		if out[k] == nil {
			out[k] = map[string]int{}
		}
		out[k][strconv.FormatInt(prod, 10)] = qty
	}
	return out, rows.Err()
}

func UpsertOpening(ctx context.Context, db DB, periodID, productID int64, qty int) error {
	_, err := db.Exec(ctx, `
		INSERT INTO stock_openings (period_id, product_id, qty) VALUES ($1, $2, $3)
		ON CONFLICT (period_id, product_id) DO UPDATE SET qty = EXCLUDED.qty`, periodID, productID, qty)
	return err
}
