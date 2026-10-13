package repo

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

const cardSelect = `
SELECT c.id, c.name, c.currency, c.is_default, c.is_active,
       (SELECT COUNT(*) FROM purchases x WHERE x.card_id = c.id OR x.point_card_id = c.id) + (SELECT COUNT(*) FROM card_refunds r WHERE r.card_id = c.id)
FROM cards c`

func scanCard(row pgx.Row) (models.Card, error) {
	var c models.Card
	err := row.Scan(&c.ID, &c.Name, &c.Currency, &c.Default, &c.Active, &c.TxCount)
	return c, err
}

func ListCards(ctx context.Context, db DB) ([]models.Card, error) {
	rows, err := db.Query(ctx, cardSelect+` ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.Card, error) { return scanCard(r) })
}

func GetCard(ctx context.Context, db DB, id int64) (models.Card, error) {
	return scanCard(db.QueryRow(ctx, cardSelect+` WHERE c.id = $1`, id))
}

// DefaultCard returns the card given to purchases the Rakuma sync writes, or ok=false when none is set.
func DefaultCard(ctx context.Context, db DB) (models.Card, bool, error) {
	c, err := scanCard(db.QueryRow(ctx, cardSelect+` WHERE c.is_default AND c.is_active AND c.currency = 'JPY'`))
	if err == pgx.ErrNoRows {
		return c, false, nil
	}
	return c, err == nil, err
}

func CardNameTaken(ctx context.Context, db DB, name string, exceptID int64) (bool, error) {
	var taken bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cards WHERE lower(btrim(name)) = lower(btrim($1)) AND id <> $2)`, name, exceptID).Scan(&taken)
	return taken, err
}

func InsertCard(ctx context.Context, db DB, name, currency string) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO cards (name, currency) VALUES (btrim($1), $2) RETURNING id`, name, currency).Scan(&id)
	return id, err
}

func UpdateCard(ctx context.Context, db DB, c models.Card) error {
	if c.Default {
		if _, err := db.Exec(ctx, `UPDATE cards SET is_default = FALSE WHERE is_default AND id <> $1`, c.ID); err != nil {
			return err
		}
	}
	_, err := db.Exec(ctx, `UPDATE cards SET name = btrim($2), is_default = $3, is_active = $4 WHERE id = $1`, c.ID, c.Name, c.Default, c.Active)
	return err
}

func DeleteCard(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `DELETE FROM cards WHERE id = $1`, id)
	return err
}

// CardLedger lists the card lines of this project, oldest first: a charge ("Mua hàng") for the part of each purchase
// paid by card on its charge date, the points it used ("Tiêu point") on its point wallet, the charge of a purchase
// removed after a full refund, and each refund ("Hoàn tiền") on the day the money came back. from/to are YYYY-MM-DD
// and inclusive; empty means open-ended.
func CardLedger(ctx context.Context, db DB, from, to, card string) ([]models.LedgerRow, error) {
	rows, err := db.Query(ctx, `
		WITH purchase AS (
		    SELECT x.id, x.card_id, x.point_card_id, x.points, x.total, x.item_url, pe.label,
		           COALESCE(x.order_date, (x.created_at AT TIME ZONE 'Asia/Tokyo')::date) AS day,
		           'Sổ kho Rakuma, kỳ ' || pe.label || ' dòng ' ||
		             row_number() OVER (PARTITION BY x.period_id ORDER BY x.order_date NULLS LAST, x.id) ||
		             COALESCE(' · ' || pr.name, '') || COALESCE(' · ' || x.item_url, '') AS note
		    FROM purchases x JOIN periods pe ON pe.id = x.period_id LEFT JOIN products pr ON pr.id = x.product_id
		  ),
		  lines AS (
		  SELECT 'rakuma:purchase:' || q.id AS id, q.card_id, q.day, -(q.total - q.points) AS amount, 'out' AS type, 'Mua hàng' AS category,
		         q.note, q.id AS ord
		  FROM purchase q WHERE q.card_id IS NOT NULL AND q.total > q.points
		  UNION ALL
		  SELECT 'rakuma:purchase:' || q.id || ':point', q.point_card_id, q.day, -q.points, 'out', 'Tiêu point', q.note, q.id
		  FROM purchase q WHERE q.points > 0
		  UNION ALL
		  SELECT 'rakuma:refund:' || r.id || ':charge', r.card_id, r.charge_date, -r.charge_amount, 'out', 'Mua hàng',
		         'Sổ kho Rakuma (dòng đã xoá sau khi hoàn tiền) · ' || r.label, r.id
		  FROM card_refunds r WHERE r.purchase_id IS NULL
		  UNION ALL
		  SELECT 'rakuma:refund:' || r.id, r.card_id, r.refund_date, r.amount, 'in', 'Hoàn tiền',
		         'Sổ kho Rakuma, hoàn tiền · ' || r.label || CASE WHEN r.note <> '' THEN ' · ' || r.note ELSE '' END, r.id
		  FROM card_refunds r
		)
		SELECT l.id, c.name, c.currency, to_char(l.day, 'YYYY-MM-DD'), l.amount, l.type, l.category, l.note
		FROM lines l JOIN cards c ON c.id = l.card_id
		WHERE ($1 = '' OR l.day >= $1::date) AND ($2 = '' OR l.day <= $2::date) AND ($3 = '' OR lower(c.name) = lower(btrim($3)))
		ORDER BY l.day, l.category DESC, l.ord`, from, to, card)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.LedgerRow, error) {
		l := models.LedgerRow{Project: "rakuma"}
		err := r.Scan(&l.ID, &l.Wallet, &l.Currency, &l.Date, &l.Amount, &l.Type, &l.Category, &l.Note)
		return l, err
	})
}

const refundSelect = `
SELECT r.id, COALESCE(r.purchase_id::text, ''), r.card_id, c.name, to_char(r.charge_date, 'YYYY-MM-DD'), r.charge_amount,
       to_char(r.refund_date, 'YYYY-MM-DD'), r.amount, r.label, r.note
