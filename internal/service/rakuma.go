package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	// The owner's choice in the extension preview: product id ("" = none, let the sync guess) and number of items.
	// Omitted keeps the stored choice; the purchase row written later uses it.
	ProductID *string `json:"productId"`
	Qty       Flex    `json:"qty"`
}

type RakumaSyncResult struct {
	Created     int `json:"created"`
	Updated     int `json:"updated"`
	TrackingSet int `json:"trackingSet"` // purchases whose tracking number was filled from Rakuma
	// TrackingFromChat: shipped orders without らくまパック whose tracking number was read from the seller's message
	TrackingFromChat int `json:"trackingFromChat"`
	Merged           int `json:"merged"`    // purchase rows marked as one parcel because orders of one seller share a tracking number
	DateSet          int `json:"dateSet"`   // purchases whose empty date was filled from Rakuma
	Linked           int `json:"linked"`    // queued orders attached to a purchase row already entered with the same link
	Purchased        int `json:"purchased"` // purchase rows the sync wrote for paid orders whose link was not on any row
	NewMessages      int `json:"newMessages"`
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
	if in.ProductID != nil || !in.Qty.empty() {
		r.PlanSet = true
		if v := strings.TrimSpace(deref(in.ProductID)); v != "" && v != "0" {
			id, ok := parseID(v)
			if !ok {
				e["productId"] = "Chọn sản phẩm có trong danh mục."
			}
			r.PlanProduct = id
		}
		if !in.Qty.empty() {
			q, ok := in.Qty.int()
			if !ok || q < 1 || q > 99 {
				e["qty"] = "Số lượng phải từ 1 đến 99."
			}
			r.PlanQty = int(q)
		}
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
		var products []models.Product
		for i, r := range rows {
			if r.PlanProduct != 0 {
				if _, err := repo.GetProduct(ctx, tx, r.PlanProduct); err != nil {
					return &ValidationError{Fields: map[string]string{fmt.Sprintf("orders.%d.productId", i): "Chọn sản phẩm có trong danh mục."}}
				}
			}
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
			if o.Tracking == "" && shippedStatus(o.Status) {
				if t, carrier := trackingFromMessages(o.Messages); t != "" {
					if err := repo.SetRakumaTrackingIfEmpty(ctx, tx, id, t, carrier); err != nil {
						return err
					}
					if err := repo.Audit(ctx, tx, "rakuma_order", id, "tracking_from_chat", actor, nil, map[string]string{"tracking": t, "carrier": carrier}); err != nil {
						return err
					}
					o.Tracking, o.TrackingFromChat = t, true
					res.TrackingFromChat++
				}
			}
			if o.PurchaseID == nil && !o.Dismissed {
				linked, err := linkEnteredPurchase(ctx, tx, actor, &o)
				if err != nil {
					return err
				}
				if linked {
					res.Linked++
				} else {
					if products == nil {
						if products, err = repo.ListProducts(ctx, tx); err != nil {
							return err
						}
					}
					made, w, err := s.purchaseFromRakuma(ctx, tx, actor, &o, products)
					if err != nil {
						return err
					}
					if made {
						res.Purchased++
						pid, _ := parseID(*o.PurchaseID)
						row, err := repo.GetPurchase(ctx, tx, pid)
						if err != nil {
							return err
						}
						if err := shareParcel(ctx, tx, o, row, &res); err != nil {
							return err
						}
					}
					res.Warnings = append(res.Warnings, w...)
				}
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
			pid, _ := parseID(*o.PurchaseID)
			before, err := repo.GetPurchase(ctx, tx, pid)
			if err != nil {
				return err
			}
			// The purchase date is filled only while the row has none, so a date the owner typed is never replaced
			fillDate := before.Date == "" && o.Date != ""
			// A number read from the chat only fills an empty row; one the owner typed stays
			setTracking := o.Tracking != "" && before.Tracking != o.Tracking && !(o.TrackingFromChat && before.Tracking != "")
			if !fillDate && !setTracking {
				continue
			}
			if fillDate {
				if err := repo.FillPurchaseDate(ctx, tx, pid, o.Date); err != nil {
					return err
				}
				res.DateSet++
			}
			if setTracking {
				if err := repo.SetPurchaseTracking(ctx, tx, pid, o.Tracking); err != nil {
					return err
				}
			}
			after, err := repo.GetPurchase(ctx, tx, pid)
			if err != nil {
				return err
			}
			if err := repo.Audit(ctx, tx, "purchase", pid, "update", actor, before, after); err != nil {
				return err
			}
			if !setTracking {
				continue
			}
			res.TrackingSet++
			if err := shareParcel(ctx, tx, o, after, &res); err != nil {
				return err
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

// linkEnteredPurchase attaches a queued order to the purchase row the owner already entered with the same item link,
// so it leaves the approval queue and later tracking numbers flow onto that row. It links only when exactly one row
// has the link and no other Rakuma order points at it.
func linkEnteredPurchase(ctx context.Context, tx pgx.Tx, actor string, o *models.RakumaOrder) (bool, error) {
	same, err := repo.PurchasesByLink(ctx, tx, o.Link)
	if err != nil || len(same) != 1 {
		return false, err
	}
	pid := same[0].ID
	taken, err := repo.RakumaOrderLinkedTo(ctx, tx, pid)
	if err != nil || taken {
		return false, err
	}
	if err := repo.SetRakumaPurchase(ctx, tx, o.ID, pid); err != nil {
		return false, err
	}
	if err := repo.Audit(ctx, tx, "rakuma_order", o.ID, "link", actor, nil, map[string]any{"purchaseId": pid}); err != nil {
		return false, err
	}
	sid := strconv.FormatInt(pid, 10)
	o.PurchaseID = &sid
	return true, nil
}

// purchaseFromRakuma writes the purchase row for a paid order whose link is on no row yet, so the owner only
// corrects it afterwards. The product comes from the owner's names and keywords and stays empty when unclear; a
// "4BOX" lump price is split only when it divides evenly, otherwise the row is one item at the lump price.
// Unpaid orders, and orders without a price or date, stay queued.
func (s *Service) purchaseFromRakuma(ctx context.Context, tx pgx.Tx, actor string, o *models.RakumaOrder, products []models.Product) (bool, []string, error) {
	if strings.HasPrefix(o.Status, "支払い") || o.Price <= 0 || o.Date == "" {
		return false, nil, nil
	}
	if same, err := repo.PurchasesByLink(ctx, tx, o.Link); err != nil || len(same) > 0 {
		return false, nil, err
	}
	// The owner's choice from the extension preview wins over the guess from the title
	q := int64(o.PlanQty)
	if q == 0 {
		q = int64(guessQty(o.Title))
	}
	if q < 1 {
		q = 1
	}
	var productID int64
	if o.PlanProductID != "" {
		productID, _ = parseID(o.PlanProductID)
	} else if p, ok := guessProduct(o.Title, products); ok {
		productID = p.ID
	}
	// One row of q items when the price and coupon divide evenly; otherwise one row at the order price, split below
	even := o.Price%q == 0 && o.Discount%q == 0
	qty, price, disc := int64(1), o.Price, o.Discount
	if even {
		qty, price, disc = q, o.Price/q, o.Discount/q
	}
	in := PurchaseInput{
		Date: o.Date, Link: o.Link, Tracking: o.Tracking, noProduct: true,
		Price: Flex(strconv.FormatInt(price, 10)), Qty: Flex(strconv.FormatInt(qty, 10)), Discount: Flex(strconv.FormatInt(disc, 10)),
	}
	if productID != 0 {
		in.ProductID = strconv.FormatInt(productID, 10)
	}
	if c, ok, err := repo.DefaultCard(ctx, tx); err != nil {
		return false, nil, err
	} else if ok {
		id := strconv.FormatInt(c.ID, 10)
		in.CardID = &id
	}
	p, err := s.addPurchase(ctx, tx, actor, in, true)
	var ve *ValidationError
	if errors.As(err, &ve) {
		msgs := make([]string, 0, len(ve.Fields))
		for _, m := range ve.Fields {
			msgs = append(msgs, m)
		}
		return false, []string{fmt.Sprintf("Đơn %s chưa tự nhập được, duyệt tay ở tab Rakuma: %s", o.OrderNo, strings.Join(msgs, " "))}, nil
	}
	if err != nil {
		return false, nil, err
	}
	if err := repo.SetRakumaPurchase(ctx, tx, o.ID, p.ID); err != nil {
		return false, nil, err
	}
	sid := strconv.FormatInt(p.ID, 10)
	o.PurchaseID = &sid
	if !even && q > 1 && productID != 0 {
		// q items whose price or coupon does not divide by q: q-1 items at the rounded-down unit figures and one item
		// taking the remainder, as two merged rows, so they add up to what the link cost exactly
		up, ud := o.Price/q, o.Discount/q
		pid := strconv.FormatInt(productID, 10)
		line := func(n, pr, d int64) SplitLine {
			return SplitLine{ProductID: pid, Qty: Flex(strconv.FormatInt(n, 10)), Price: Flex(strconv.FormatInt(pr, 10)), Discount: Flex(strconv.FormatInt(d, 10))}
		}
		_, err := s.splitPurchase(ctx, tx, actor, p.ID, []SplitLine{line(q-1, up, ud), line(1, o.Price-(q-1)*up, o.Discount-(q-1)*ud)})
		if errors.As(err, &ve) { // checked before any write: the row stays one lump for the owner to split
			return true, []string{fmt.Sprintf("Đơn %s: chưa chia được %d món, dòng nhập giữ giá cả đơn; bấm Tách ở Nhập hàng.", o.OrderNo, q)}, nil
		}
		if err != nil {
			return false, nil, err
		}
	}
	return true, nil, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// shareParcel runs after a tracking number lands on a purchase row. When other rows carry the same number and all of
// them belong to Rakuma orders of the same seller, the rows are one parcel and get one merge group; otherwise the
// owner is warned of the duplicate (BR-06).
func shareParcel(ctx context.Context, tx pgx.Tx, o models.RakumaOrder, row models.Purchase, res *RakumaSyncResult) error {
	if o.Tracking == "" {
		return nil
	}
	same, err := repo.PurchasesByTracking(ctx, tx, o.Tracking)
	if err != nil {
		return err
	}
	if same = without(same, row.ID); len(same) == 0 || row.Merged && allMerged(same) {
		return nil
	}
	oneParcel, err := sameSellerRows(ctx, tx, o.Seller, same)
	if err != nil {
		return err
	}
	if !oneParcel {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Đơn %s: mã vận đơn %s vừa ghi vào dòng nhập số %d nhưng đã có ở dòng số %s.",
			o.OrderNo, o.Tracking, row.STT, rowRefs(same)))
		return nil
	}
	ids := []int64{row.ID}
	for _, r := range same {
		ids = append(ids, r.ID)
	}
	if err := repo.SetPurchasesMerged(ctx, tx, ids, o.Tracking); err != nil {
		return err
	}
	res.Merged += len(ids)
	return nil
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
		if in.CardID == nil { // like the rows the sync writes: paid with the default card unless the owner picked one
			c, ok, err := repo.DefaultCard(ctx, tx)
			if err != nil {
				return err
			}
			if ok {
				id := strconv.FormatInt(c.ID, 10)
				in.CardID = &id
			}
		}
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

// DismissRakuma skips an order (kept with who, when and why) or restores it. A restored order is linked at once to the
// purchase row with its item link when exactly one row has it, as the sync would; otherwise it goes back to the queue.
func (s *Service) DismissRakuma(ctx context.Context, actor string, id int64, dismissed bool, reason string) (models.RakumaOrder, error) {
	return s.updateRakuma(ctx, actor, id, "dismiss", func(tx pgx.Tx, o models.RakumaOrder) error {
		if dismissed {
			reason = strings.TrimSpace(reason)
			if len([]rune(reason)) > 300 {
				return &ValidationError{Fields: map[string]string{"reason": "Lý do tối đa 300 ký tự."}}
			}
			if reason == "" {
				reason = "Bấm Bỏ qua"
			}
			return repo.SetRakumaDismissed(ctx, tx, id, actor, "manual", reason)
		}
		if err := repo.SetRakumaDismissed(ctx, tx, id, "", "", ""); err != nil {
			return err
		}
		if o.PurchaseID != nil {
			return nil
		}
		_, err := linkEnteredPurchase(ctx, tx, actor, &o)
		return err
	})
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

// sameSellerRows reports whether every row is linked to a Rakuma order of this seller.
func sameSellerRows(ctx context.Context, tx pgx.Tx, seller string, rows []models.Purchase) (bool, error) {
	if seller == "" {
		return false, nil
	}
	for _, r := range rows {
		s, err := repo.RakumaSellerOfPurchase(ctx, tx, r.ID)
		if err != nil || s != seller {
			return false, err
		}
	}
	return true, nil
}
