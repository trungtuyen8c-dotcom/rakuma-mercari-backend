package service

import (
	"context"
	"errors"
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
	ChatOpen   *bool                `json:"chatOpen"` // omitted = keep; false once Rakuma hides the chat
	Messages   []RakumaMessageInput `json:"messages"`
}

type RakumaSyncResult struct {
	Created     int `json:"created"`
	Updated     int `json:"updated"`
	TrackingSet int `json:"trackingSet"` // purchases whose tracking number was filled from Rakuma
	NewMessages int `json:"newMessages"`
	// Warnings lists queued orders whose link or tracking number is already on a purchase row, and purchases whose
	// newly copied tracking number repeats another row's (BR-05, BR-06).
	Warnings []string `json:"warnings"`
}

func validRakumaOrder(in RakumaOrderInput) (repo.RakumaRow, map[string]string) {
	e := map[string]string{}
	r := repo.RakumaRow{
		OrderNo: strings.TrimSpace(in.OrderNo), Link: strings.TrimSpace(in.Link), Title: strings.TrimSpace(in.Title),
		Status: strings.TrimSpace(in.Status), Carrier: strings.TrimSpace(in.Carrier), Seller: strings.TrimSpace(in.Seller),
		Summary: strings.TrimSpace(in.Summary), ReplyDraft: strings.TrimSpace(in.ReplyDraft), ChatOpen: in.ChatOpen,
	}
	if r.OrderNo == "" {
		e["orderNo"] = "Thiếu mã đơn Rakuma."
	}
	if !reLink.MatchString(r.Link) {
		e["link"] = "Link phải bắt đầu bằng http:// hoặc https://"
	}
	if r.Image = strings.TrimSpace(in.Image); r.Image != "" && !reLink.MatchString(r.Image) {
		e["image"] = "Link ảnh phải bắt đầu bằng http:// hoặc https://"
	}
	var price int64 // 0 = not read from Rakuma; the owner enters it when approving
	if !in.Price.empty() {
		v, ok := in.Price.int()
		if !ok || v < 0 {
			e["price"] = "Giá phải là số nguyên không âm."
		}
		price = v
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
// order was approved into.
// listed (optional) holds every item link shown in 取引中 and the part of 購入済 that was read; unfinished orders
// missing from it are flagged and reported.
func (s *Service) SyncRakuma(ctx context.Context, actor string, orders []RakumaOrderInput, listed []string) (RakumaSyncResult, error) {
	res := RakumaSyncResult{Warnings: []string{}}
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
			if o.PurchaseID == nil {
				if !o.Dismissed {
					w, err := queuedDuplicates(ctx, tx, o)
					if err != nil {
						return err
					}
					res.Warnings = append(res.Warnings, w...)
				}
				continue
			}
			if o.Tracking == "" {
				continue
			}
			pid, _ := parseID(*o.PurchaseID)
			before, err := repo.GetPurchase(ctx, tx, pid)
			if err != nil {
				return err
			}
			if before.Tracking == o.Tracking {
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
			same, err := repo.PurchasesByTracking(ctx, tx, o.Tracking)
			if err != nil {
				return err
			}
			if same = without(same, pid); len(same) > 0 && !(after.Merged && allMerged(same)) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("Đơn %s: mã vận đơn %s vừa ghi vào dòng nhập số %d nhưng đã có ở dòng số %s.",
					o.OrderNo, o.Tracking, after.STT, rowRefs(same)))
			}
		}
		if listed != nil {
			gone, err := repo.MarkRakumaMissing(ctx, tx, listed)
			if err != nil {
				return err
			}
			for _, o := range gone {
				res.Warnings = append(res.Warnings, fmt.Sprintf("Đơn %s (%s, %s) không còn trên Rakuma từ %s: kiểm tra xem có bị huỷ hay hết hạn thanh toán.",
					o.OrderNo, o.Seller, o.Title, o.MissingSince))
			}
		}
		return repo.Audit(ctx, tx, "rakuma_sync", "batch", "sync", actor, nil, res)
	})
	return res, err
}

// queuedDuplicates warns when an order still waiting for approval is already on a purchase row, e.g. entered by hand.
func queuedDuplicates(ctx context.Context, tx pgx.Tx, o models.RakumaOrder) ([]string, error) {
	var w []string
	same, err := repo.PurchasesByLink(ctx, tx, o.Link)
	if err != nil {
		return nil, err
	}
	if len(same) > 0 {
		w = append(w, fmt.Sprintf("Đơn %s đang chờ duyệt: link đã có ở dòng nhập số %s.", o.OrderNo, rowRefs(same)))
	}
	if o.Tracking != "" {
		if same, err = repo.PurchasesByTracking(ctx, tx, o.Tracking); err != nil {
			return nil, err
		}
		if len(same) > 0 {
			w = append(w, fmt.Sprintf("Đơn %s đang chờ duyệt: mã vận đơn %s đã có ở dòng nhập số %s.", o.OrderNo, o.Tracking, rowRefs(same)))
		}
	}
	return w, nil
}

