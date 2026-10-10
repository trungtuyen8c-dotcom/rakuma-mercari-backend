package repo

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

// STT = row order inside its period (BR-02). Duplicate flags follow BR-05 / BR-06 (merged shipments are not flagged).
const purchaseSelect = `
SELECT * FROM (
  SELECT x.id, x.period_id, pe.label, row_number() OVER (PARTITION BY x.period_id ORDER BY x.id)::int,
         x.source, COALESCE(to_char(x.order_date, 'YYYY-MM-DD'), ''), COALESCE(x.item_url, ''),
         COALESCE(x.product_id, 0), COALESCE(pr.name, ''), x.unit_price, x.quantity, x.unit_discount, x.total,
         COALESCE(x.tracking_no, ''), x.merge_group IS NOT NULL, x.is_checked, x.is_reviewed, x.note,
         x.item_url IS NOT NULL AND COUNT(*) OVER (PARTITION BY x.item_url) > 1,
         x.tracking_no IS NOT NULL AND COUNT(*) OVER (PARTITION BY x.tracking_no) > 1
           AND NOT bool_and(x.merge_group IS NOT NULL) OVER (PARTITION BY x.tracking_no),
         pe.status = 'CLOSED'
  FROM purchases x
  JOIN periods pe ON pe.id = x.period_id
  LEFT JOIN products pr ON pr.id = x.product_id
) q`

func scanPurchase(row pgx.Row) (models.Purchase, error) {
	var p models.Purchase
	err := row.Scan(&p.ID, &p.PeriodID, &p.PeriodLabel, &p.STT, &p.Source, &p.Date, &p.Link, &p.ProductID, &p.ProductName,
		&p.Price, &p.Qty, &p.Discount, &p.Total, &p.Tracking, &p.Merged, &p.Checked, &p.Reviewed, &p.Note,
		&p.DupLink, &p.DupTracking, &p.Locked)
	return p, err
}

// ListPurchases returns rows ordered by id; periodID 0 means all periods.
func ListPurchases(ctx context.Context, db DB, periodID int64) ([]models.Purchase, error) {
	rows, err := db.Query(ctx, purchaseSelect+` WHERE ($1 = 0 OR period_id = $1) ORDER BY id`, periodID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.Purchase, error) { return scanPurchase(r) })
}

func GetPurchase(ctx context.Context, db DB, id int64) (models.Purchase, error) {
	return scanPurchase(db.QueryRow(ctx, purchaseSelect+` WHERE id = $1`, id))
}

func PurchasesByLink(ctx context.Context, db DB, link string) ([]models.Purchase, error) {
	rows, err := db.Query(ctx, purchaseSelect+` WHERE id IN (SELECT id FROM purchases WHERE item_url = $1) ORDER BY id`, link)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.Purchase, error) { return scanPurchase(r) })
}

func PurchasesByTracking(ctx context.Context, db DB, tracking string) ([]models.Purchase, error) {
	rows, err := db.Query(ctx, purchaseSelect+` WHERE id IN (SELECT id FROM purchases WHERE tracking_no = $1) ORDER BY id`, tracking)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.Purchase, error) { return scanPurchase(r) })
}

type PurchaseRow struct {
	PeriodID   int64
	Source     string
	Date       *string
	Link       *string
	ProductID  int64
	Price      int64
	Qty        int
	Discount   int64
	Tracking   *string
	MergeGroup *string
	Checked    bool
	Reviewed   bool
	Note       string
}

func InsertPurchase(ctx context.Context, db DB, r PurchaseRow) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO purchases (period_id, source, order_date, item_url, product_id, unit_price, quantity, unit_discount,
		                       tracking_no, merge_group, is_checked, is_reviewed, note)
		VALUES ($1, $2, $3, $4, NULLIF($5, 0), $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
		r.PeriodID, r.Source, r.Date, r.Link, r.ProductID, r.Price, r.Qty, r.Discount, r.Tracking, r.MergeGroup, r.Checked, r.Reviewed, r.Note).Scan(&id)
	return id, err
}

