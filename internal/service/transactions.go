package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

var (
	reLink     = regexp.MustCompile(`^https?://\S+$`)
	reTracking = regexp.MustCompile(`^[0-9A-Za-z-]+$`)
)

type PurchaseInput struct {
	PeriodID  string `json:"periodId"` // optional: an open period; otherwise picked from the date, else the newest open one
	ProductID string `json:"productId"`
	Source    string `json:"source"`
	Date      string `json:"date"`
	Price     Flex   `json:"price"`
	Qty       Flex   `json:"qty"`
	Discount  Flex   `json:"discount"`
	Tracking  string `json:"tracking"`
	Merged    bool   `json:"merged"`
	Link      string `json:"link"`
	Note      string `json:"note"`

	// noProduct lets a row be saved without a product: rows written by the Rakuma sync, and edits of such rows.
	noProduct bool
}

// OpenedStockNote marks a 0¥ sale that only takes opened items out of stock.
const OpenedStockNote = "Bóc hàng"

type SaleInput struct {
	PeriodID  string `json:"periodId"`
	ProductID string `json:"productId"`
	Date      string `json:"date"`
	Qty       Flex   `json:"qty"`
	Price     Flex   `json:"price"`
	Ship      Flex   `json:"ship"`
	Customer  string `json:"customer"`
	Note      string `json:"note"`
}

// writePeriod picks the open period a new row goes to. Up to two periods can be open (an old month finishing while
// the next one has started): an explicit periodId wins, then the period containing the date, then the newest one.
// Errors are keyed by form field.
func writePeriod(ctx context.Context, db repo.DB, periodID, date string) (models.Period, map[string]string, error) {
	opens, err := repo.OpenPeriods(ctx, db, false)
	if err != nil {
		return models.Period{}, nil, err
	}
	labels := make([]string, len(opens))
	for i, p := range opens {
		labels[i] = p.Label
	}
	pick := opens[len(opens)-1]
	switch {
	case strings.TrimSpace(periodID) != "":
		// The owner may write into any period, closed ones included; later periods follow (migration 0006)
		id, _ := parseID(periodID)
		p, err := repo.GetPeriod(ctx, db, id)
		if err != nil {
			return pick, map[string]string{"periodId": "Không tìm thấy kỳ này."}, nil
		}
		pick = p
	case date != "":
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return pick, map[string]string{"date": "Ngày không hợp lệ."}, nil
		}
		found := false
		for _, p := range opens {
			if date >= p.Start && date <= p.End {
				pick, found = p, true
			}
		}
		if !found {
			return pick, map[string]string{"date": fmt.Sprintf("Ngày phải nằm trong kỳ đang mở (%s). Kỳ khác đã chốt hoặc chưa mở.", strings.Join(labels, ", "))}, nil
		}
	}
	if m := dateErr(date, pick); m != "" {
		return pick, map[string]string{"date": m}, nil
	}
	return pick, nil, nil
}

// dateErr: optional date, must be valid and inside the open period (Q-10: a period is a calendar month).
func dateErr(d string, open models.Period) string {
	if d == "" {
		return ""
	}
	if _, err := time.Parse("2006-01-02", d); err != nil {
		return "Ngày không hợp lệ."
	}
	if d < open.Start || d > open.End {
		return fmt.Sprintf("Ngày phải nằm trong kỳ đang mở %s. Kỳ khác đã chốt hoặc chưa mở.", open.Label)
	}
	return ""
}

func (s *Service) activeProduct(ctx context.Context, db repo.DB, id string) (models.Product, bool) {
	pid, ok := parseID(id)
	if !ok {
		return models.Product{}, false
	}
	p, err := repo.GetProduct(ctx, db, pid)
	return p, err == nil && p.Active
}

func (s *Service) AddPurchase(ctx context.Context, actor string, in PurchaseInput, force bool) (models.Purchase, error) {
	var out models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.savePurchase(ctx, tx, actor, in, force, 0)
		return err
	})
	return out, err
}

