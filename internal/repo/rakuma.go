package repo

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

const rakumaSelect = `
SELECT o.id, o.order_no, o.item_url, o.title, o.image_url, o.status, COALESCE(to_char(o.order_date, 'YYYY-MM-DD'), ''),
       o.price, o.discount, o.carrier, COALESCE(o.tracking_no, ''), o.seller, o.summary, o.reply_draft,
       o.rating, o.issue_note, o.purchase_id::text, o.is_dismissed, o.is_chat_open,
       (SELECT COUNT(*) FROM rakuma_messages m WHERE m.order_id = o.id AND m.sender = 'seller'
          AND (o.messages_handled_at IS NULL OR m.created_at > o.messages_handled_at))::int,
       to_char(o.synced_at AT TIME ZONE 'Asia/Tokyo', 'YYYY-MM-DD HH24:MI'),
       COALESCE(to_char(o.missing_since AT TIME ZONE 'Asia/Tokyo', 'YYYY-MM-DD HH24:MI'), '')
FROM rakuma_orders o`

func scanRakuma(row pgx.Row) (models.RakumaOrder, error) {
	var o models.RakumaOrder
	err := row.Scan(&o.ID, &o.OrderNo, &o.Link, &o.Title, &o.Image, &o.Status, &o.Date, &o.Price, &o.Discount, &o.Carrier,
		&o.Tracking, &o.Seller, &o.Summary, &o.ReplyDraft, &o.Rating, &o.IssueNote, &o.PurchaseID, &o.Dismissed, &o.ChatOpen, &o.NewMessages, &o.SyncedAt, &o.MissingSince)
	return o, err
}

// ListRakumaOrders returns orders newest first, each with its messages and replies in arrival order.
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
	replies, err := rakumaReplies(ctx, db, 0)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		fillThread(&orders[i], msgs, replies)
	}
	return orders, nil
}

func fillThread(o *models.RakumaOrder, msgs map[int64][]models.RakumaMessage, replies map[int64][]models.RakumaReply) {
	o.Messages, o.Replies = msgs[o.ID], replies[o.ID]
	if o.Messages == nil {
		o.Messages = []models.RakumaMessage{}
	}
	if o.Replies == nil {
		o.Replies = []models.RakumaReply{}
	}
}

func GetRakumaOrder(ctx context.Context, db DB, id int64) (models.RakumaOrder, error) {
	o, err := scanRakuma(db.QueryRow(ctx, rakumaSelect+` WHERE o.id = $1`, id))
	if err != nil {
		return o, err
	}
	msgs, err := rakumaMessages(ctx, db, id)
	if err != nil {
		return o, err
	}
	replies, err := rakumaReplies(ctx, db, id)
	if err != nil {
		return o, err
	}
	fillThread(&o, msgs, replies)
	return o, nil
}

