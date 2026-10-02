package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

type RakumaMessageInput struct {
	From string `json:"from"`
	At   string `json:"at"`
	Body string `json:"body"`
}

type RakumaOrderInput struct {
	OrderNo    string               `json:"orderNo"`
	Link       string               `json:"link"`
	Title      string               `json:"title"`
	Image      string               `json:"image"`
	Status     string               `json:"status"`
	Date       string               `json:"date"`
	Price      Flex                 `json:"price"`
	Discount   Flex                 `json:"discount"`
	Carrier    string               `json:"carrier"`
	Tracking   string               `json:"tracking"`
	Seller     string               `json:"seller"`
	Summary    string               `json:"summary"`
	ReplyDraft string               `json:"replyDraft"`
	Messages   []RakumaMessageInput `json:"messages"`
}

type RakumaSyncResult struct {
	Created     int `json:"created"`
	Updated     int `json:"updated"`
	TrackingSet int `json:"trackingSet"` // purchases whose tracking number was filled from Rakuma
	NewMessages int `json:"newMessages"`
}

func validRakumaOrder(in RakumaOrderInput) (repo.RakumaRow, map[string]string) {
	e := map[string]string{}
	r := repo.RakumaRow{
		OrderNo: strings.TrimSpace(in.OrderNo), Link: strings.TrimSpace(in.Link), Title: strings.TrimSpace(in.Title),
		Status: strings.TrimSpace(in.Status), Carrier: strings.TrimSpace(in.Carrier), Seller: strings.TrimSpace(in.Seller),
		Summary: strings.TrimSpace(in.Summary), ReplyDraft: strings.TrimSpace(in.ReplyDraft),
	}
	if r.OrderNo == "" {
		e["orderNo"] = "Thiếu mã đơn Rakuma."
	}
	if !reLink.MatchString(r.Link) {
		e["link"] = "Link phải bắt đầu bằng http:// hoặc https://"
	}
	if r.Title == "" {
		e["title"] = "Thiếu tên món."
	}
	if r.Image = strings.TrimSpace(in.Image); r.Image != "" && !reLink.MatchString(r.Image) {
		e["image"] = "Link ảnh phải bắt đầu bằng http:// hoặc https://"
	}
	price, ok := in.Price.int()
	if !ok || price <= 0 {
		e["price"] = "Giá phải là số nguyên lớn hơn 0."
	}
	r.Price = price
	if !in.Discount.empty() {
		v, ok := in.Discount.int()
		if !ok || v < 0 || v > price {
			e["discount"] = "Giảm giá phải từ 0 đến giá món."
		}
		r.Discount = v
	}
	if d := strings.TrimSpace(in.Date); d != "" {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			e["date"] = "Ngày không hợp lệ."
		}
		r.Date = &d
	}
	if t := strings.TrimSpace(in.Tracking); t != "" {
		if !reTracking.MatchString(t) {
			e["tracking"] = "Mã vận đơn chỉ gồm chữ, số và dấu gạch."
		}
		r.Tracking = &t
	}
	for _, m := range in.Messages {
		if m.From != "seller" && m.From != "buyer" {
			e["messages"] = "Người gửi phải là seller hoặc buyer."
		}
		if strings.TrimSpace(m.Body) == "" {
			e["messages"] = "Tin nhắn không được trống."
		}
	}
	return r, e
}

// SyncRakuma upserts a batch of scraped orders atomically. A new tracking number is copied onto the purchase the
// order was approved into, unless that purchase is in a closed period.
func (s *Service) SyncRakuma(ctx context.Context, actor string, orders []RakumaOrderInput) (RakumaSyncResult, error) {
	var res RakumaSyncResult
	rows := make([]repo.RakumaRow, len(orders))
	fields := map[string]string{}
	for i, in := range orders {
		r, e := validRakumaOrder(in)
		for k, v := range e {
			fields[fmt.Sprintf("orders.%d.%s", i, k)] = v
		}
		rows[i] = r
	}
	if len(fields) > 0 {
		return res, &ValidationError{Fields: fields}
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		for i, r := range rows {
			id, created, err := repo.UpsertRakumaOrder(ctx, tx, r)
			if err != nil {
				return err
			}
			if created {
				res.Created++
			} else {
				res.Updated++
			}
			for _, m := range orders[i].Messages {
				ok, err := repo.InsertRakumaMessage(ctx, tx, id, m.From, strings.TrimSpace(m.At), strings.TrimSpace(m.Body))
				if err != nil {
					return err
				}
				if ok && m.From == "seller" {
					res.NewMessages++
				}
			}
			o, err := repo.GetRakumaOrder(ctx, tx, id)
			if err != nil {
				return err
			}
			if o.PurchaseID == nil || o.Tracking == "" {
				continue
			}
			pid, _ := parseID(*o.PurchaseID)
			before, err := repo.GetPurchase(ctx, tx, pid)
			if err != nil {
				return err
			}
			if before.Locked || before.Tracking == o.Tracking {
				continue
			}
			if err := repo.SetPurchaseTracking(ctx, tx, pid, o.Tracking); err != nil {
				return err
			}
			after, err := repo.GetPurchase(ctx, tx, pid)
			if err != nil {
				return err
			}
			if err := repo.Audit(ctx, tx, "purchase", pid, "update", actor, before, after); err != nil {
				return err
			}
			res.TrackingSet++
		}
		return repo.Audit(ctx, tx, "rakuma_sync", "batch", "sync", actor, nil, res)
	})
	return res, err
}