// UpdatePurchase replaces every field of a purchase, in any period. It stays in its period unless periodId says
// otherwise; check/review marks are kept. Totals, stock and every later period follow from the rows.
func (s *Service) UpdatePurchase(ctx context.Context, actor string, id int64, in PurchaseInput, force bool) (models.Purchase, error) {
	var out models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.savePurchase(ctx, tx, actor, in, force, id)
		return err
	})
	return out, err
}

func (s *Service) addPurchase(ctx context.Context, tx pgx.Tx, actor string, in PurchaseInput, force bool) (models.Purchase, error) {
	return s.savePurchase(ctx, tx, actor, in, force, 0)
}

// rowPeriod picks the period for a new row (editID 0) or an edited one: an edit stays in its period unless
// periodId moves it, and its date must fall inside that period.
func rowPeriod(ctx context.Context, tx pgx.Tx, periodID, date string, current int64) (models.Period, map[string]string, error) {
	if current == 0 || strings.TrimSpace(periodID) != "" {
		return writePeriod(ctx, tx, periodID, date)
	}
	p, err := repo.GetPeriod(ctx, tx, current)
	if err != nil {
		return p, nil, err
	}
	if m := dateErr(date, p); m != "" {
		return p, map[string]string{"date": m}, nil
	}
	return p, nil, nil
}

// productFor accepts an active product, or the row's current product even if it was deactivated since.
func (s *Service) productFor(ctx context.Context, db repo.DB, id string, current int64) (models.Product, bool) {
	if pid, ok := parseID(id); ok && pid == current {
		p, err := repo.GetProduct(ctx, db, pid)
		return p, err == nil
	}
	return s.activeProduct(ctx, db, id)
}

func (s *Service) savePurchase(ctx context.Context, tx pgx.Tx, actor string, in PurchaseInput, force bool, editID int64) (models.Purchase, error) {
	var out models.Purchase
	err := func() error {
		var before models.Purchase
		if editID > 0 {
			var err error
			if before, err = repo.GetPurchase(ctx, tx, editID); err != nil {
				return notFound(err)
			}
		}
		date := strings.TrimSpace(in.Date)
		open, e, err := rowPeriod(ctx, tx, in.PeriodID, date, before.PeriodID)
		if err != nil {
			return err
		}
		if e == nil {
			e = map[string]string{}
		}
		var prod models.Product
		if blank := strings.TrimSpace(in.ProductID); (blank == "" || blank == "0") && (in.noProduct || editID > 0 && before.ProductID == 0) {
			// stays without a product until the owner picks one
		} else if p, ok := s.productFor(ctx, tx, in.ProductID, before.ProductID); ok {
			prod = p
		} else {
			e["productId"] = "Chọn sản phẩm có trong danh mục."
		}
		price, pOK := in.Price.int()
		if !pOK || price <= 0 {
			e["price"] = "Giá nhập phải là số nguyên lớn hơn 0."
		}
		qty, qOK := in.Qty.int()
		if !qOK || qty < 1 {
			e["qty"] = "Số lượng phải là số nguyên từ 1 trở lên."
		}
		disc := int64(0)
		if !in.Discount.empty() {
			v, ok := in.Discount.int()
			if !ok || v < 0 {
				e["discount"] = "Giảm giá phải là số nguyên ≥ 0."
			}
			disc = v
		}
		if e["discount"] == "" && e["price"] == "" && disc > price {
			e["discount"] = "Giảm giá không được lớn hơn giá nhập." // E4
		}
		link, tracking := strings.TrimSpace(in.Link), strings.TrimSpace(in.Tracking)
		if link != "" && !reLink.MatchString(link) {
			e["link"] = "Link phải bắt đầu bằng http:// hoặc https://"
		}
		if tracking != "" && !reTracking.MatchString(tracking) {
			e["tracking"] = "Mã vận đơn chỉ gồm chữ, số và dấu gạch."
		}
		source := in.Source
		if source == "" {
			source = "REGULAR"
		}
		if source != "REGULAR" && source != "BULK" {
			e["source"] = "Nguồn phải là Nhập hàng hoặc Nhập lô lớn."
		}
		if len(e) > 0 {
			return &ValidationError{Fields: e}
		}

		if !force { // E5, E6: warn, allow after confirmation
			var w []string
			if link != "" {
				same, err := repo.PurchasesByLink(ctx, tx, link)
				if err != nil {
					return err
				}
				same = without(same, editID)
				if len(same) > 0 {
					w = append(w, fmt.Sprintf("Link này đã có ở dòng số %d (kỳ %s).", same[0].STT, same[0].PeriodLabel))
				}
			}
			if tracking != "" {
				same, err := repo.PurchasesByTracking(ctx, tx, tracking)
				if err != nil {
					return err
				}
				same = without(same, editID)
				allMerged := true
				refs := make([]string, 0, len(same))
				for _, r := range same {
					allMerged = allMerged && r.Merged
					refs = append(refs, fmt.Sprintf("%d (kỳ %s)", r.STT, r.PeriodLabel))
				}
				if len(same) > 0 && !(in.Merged && allMerged) {
					w = append(w, fmt.Sprintf("Mã vận đơn đã có ở dòng số %s. Nếu các món về chung một kiện, đánh dấu “Đơn gộp” ở tất cả các dòng.", strings.Join(refs, ", ")))
				}
			}
			if len(w) > 0 {
				return &WarningError{Warnings: w}
			}
		}

		row := repo.PurchaseRow{
			PeriodID: open.ID, Source: source, Date: ptr(date), Link: ptr(link), ProductID: prod.ID,
			Price: price, Qty: int(qty), Discount: disc, Tracking: ptr(tracking), Note: strings.TrimSpace(in.Note),
		}
		if in.Merged { // BR-06: rows of one shipment share a merge_group
			g := tracking
			if editID > 0 && tracking == before.Tracking { // keep the group of a split combined order
				prev, err := repo.PurchaseMergeGroup(ctx, tx, editID)
				if err != nil {
					return err
				}
				if prev != "" {
					g = prev
				}
			}
			row.MergeGroup = &g
		}
		if editID > 0 {
			row.Checked, row.Reviewed = before.Checked, before.Reviewed
			if err := repo.UpdatePurchase(ctx, tx, editID, row); err != nil {
				return err
			}
			if out, err = repo.GetPurchase(ctx, tx, editID); err != nil {
				return err
			}
			return repo.Audit(ctx, tx, "purchase", editID, "update", actor, before, out)
		}
		id, err := repo.InsertPurchase(ctx, tx, row)
		if err != nil {
			return err
		}
		out, err = repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "purchase", id, "create", actor, nil, out)
	}()
	return out, err
}