// rakumaReplies groups the owner's replies by order; orderID 0 means all orders.
func rakumaReplies(ctx context.Context, db DB, orderID int64) (map[int64][]models.RakumaReply, error) {
	rows, err := db.Query(ctx, `
		SELECT order_id, id, body_vi, body_ja, kind, status, skip_reason,
		       to_char(created_at AT TIME ZONE 'Asia/Tokyo', 'YYYY-MM-DD HH24:MI'),
		       COALESCE(to_char(sent_at AT TIME ZONE 'Asia/Tokyo', 'YYYY-MM-DD HH24:MI'), '')
		FROM rakuma_replies WHERE $1 = 0 OR order_id = $1 ORDER BY order_id, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]models.RakumaReply{}
	for rows.Next() {
		var oid int64
		var r models.RakumaReply
		if err := rows.Scan(&oid, &r.ID, &r.BodyVi, &r.BodyJa, &r.Kind, &r.Status, &r.Reason, &r.CreatedAt, &r.SentAt); err != nil {
			return nil, err
		}
		out[oid] = append(out[oid], r)
	}
	return out, rows.Err()
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
	Image      string
	Status     string
	Date       *string
	Price      int64
	Discount   int64
	Carrier    string
	Tracking   *string
	Seller     string
	Summary    string
	ReplyDraft string
	ChatOpen   *bool
}

// UpsertRakumaOrder inserts or refreshes an order by order_no. Empty date, tracking, summary and draft never erase
// what an earlier sync stored. It returns the row id and whether it was new.
func UpsertRakumaOrder(ctx context.Context, db DB, r RakumaRow) (id int64, created bool, err error) {
	err = db.QueryRow(ctx, `
		INSERT INTO rakuma_orders (order_no, item_url, title, status, order_date, price, discount, carrier, tracking_no,
		                           seller, summary, reply_draft, image_url, is_chat_open)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, COALESCE($14, TRUE))
		ON CONFLICT (order_no) DO UPDATE SET
			item_url    = EXCLUDED.item_url,
			title       = CASE WHEN EXCLUDED.title = '' THEN rakuma_orders.title ELSE EXCLUDED.title END,
			image_url   = CASE WHEN EXCLUDED.image_url = '' THEN rakuma_orders.image_url ELSE EXCLUDED.image_url END,
			status      = EXCLUDED.status,
			order_date  = COALESCE(EXCLUDED.order_date, rakuma_orders.order_date),
			price       = CASE WHEN EXCLUDED.price = 0 THEN rakuma_orders.price ELSE EXCLUDED.price END,
			discount    = CASE WHEN EXCLUDED.price = 0 THEN rakuma_orders.discount ELSE EXCLUDED.discount END,
			carrier     = CASE WHEN EXCLUDED.carrier = '' THEN rakuma_orders.carrier ELSE EXCLUDED.carrier END,
			tracking_no = COALESCE(EXCLUDED.tracking_no, rakuma_orders.tracking_no),
			seller      = CASE WHEN EXCLUDED.seller = '' THEN rakuma_orders.seller ELSE EXCLUDED.seller END,
			summary     = CASE WHEN EXCLUDED.summary = '' THEN rakuma_orders.summary ELSE EXCLUDED.summary END,
			reply_draft = CASE WHEN EXCLUDED.reply_draft = '' THEN rakuma_orders.reply_draft ELSE EXCLUDED.reply_draft END,
			is_chat_open = COALESCE($14, rakuma_orders.is_chat_open),
			synced_at   = now(),
			missing_since = NULL,
			updated_at  = now()
		RETURNING id, xmax = 0`,
		r.OrderNo, r.Link, r.Title, r.Status, r.Date, r.Price, r.Discount, r.Carrier, r.Tracking, r.Seller, r.Summary,
		r.ReplyDraft, r.Image, r.ChatOpen).Scan(&id, &created)
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

func SetRakumaNotes(ctx context.Context, db DB, id int64, rating, issueNote string) error {
	_, err := db.Exec(ctx, `UPDATE rakuma_orders SET rating = $2, issue_note = $3, updated_at = now() WHERE id = $1`, id, rating, issueNote)
	return err
}

func InsertRakumaReply(ctx context.Context, db DB, orderID int64, kind, bodyVi string) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO rakuma_replies (order_id, kind, body_vi) VALUES ($1, $2, $3) RETURNING id`, orderID, kind, bodyVi).Scan(&id)
	return id, err
}

func SkipRakumaReply(ctx context.Context, db DB, id int64, reason string) error {
	_, err := db.Exec(ctx, `UPDATE rakuma_replies SET status = 'SKIPPED', skip_reason = $2, sent_at = now() WHERE id = $1`, id, reason)
	return err
}

// MarkRakumaMissing flags in-progress orders whose link is not among the links listed on Rakuma, clears the flag on
// listed ones, and returns every flagged order. Finished (取引完了) and dismissed orders are never flagged.
func MarkRakumaMissing(ctx context.Context, db DB, listed []string) ([]models.RakumaOrder, error) {
	if _, err := db.Exec(ctx, `
		UPDATE rakuma_orders SET missing_since = CASE WHEN item_url = ANY($1) THEN NULL ELSE COALESCE(missing_since, now()) END
		WHERE item_url = ANY($1) AND missing_since IS NOT NULL
		   OR NOT item_url = ANY($1) AND NOT is_dismissed AND status NOT LIKE '取引完了%'`, listed); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, rakumaSelect+` WHERE o.missing_since IS NOT NULL ORDER BY o.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.RakumaOrder, error) { return scanRakuma(r) })
}

func SetRakumaChatOpen(ctx context.Context, db DB, id int64, open bool) error {
	_, err := db.Exec(ctx, `UPDATE rakuma_orders SET is_chat_open = $2, updated_at = now() WHERE id = $1`, id, open)
	return err
}

// ReplyOrder returns the reply's order id and status.
func ReplyOrder(ctx context.Context, db DB, id int64) (orderID int64, status string, err error) {
	err = db.QueryRow(ctx, `SELECT order_id, status FROM rakuma_replies WHERE id = $1`, id).Scan(&orderID, &status)
	return
}

func MarkRakumaReplySent(ctx context.Context, db DB, id int64, bodyJa string) error {
	_, err := db.Exec(ctx, `UPDATE rakuma_replies SET status = 'SENT', body_ja = $2, sent_at = now() WHERE id = $1`, id, bodyJa)
	return err
}

func DeleteRakumaReply(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `DELETE FROM rakuma_replies WHERE id = $1`, id)
	return err
}