// ApproveRakuma turns a queued order into a purchase. The link and tracking number always come from the order; the
// owner supplies the product and may correct qty, unit price and per-unit discount (a "5BOX" listing is one lump price).
func (s *Service) ApproveRakuma(ctx context.Context, actor string, id int64, in PurchaseInput, force bool) (models.Purchase, error) {
	var out models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		o, err := repo.GetRakumaOrder(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if o.PurchaseID != nil {
			return &ConflictError{Msg: "Đơn Rakuma này đã được nhập."}
		}
		in.Link = o.Link
		in.Tracking = o.Tracking
		if out, err = s.addPurchase(ctx, tx, actor, in, force); err != nil {
			return err
		}
		if err := repo.SetRakumaPurchase(ctx, tx, id, out.ID); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "rakuma_order", id, "approve", actor, o, out.ID)
	})
	return out, err
}

func (s *Service) DismissRakuma(ctx context.Context, actor string, id int64, dismissed bool) (models.RakumaOrder, error) {
	return s.updateRakuma(ctx, actor, id, "dismiss", func(tx pgx.Tx, _ models.RakumaOrder) error { return repo.SetRakumaDismissed(ctx, tx, id, dismissed) })
}

func (s *Service) HandleRakumaMessages(ctx context.Context, actor string, id int64) (models.RakumaOrder, error) {
	return s.updateRakuma(ctx, actor, id, "messages_handled", func(tx pgx.Tx, _ models.RakumaOrder) error { return repo.SetRakumaHandled(ctx, tx, id) })
}

func (s *Service) updateRakuma(ctx context.Context, actor string, id int64, action string, fn func(pgx.Tx, models.RakumaOrder) error) (models.RakumaOrder, error) {
	var out models.RakumaOrder
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetRakumaOrder(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if err := fn(tx, before); err != nil {
			return err
		}
		if out, err = repo.GetRakumaOrder(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "rakuma_order", id, action, actor, before, out)
	})
	return out, err
}

var ratings = map[string]bool{"": true, "GOOD": true, "NORMAL": true, "BAD": true}

// UpdateRakumaNotes sets the seller rating and/or the owner's issue note. Rating a seller also ticks "Đã đánh giá"
// on the purchase the order became, unless that purchase is in a closed period.
func (s *Service) UpdateRakumaNotes(ctx context.Context, actor string, id int64, rating, issueNote *string) (models.RakumaOrder, error) {
	if rating != nil && !ratings[*rating] {
		return models.RakumaOrder{}, &ValidationError{Fields: map[string]string{"rating": "Đánh giá phải là Tốt, Bình thường hoặc Không tốt."}}
	}
	return s.updateRakuma(ctx, actor, id, "update", func(tx pgx.Tx, o models.RakumaOrder) error {
		r, note := o.Rating, o.IssueNote
		if rating != nil {
			r = *rating
		}
		if issueNote != nil {
			note = strings.TrimSpace(*issueNote)
		}
		if err := repo.SetRakumaNotes(ctx, tx, id, r, note); err != nil {
			return err
		}
		if r == "" || o.PurchaseID == nil {
			return nil
		}
		pid, _ := parseID(*o.PurchaseID)
		p, err := repo.GetPurchase(ctx, tx, pid)
		if err != nil || p.Locked || p.Reviewed {
			return err
		}
		if err := repo.SetPurchaseFlags(ctx, tx, pid, p.Checked, true); err != nil {
			return err
		}
		after, err := repo.GetPurchase(ctx, tx, pid)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "purchase", pid, "update", actor, p, after)
	})
}

// AddRakumaReply queues a reply the owner wrote in Vietnamese; Claude translates and posts it on the next sync.
func (s *Service) AddRakumaReply(ctx context.Context, actor string, id int64, body string) (models.RakumaOrder, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return models.RakumaOrder{}, &ValidationError{Fields: map[string]string{"body": "Nhập nội dung trả lời."}}
	}
	return s.updateRakuma(ctx, actor, id, "reply", func(tx pgx.Tx, _ models.RakumaOrder) error {
		_, err := repo.InsertRakumaReply(ctx, tx, id, body)
		return err
	})
}

// MarkRakumaReplySent records the Japanese text Claude posted for a pending reply.
func (s *Service) MarkRakumaReplySent(ctx context.Context, actor string, replyID int64, bodyJa string) (models.RakumaOrder, error) {
	bodyJa = strings.TrimSpace(bodyJa)
	if bodyJa == "" {
		return models.RakumaOrder{}, &ValidationError{Fields: map[string]string{"bodyJa": "Thiếu nội dung tiếng Nhật đã gửi."}}
	}
	return s.updateReply(ctx, actor, replyID, "reply_sent", func(tx pgx.Tx) error { return repo.MarkRakumaReplySent(ctx, tx, replyID, bodyJa) })
}

// DeleteRakumaReply cancels a reply that has not been sent yet.
func (s *Service) DeleteRakumaReply(ctx context.Context, actor string, replyID int64) (models.RakumaOrder, error) {
	return s.updateReply(ctx, actor, replyID, "reply_delete", func(tx pgx.Tx) error { return repo.DeleteRakumaReply(ctx, tx, replyID) })
}

func (s *Service) updateReply(ctx context.Context, actor string, replyID int64, action string, fn func(pgx.Tx) error) (models.RakumaOrder, error) {
	orderID, _, err := repo.ReplyOrder(ctx, s.Pool, replyID)
	if err != nil {
		return models.RakumaOrder{}, notFound(err)
	}
	return s.updateRakuma(ctx, actor, orderID, action, func(tx pgx.Tx, _ models.RakumaOrder) error {
		if _, status, err := repo.ReplyOrder(ctx, tx, replyID); err != nil {
			return notFound(err)
		} else if status != "PENDING" {
			return &ConflictError{Msg: "Tin trả lời này đã được gửi."}
		}
		return fn(tx)
	})
}
