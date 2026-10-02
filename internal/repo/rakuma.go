package repo

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

const rakumaSelect = `
SELECT o.id, o.order_no, o.item_url, o.title, o.status, COALESCE(to_char(o.order_date, 'YYYY-MM-DD'), ''),
       o.price, o.discount, o.carrier, COALESCE(o.tracking_no, ''), o.seller, o.summary, o.reply_draft,
       o.purchase_id::text, o.is_dismissed,
       (SELECT COUNT(*) FROM rakuma_messages m WHERE m.order_id = o.id AND m.sender = 'seller'
          AND (o.messages_handled_at IS NULL OR m.created_at > o.messages_handled_at))::int,
       to_char(o.synced_at AT TIME ZONE 'Asia/Tokyo', 'YYYY-MM-DD HH24:MI')
FROM rakuma_orders o`

func scanRakuma(row pgx.Row) (models.RakumaOrder, error) {
	var o models.RakumaOrder
	err := row.Scan(&o.ID, &o.OrderNo, &o.Link, &o.Title, &o.Status, &o.Date, &o.Price, &o.Discount, &o.Carrier,
		&o.Tracking, &o.Seller, &o.Summary, &o.ReplyDraft, &o.PurchaseID, &o.Dismissed, &o.NewMessages, &o.SyncedAt)
	return o, err
}

// ListRakumaOrders returns orders newest first, each with its messages in arrival order.
func ListRakumaOrders(ctx context.Context, db DB) ([]models.RakumaOrder, error) {
	rows, err := db.Query(ctx, rakumaSelect+` ORDER BY o.order_date DESC NULLS FIRST, o.id DESC`)
	if err != nil {
		return nil, err
	}
	orders, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.RakumaOrder, error) { return scanRakuma(r) })
	if err != nil {
		return nil, err
	}
	msgs, err := rakumaMessages(ctx, db, 0)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		orders[i].Messages = msgs[orders[i].ID]
		if orders[i].Messages == nil {
			orders[i].Messages = []models.RakumaMessage{}
		}
	}
	return orders, nil
}

func GetRakumaOrder(ctx context.Context, db DB, id int64) (models.RakumaOrder, error) {
	o, err := scanRakuma(db.QueryRow(ctx, rakumaSelect+` WHERE o.id = $1`, id))
	if err != nil {
		return o, err
	}
	msgs, err := rakumaMessages(ctx, db, id)
	o.Messages = msgs[id]
	if o.Messages == nil {
		o.Messages = []models.RakumaMessage{}
	}
	return o, err
}

// rakumaMessages groups messages by order; orderID 0 means all orders.
func rakumaMessages(ctx context.Context, db DB, orderID int64) (map[int64][]models.RakumaMessage, error) {
	rows, err := db.Query(ctx, `
		SELECT m.order_id, m.id, m.sender, m.sent_at, m.body,
		       m.sender = 'seller' AND (o.messages_handled_at IS NULL OR m.created_at > o.messages_handled_at)
		FROM rakuma_messages m JOIN rakuma_orders o ON o.id = m.order_id
		WHERE $1 = 0 OR m.order_id = $1
		ORDER BY m.order_id, m.id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]models.RakumaMessage{}
	for rows.Next() {
		var oid int64
		var m models.RakumaMessage
		if err := rows.Scan(&oid, &m.ID, &m.From, &m.At, &m.Body, &m.New); err != nil {
			return nil, err
		}
		out[oid] = append(out[oid], m)
	}
	return out, rows.Err()
}

type RakumaRow struct {
	OrderNo    string
	Link       string
	Title      string
	Status     string
	Date       *string
	Price      int64
	Discount   int64
	Carrier    string
	Tracking   *string
	Seller     string
	Summary    string
	ReplyDraft string
}

// UpsertRakumaOrder inserts or refreshes an order by order_no. Empty date, tracking, summary and draft never erase
// what an earlier sync stored. It returns the row id and whether it was new.
func UpsertRakumaOrder(ctx context.Context, db DB, r RakumaRow) (id int64, created bool, err error) {
	err = db.QueryRow(ctx, `
		INSERT INTO rakuma_orders (order_no, item_url, title, status, order_date, price, discount, carrier, tracking_no,
		                           seller, summary, reply_draft)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (order_no) DO UPDATE SET
			item_url    = EXCLUDED.item_url,
			title       = EXCLUDED.title,
			status      = EXCLUDED.status,
			order_date  = COALESCE(EXCLUDED.order_date, rakuma_orders.order_date),
			price       = EXCLUDED.price,
			discount    = EXCLUDED.discount,
			carrier     = CASE WHEN EXCLUDED.carrier = '' THEN rakuma_orders.carrier ELSE EXCLUDED.carrier END,
			tracking_no = COALESCE(EXCLUDED.tracking_no, rakuma_orders.tracking_no),
			seller      = CASE WHEN EXCLUDED.seller = '' THEN rakuma_orders.seller ELSE EXCLUDED.seller END,
			summary     = CASE WHEN EXCLUDED.summary = '' THEN rakuma_orders.summary ELSE EXCLUDED.summary END,
			reply_draft = CASE WHEN EXCLUDED.reply_draft = '' THEN rakuma_orders.reply_draft ELSE EXCLUDED.reply_draft END,
			synced_at   = now(),
			updated_at  = now()
		RETURNING id, xmax = 0`,
		r.OrderNo, r.Link, r.Title, r.Status, r.Date, r.Price, r.Discount, r.Carrier, r.Tracking, r.Seller, r.Summary,
		r.ReplyDraft).Scan(&id, &created)
	return
}

// InsertRakumaMessage stores a message once; a re-sync of the same message is a no-op that returns false.
func InsertRakumaMessage(ctx context.Context, db DB, orderID int64, sender, sentAt, body string) (bool, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO rakuma_messages (order_id, sender, sent_at, body) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		orderID, sender, sentAt, body)
	return tag.RowsAffected() == 1, err
}

func SetRakumaPurchase(ctx context.Context, db DB, id, purchaseID int64) error {
	_, err := db.Exec(ctx, `UPDATE rakuma_orders SET purchase_id = $2, is_dismissed = FALSE, updated_at = now() WHERE id = $1`, id, purchaseID)
	return err
}

func SetRakumaDismissed(ctx context.Context, db DB, id int64, dismissed bool) error {
	_, err := db.Exec(ctx, `UPDATE rakuma_orders SET is_dismissed = $2, updated_at = now() WHERE id = $1`, id, dismissed)
	return err
}

func SetRakumaHandled(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `UPDATE rakuma_orders SET messages_handled_at = now(), updated_at = now() WHERE id = $1`, id)
	return err
}

func SetPurchaseTracking(ctx context.Context, db DB, id int64, tracking string) error {
	_, err := db.Exec(ctx, `UPDATE purchases SET tracking_no = $2, updated_at = now() WHERE id = $1`, id, tracking)
	return err
}