func allMerged(rows []models.Purchase) bool {
	for _, r := range rows {
		if !r.Merged {
			return false
		}
	}
	return true
}

func rowRefs(rows []models.Purchase) string {
	refs := make([]string, len(rows))
	for i, r := range rows {
		refs[i] = fmt.Sprintf("%d (kỳ %s)", r.STT, r.PeriodLabel)
	}
	return strings.Join(refs, ", ")
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
// on the purchase the order became.
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
		if err != nil || p.Reviewed {
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
	return s.updateRakuma(ctx, actor, id, "reply", func(tx pgx.Tx, o models.RakumaOrder) error {
		if !o.ChatOpen {
			return errChatClosed
		}
		_, err := repo.InsertRakumaReply(ctx, tx, id, "REPLY", body)
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

// MaxRakumaMessage is the length limit of Rakuma's transaction message box (maxlength="250", verified 2026-10-10).
const MaxRakumaMessage = 250

// SetRakumaReplyTranslation saves Claude's Japanese text for a pending reply, so the owner can send it from the
// Chrome extension (which fills Rakuma's message box; the owner presses send).
func (s *Service) SetRakumaReplyTranslation(ctx context.Context, actor string, replyID int64, bodyJa string) (models.RakumaOrder, error) {
	bodyJa = strings.TrimSpace(bodyJa)
	if bodyJa == "" {
		return models.RakumaOrder{}, &ValidationError{Fields: map[string]string{"bodyJa": "Thiếu bản tiếng Nhật."}}
	}
	if n := len([]rune(bodyJa)); n > MaxRakumaMessage {
		return models.RakumaOrder{}, &ValidationError{Fields: map[string]string{"bodyJa": fmt.Sprintf("Bản tiếng Nhật dài %d ký tự, khung chat Rakuma chỉ nhận tối đa %d.", n, MaxRakumaMessage)}}
	}
	return s.updateReply(ctx, actor, replyID, "reply_translation", func(tx pgx.Tx) error { return repo.SetRakumaReplyTranslation(ctx, tx, replyID, bodyJa) })
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

var errChatClosed = &ConflictError{Msg: "Chat của đơn này đã đóng trên Rakuma, không gửi được nữa."}

// MaxBroadcast caps one broadcast; Claude also paces sending, so a large batch spreads over several syncs.
const MaxBroadcast = 100

type BroadcastResult struct {
	Queued  int `json:"queued"`
	Skipped int `json:"skipped"` // chat already closed or order not found
}

// BroadcastRakuma queues one Vietnamese message for each selected order whose chat is still open. Claude translates,
// adds the seller and item name, and posts them slowly so the account is not flagged for spam.
func (s *Service) BroadcastRakuma(ctx context.Context, actor string, orderIDs []string, body string) (BroadcastResult, error) {
	var res BroadcastResult
	body = strings.TrimSpace(body)
	e := map[string]string{}
	if body == "" {
		e["body"] = "Nhập nội dung tin nhắn."
	}
	if len(orderIDs) == 0 || len(orderIDs) > MaxBroadcast {
		e["orderIds"] = fmt.Sprintf("Chọn từ 1 đến %d đơn.", MaxBroadcast)
	}
	if len(e) > 0 {
		return res, &ValidationError{Fields: e}
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		seen := map[int64]bool{}
		for _, raw := range orderIDs {
			id, ok := parseID(raw)
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			o, err := repo.GetRakumaOrder(ctx, tx, id)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err != nil || !o.ChatOpen {
				res.Skipped++
				continue
			}
			if _, err := repo.InsertRakumaReply(ctx, tx, id, "BROADCAST", body); err != nil {
				return err
			}
			res.Queued++
		}
		return repo.Audit(ctx, tx, "rakuma_broadcast", "batch", "create", actor, nil, map[string]any{"body": body, "result": res})
	})
	return res, err
}

// SkipRakumaReply records that Claude could not post a pending reply; chatClosed also marks the order's chat closed.
func (s *Service) SkipRakumaReply(ctx context.Context, actor string, replyID int64, reason string, chatClosed bool) (models.RakumaOrder, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return models.RakumaOrder{}, &ValidationError{Fields: map[string]string{"reason": "Ghi lý do bỏ qua."}}
	}
	return s.updateReply(ctx, actor, replyID, "reply_skip", func(tx pgx.Tx) error {
		if err := repo.SkipRakumaReply(ctx, tx, replyID, reason); err != nil {
			return err
		}
		if !chatClosed {
			return nil
		}
		orderID, _, err := repo.ReplyOrder(ctx, tx, replyID)
		if err != nil {
			return err
		}
		return repo.SetRakumaChatOpen(ctx, tx, orderID, false)
	})
}