func without(rows []models.Purchase, id int64) []models.Purchase {
	out := rows[:0:0]
	for _, r := range rows {
		if r.ID != id {
			out = append(out, r)
		}
	}
	return out
}

// SetPurchaseFlags updates "checked" (received, GD-03), "reviewed" (seller rated) and/or the tracking number, which
// often arrives after the row was written. Any period, closed or open.
func (s *Service) SetPurchaseFlags(ctx context.Context, actor string, id int64, checked, reviewed *bool, tracking *string) (models.Purchase, error) {
	if tracking != nil {
		if t := strings.TrimSpace(*tracking); t != "" && !reTracking.MatchString(t) {
			return models.Purchase{}, &ValidationError{Fields: map[string]string{"tracking": "Mã vận đơn chỉ gồm chữ, số và dấu gạch."}}
		}
	}
	var out models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		c, r := before.Checked, before.Reviewed
		if checked != nil {
			c = *checked
		}
		if reviewed != nil {
			r = *reviewed
		}
		if err := repo.SetPurchaseFlags(ctx, tx, id, c, r); err != nil {
			return err
		}
		if tracking != nil {
			if t := strings.TrimSpace(*tracking); t == "" {
				_, err = tx.Exec(ctx, `UPDATE purchases SET tracking_no = NULL, updated_at = now() WHERE id = $1`, id)
			} else {
				err = repo.SetPurchaseTracking(ctx, tx, id, t)
			}
			if err != nil {
				return err
			}
		}
		out, err = repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "purchase", id, "update", actor, before, out)
	})
	return out, err
}