func PurchaseMergeGroup(ctx context.Context, db DB, id int64) (string, error) {
	var g *string
	err := db.QueryRow(ctx, `SELECT merge_group FROM purchases WHERE id = $1`, id).Scan(&g)
	if g == nil {
		return "", err
	}
	return *g, err
}

func UpdatePurchase(ctx context.Context, db DB, id int64, r PurchaseRow) error {
	_, err := db.Exec(ctx, `
		UPDATE purchases SET period_id = $2, source = $3, order_date = $4, item_url = $5, product_id = NULLIF($6, 0), unit_price = $7,
		       quantity = $8, unit_discount = $9, tracking_no = $10, merge_group = $11, is_checked = $12, is_reviewed = $13,
		       note = $14, updated_at = now()
		WHERE id = $1`,
		id, r.PeriodID, r.Source, r.Date, r.Link, r.ProductID, r.Price, r.Qty, r.Discount, r.Tracking, r.MergeGroup, r.Checked, r.Reviewed, r.Note)
	return err
}

func SetPurchaseFlags(ctx context.Context, db DB, id int64, checked, reviewed bool) error {
	_, err := db.Exec(ctx, `UPDATE purchases SET is_checked = $2, is_reviewed = $3, updated_at = now() WHERE id = $1`, id, checked, reviewed)
	return err
}

func DeletePurchase(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `DELETE FROM purchases WHERE id = $1`, id)
	return err
}

const saleSelect = `
SELECT * FROM (
  SELECT s.id, s.period_id, pe.label, row_number() OVER (PARTITION BY s.period_id ORDER BY s.id)::int,
         COALESCE(to_char(s.sale_date, 'YYYY-MM-DD'), ''), s.product_id, pr.name, s.quantity, s.unit_price,
         s.shipping_fee, s.total, s.customer_name, s.note, pe.status = 'CLOSED'
  FROM sales s
  JOIN periods pe ON pe.id = s.period_id
  JOIN products pr ON pr.id = s.product_id
) q`

func scanSale(row pgx.Row) (models.Sale, error) {
	var s models.Sale
	err := row.Scan(&s.ID, &s.PeriodID, &s.PeriodLabel, &s.STT, &s.Date, &s.ProductID, &s.ProductName, &s.Qty, &s.Price,
		&s.Ship, &s.Total, &s.Customer, &s.Note, &s.Locked)
	return s, err
}

func ListSales(ctx context.Context, db DB, periodID int64) ([]models.Sale, error) {
	rows, err := db.Query(ctx, saleSelect+` WHERE ($1 = 0 OR period_id = $1) ORDER BY id`, periodID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.Sale, error) { return scanSale(r) })
}

func GetSale(ctx context.Context, db DB, id int64) (models.Sale, error) {
	return scanSale(db.QueryRow(ctx, saleSelect+` WHERE id = $1`, id))
}

type SaleRow struct {
	PeriodID  int64
	Date      *string
	ProductID int64
	Qty       int
	Price     int64
	Ship      int64
	Customer  string
	Note      string
}

func InsertSale(ctx context.Context, db DB, r SaleRow) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO sales (period_id, sale_date, product_id, quantity, unit_price, shipping_fee, customer_name, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		r.PeriodID, r.Date, r.ProductID, r.Qty, r.Price, r.Ship, r.Customer, r.Note).Scan(&id)
	return id, err
}

func UpdateSale(ctx context.Context, db DB, id int64, r SaleRow) error {
	_, err := db.Exec(ctx, `
		UPDATE sales SET period_id = $2, sale_date = $3, product_id = $4, quantity = $5, unit_price = $6, shipping_fee = $7,
		       customer_name = $8, note = $9, updated_at = now()
		WHERE id = $1`,
		id, r.PeriodID, r.Date, r.ProductID, r.Qty, r.Price, r.Ship, r.Customer, r.Note)
	return err
}

func DeleteSale(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `DELETE FROM sales WHERE id = $1`, id)
	return err
}
