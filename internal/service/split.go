package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

// SplitLine is one product of a combined order ("đơn gộp").
type SplitLine struct {
	ProductID string `json:"productId"`
	Qty       Flex   `json:"qty"`
	Price     Flex   `json:"price"`    // per item
	Discount  Flex   `json:"discount"` // per item; empty = 0. Lets an order-wide discount (400¥ off 3 items) add up exactly
}

// SplitPurchase turns one row that holds a combined order into one row per product, like the owner's sheet: the first
// row keeps the link, the others have none, and all share the tracking number as one merged shipment (BR-06), so the
// link is still on a single row (BR-05). The lines must add up to the row's total; the order's cost does not change.
func (s *Service) SplitPurchase(ctx context.Context, actor string, id int64, lines []SplitLine) ([]models.Purchase, error) {
	var out []models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.splitPurchase(ctx, tx, actor, id, lines)
		return err
	})
	return out, err
}

func (s *Service) splitPurchase(ctx context.Context, tx pgx.Tx, actor string, id int64, lines []SplitLine) ([]models.Purchase, error) {
	var out []models.Purchase
	err := func() error {
		before, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		e := map[string]string{}
		if len(lines) < 2 {
			e["lines"] = "Đơn gộp cần ít nhất 2 sản phẩm."
		}
		type line struct {
			product          int64
			qty, price, disc int64
		}
		ok := make([]line, len(lines))
		var sum int64
		for i, l := range lines {
			p, found := s.productFor(ctx, tx, l.ProductID, before.ProductID)
			if !found {
				e[fmt.Sprintf("lines.%d.productId", i)] = "Chọn sản phẩm có trong danh mục."
			}
			qty, qOK := l.Qty.int()
			if !qOK || qty < 1 {
				e[fmt.Sprintf("lines.%d.qty", i)] = "Số lượng phải là số nguyên từ 1 trở lên."
			}
			price, pOK := l.Price.int()
			if !pOK || price <= 0 {
				e[fmt.Sprintf("lines.%d.price", i)] = "Giá nhập phải là số nguyên lớn hơn 0."
			}
			var disc int64
			if !l.Discount.empty() {
				v, dOK := l.Discount.int()
				if !dOK || v < 0 || pOK && v > price {
					e[fmt.Sprintf("lines.%d.discount", i)] = "Giảm giá mỗi cái phải từ 0 đến giá nhập."
				}
				disc = v
			}
			ok[i] = line{p.ID, qty, price, disc}
			sum += qty * (price - disc)
		}
		if len(e) == 0 && sum != before.Total {
			e["lines"] = fmt.Sprintf("Tổng các dòng %s¥ phải bằng tổng đơn %s¥ (lệch %s¥).", money(sum), money(before.Total), money(before.Total-sum))
		}
		if len(e) == 0 && before.Points > ok[0].qty*(ok[0].price-ok[0].disc) { // the points stay on the first row
			e["lines"] = fmt.Sprintf("Đơn trả %s point: dòng đầu phải từ %s¥ trở lên để giữ số point đó.", money(before.Points), money(before.Points))
		}
		if len(e) > 0 {
			return &ValidationError{Fields: e}
		}

		group := before.Tracking
		if group == "" { // tracking not known yet: the rows still move together when the sync fills it in
			group = "split:" + strconv.FormatInt(id, 10)
		}
		row := func(l line, link string) repo.PurchaseRow {
			return repo.PurchaseRow{
				PeriodID: before.PeriodID, Source: before.Source, Date: ptr(before.Date), Link: ptr(link), ProductID: l.product,
				Price: l.price, Qty: int(l.qty), Discount: l.disc, CardID: before.CardID, Tracking: ptr(before.Tracking), MergeGroup: &group,
				Checked: before.Checked, Reviewed: before.Reviewed, Note: before.Note,
			}
		}
		head := row(ok[0], before.Link)
		head.Points, head.PointCard = before.Points, before.PointCardID
		if err := repo.UpdatePurchase(ctx, tx, id, head); err != nil {
			return err
		}
		first, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := repo.Audit(ctx, tx, "purchase", id, "split", actor, before, first); err != nil {
			return err
		}
		out = append(out, first)
		for _, l := range ok[1:] {
			nid, err := repo.InsertPurchase(ctx, tx, row(l, ""))
			if err != nil {
				return err
			}
			p, err := repo.GetPurchase(ctx, tx, nid)
			if err != nil {
				return err
			}
			if err := repo.Audit(ctx, tx, "purchase", nid, "create", actor, nil, p); err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	}()
	return out, err
}

func money(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := v < 0
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}