func (s *Service) DeletePurchase(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if err := repo.DeletePurchase(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "purchase", id, "delete", actor, before, nil)
	})
}

func (s *Service) AddSale(ctx context.Context, actor string, in SaleInput, force bool) (models.Sale, error) {
	return s.saveSale(ctx, actor, in, force, 0)
}

// UpdateSale replaces every field of a sale, in any period; later periods follow.
func (s *Service) UpdateSale(ctx context.Context, actor string, id int64, in SaleInput, force bool) (models.Sale, error) {
	return s.saveSale(ctx, actor, in, force, id)
}

func (s *Service) saveSale(ctx context.Context, actor string, in SaleInput, force bool, editID int64) (models.Sale, error) {
	var out models.Sale
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var before models.Sale
		if editID > 0 {
			var err error
			if before, err = repo.GetSale(ctx, tx, editID); err != nil {
				return notFound(err)
			}
		}
		date := strings.TrimSpace(in.Date)
		open, e, err := rowPeriod(ctx, tx, in.PeriodID, date, before.PeriodID)
		if err != nil {
			return err
		}
		if e == nil {
			e = map[string]string{}
		}
		prod, ok := s.productFor(ctx, tx, in.ProductID, before.ProductID)
		if !ok {
			e["productId"] = "Chọn sản phẩm có trong danh mục."
		}
		qty, qOK := in.Qty.int()
		if !qOK || qty < 1 {
			e["qty"] = "Số lượng phải là số nguyên từ 1 trở lên."
		}
		price, pOK := in.Price.int()
		if !pOK || price < 0 {
			e["price"] = "Đơn giá phải là số nguyên ≥ 0 (0 = bóc hàng)."
		}
		ship := int64(0)
		if !in.Ship.empty() {
			v, ok := in.Ship.int()
			if !ok || v < 0 {
				e["ship"] = "Phí ship phải là số nguyên ≥ 0."
			}
			ship = v
		}
		if len(e) > 0 {
			return &ValidationError{Fields: e}
		}

		if !force {
			var w []string
			// 0¥ = opened stock ("bóc hàng"): confirm so a forgotten price is not saved silently
			if price == 0 {
				w = append(w, "Đơn giá 0¥: đơn này chỉ trừ tồn kho (bóc hàng), không tính doanh thu.")
			}
			// BR-09 / E9: warn when stock would go negative; never block (GD-05)
			var current int
			err := tx.QueryRow(ctx, `SELECT current FROM stock_by_period WHERE period_id = $1 AND product_id = $2`, open.ID, prod.ID).Scan(&current)
			if err != nil {
				return err
			}
			if editID > 0 && before.PeriodID == open.ID && before.ProductID == prod.ID {
				current += before.Qty // the edited row's own quantity is already taken out
			}
			if current-int(qty) < 0 {
				w = append(w, fmt.Sprintf("Chỉ còn %d cái “%s”, bạn đang bán %d cái. Tồn kho sẽ âm %d cái.",
					current, prod.Name, qty, int(qty)-current))
			}
			if len(w) > 0 {
				return &WarningError{Warnings: w}
			}
		}
		note := strings.TrimSpace(in.Note)
		if price == 0 && note == "" {
			note = OpenedStockNote
		}

		row := repo.SaleRow{
			PeriodID: open.ID, Date: ptr(date), ProductID: prod.ID, Qty: int(qty), Price: price, Ship: ship,
			Customer: strings.TrimSpace(in.Customer), Note: note,
		}
		if editID > 0 {
			if err := repo.UpdateSale(ctx, tx, editID, row); err != nil {
				return err
			}
			if out, err = repo.GetSale(ctx, tx, editID); err != nil {
				return err
			}
			return repo.Audit(ctx, tx, "sale", editID, "update", actor, before, out)
		}
		id, err := repo.InsertSale(ctx, tx, row)
		if err != nil {
			return err
		}
		out, err = repo.GetSale(ctx, tx, id)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "sale", id, "create", actor, nil, out)
	})
	return out, err
}

func (s *Service) DeleteSale(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetSale(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if err := repo.DeleteSale(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "sale", id, "delete", actor, before, nil)
	})
}