FROM card_refunds r JOIN cards c ON c.id = r.card_id`

func scanRefund(row pgx.Row) (models.CardRefund, error) {
	var x models.CardRefund
	err := row.Scan(&x.ID, &x.PurchaseID, &x.CardID, &x.CardName, &x.ChargeDate, &x.ChargeAmount, &x.RefundDate, &x.Amount, &x.Label, &x.Note)
	return x, err
}

func ListRefunds(ctx context.Context, db DB) ([]models.CardRefund, error) {
	rows, err := db.Query(ctx, refundSelect+` ORDER BY r.refund_date, r.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.CardRefund, error) { return scanRefund(r) })
}

func GetRefund(ctx context.Context, db DB, id int64) (models.CardRefund, error) {
	return scanRefund(db.QueryRow(ctx, refundSelect+` WHERE r.id = $1`, id))
}

func InsertRefund(ctx context.Context, db DB, x models.CardRefund, purchaseID int64) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO card_refunds (purchase_id, card_id, charge_date, charge_amount, refund_date, amount, label, note)
		VALUES (NULLIF($1, 0), $2, $3::date, $4, $5::date, $6, $7, $8) RETURNING id`,
		purchaseID, x.CardID, x.ChargeDate, x.ChargeAmount, x.RefundDate, x.Amount, x.Label, x.Note).Scan(&id)
	return id, err
}

func DeleteRefund(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `DELETE FROM card_refunds WHERE id = $1`, id)
	return err
}

func ListReconciliations(ctx context.Context, db DB) ([]models.CardReconciliation, error) {
	rows, err := db.Query(ctx, `SELECT card_id, to_char(month, 'YYYY-MM'), amount, to_char(created_at AT TIME ZONE 'Asia/Tokyo', 'YYYY-MM-DD HH24:MI')
		FROM card_reconciliations ORDER BY month, card_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.CardReconciliation, error) {
		var x models.CardReconciliation
		err := r.Scan(&x.CardID, &x.Month, &x.Amount, &x.At)
		return x, err
	})
}

// IsReconciled reports whether the card's month holding day (YYYY-MM-DD) was checked against the statement.
func IsReconciled(ctx context.Context, db DB, cardID int64, day string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM card_reconciliations WHERE card_id = $1 AND month = date_trunc('month', $2::date)::date)`, cardID, day).Scan(&ok)
	return ok, err
}

func UpsertReconciliation(ctx context.Context, db DB, cardID int64, month string, amount int64) error {
	_, err := db.Exec(ctx, `
		INSERT INTO card_reconciliations (card_id, month, amount) VALUES ($1, ($2 || '-01')::date, $3)
		ON CONFLICT (card_id, month) DO UPDATE SET amount = EXCLUDED.amount, created_at = now()`, cardID, month, amount)
	return err
}

func DeleteReconciliation(ctx context.Context, db DB, cardID int64, month string) error {
	_, err := db.Exec(ctx, `DELETE FROM card_reconciliations WHERE card_id = $1 AND month = ($2 || '-01')::date`, cardID, month)
	return err
}
