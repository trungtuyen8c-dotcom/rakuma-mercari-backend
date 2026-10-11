package repo

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

const cardSelect = `
SELECT c.id, c.name, c.currency, c.is_default, c.is_active, (SELECT COUNT(*) FROM purchases x WHERE x.card_id = c.id)
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
	c, err := scanCard(db.QueryRow(ctx, cardSelect+` WHERE c.is_default AND c.is_active`))
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

// CardLedger lists every purchase paid by card as a charge on its order date (rows without a date use the day they
// were entered, Japan time). from/to are YYYY-MM-DD and inclusive; empty means open-ended.
func CardLedger(ctx context.Context, db DB, from, to, card string) ([]models.LedgerRow, error) {
	rows, err := db.Query(ctx, `
		SELECT q.id, c.name, c.currency, to_char(q.day, 'YYYY-MM-DD'), q.total, q.label, q.stt, COALESCE(pr.name, ''), COALESCE(q.item_url, '')
		FROM (
		  SELECT x.id, x.card_id, x.product_id, x.total, x.item_url, pe.label,
		         COALESCE(x.order_date, (x.created_at AT TIME ZONE 'Asia/Tokyo')::date) AS day,
		         row_number() OVER (PARTITION BY x.period_id ORDER BY x.order_date NULLS LAST, x.id)::int AS stt
		  FROM purchases x JOIN periods pe ON pe.id = x.period_id
		) q
		JOIN cards c ON c.id = q.card_id
		LEFT JOIN products pr ON pr.id = q.product_id
		WHERE ($1 = '' OR q.day >= $1::date) AND ($2 = '' OR q.day <= $2::date) AND ($3 = '' OR lower(c.name) = lower(btrim($3)))
		ORDER BY q.day, q.id`, from, to, card)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.LedgerRow, error) {
		var (
			id, total            int64
			label, product, link string
			stt                  int
			l                    models.LedgerRow
		)
		if err := r.Scan(&id, &l.Wallet, &l.Currency, &l.Date, &total, &label, &stt, &product, &link); err != nil {
			return l, err
		}
		l.ID = "rakuma:purchase:" + strconv.FormatInt(id, 10)
		l.Project, l.Amount, l.Type, l.Category = "rakuma", -total, "out", "Mua hàng"
		l.Note = "Sổ kho Rakuma, kỳ " + label + " dòng " + strconv.Itoa(stt)
		if product != "" {
			l.Note += " · " + product
		}
		if link != "" {
			l.Note += " · " + link
		}
		return l, nil
	})
}
