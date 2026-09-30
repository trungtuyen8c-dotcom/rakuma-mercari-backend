package repo

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

const periodSelect = `
SELECT id, label, to_char(start_date, 'YYYY-MM-DD'), to_char(end_date, 'YYYY-MM-DD'), status,
       opening_cost, opening_revenue, closing_cost, closing_revenue, to_char(closed_at, 'YYYY-MM-DD')
FROM periods`

func scanPeriod(row pgx.Row) (models.Period, error) {
	var p models.Period
	err := row.Scan(&p.ID, &p.Label, &p.Start, &p.End, &p.Status, &p.OpeningCost, &p.OpeningRevenue, &p.ClosingCost, &p.ClosingRevenue, &p.ClosedAt)
	return p, err
}

func ListPeriods(ctx context.Context, db DB) ([]models.Period, error) {
	rows, err := db.Query(ctx, periodSelect+` ORDER BY start_date`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.Period, error) { return scanPeriod(r) })
}

func GetPeriod(ctx context.Context, db DB, id int64) (models.Period, error) {
	return scanPeriod(db.QueryRow(ctx, periodSelect+` WHERE id = $1`, id))
}

// OpenPeriod returns the single OPEN period; forUpdate locks it for the rest of the transaction.
func OpenPeriod(ctx context.Context, db DB, forUpdate bool) (models.Period, error) {
	q := periodSelect + ` WHERE status = 'OPEN'`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanPeriod(db.QueryRow(ctx, q))
}

func InsertPeriod(ctx context.Context, db DB, label, start, end string, openingCost, openingRevenue int64) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO periods (label, start_date, end_date, status, opening_cost, opening_revenue)
		VALUES ($1, $2, $3, 'OPEN', $4, $5) RETURNING id`, label, start, end, openingCost, openingRevenue).Scan(&id)
	return id, err
}

func ClosePeriod(ctx context.Context, db DB, id, closingCost, closingRevenue int64) error {
	_, err := db.Exec(ctx, `UPDATE periods SET status = 'CLOSED', closing_cost = $2, closing_revenue = $3, closed_at = now() WHERE id = $1`,
		id, closingCost, closingRevenue)
	return err
}

func PeriodTotals(ctx context.Context, db DB, periodID int64) (models.Totals, error) {
	var t models.Totals
	err := db.QueryRow(ctx, `
		SELECT prev_cost, prev_revenue, prev_profit, period_cost, period_revenue, period_profit,
		       total_cost, total_revenue, total_profit, sales_count,
		       (SELECT COALESCE(SUM(current), 0) FROM stock_by_period WHERE period_id = $1)
		FROM period_totals WHERE period_id = $1`, periodID).
		Scan(&t.PrevCost, &t.PrevRevenue, &t.PrevProfit, &t.PeriodCost, &t.PeriodRevenue, &t.PeriodProfit,
			&t.TotalCost, &t.TotalRevenue, &t.TotalProfit, &t.SalesCount, &t.StockTotal)
	return t, err
}
