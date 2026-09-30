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
}

type SaleInput struct {
	ProductID string `json:"productId"`
	Date      string `json:"date"`
	Qty       Flex   `json:"qty"`
	Price     Flex   `json:"price"`
	Ship      Flex   `json:"ship"`
	Customer  string `json:"customer"`
	Note      string `json:"note"`
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
		open, err := repo.OpenPeriod(ctx, tx, false)
		if err != nil {
			return err
		}
		e := map[string]string{}
		prod, ok := s.activeProduct(ctx, tx, in.ProductID)
		if !ok {
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
		date := strings.TrimSpace(in.Date)
		if m := dateErr(date, open); m != "" {
			e["date"] = m
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
				if len(same) > 0 {
					w = append(w, fmt.Sprintf("Link này đã có ở dòng số %d (kỳ %s).", same[0].STT, same[0].PeriodLabel))
				}
			}
			if tracking != "" {
				same, err := repo.PurchasesByTracking(ctx, tx, tracking)
				if err != nil {
					return err
				}
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
			row.MergeGroup = &g
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
	})
	return out, err
}

// SetPurchaseFlags updates "checked" (received, GD-03) and/or "reviewed" (seller rated). Closed periods are read-only.
func (s *Service) SetPurchaseFlags(ctx context.Context, actor string, id int64, checked, reviewed *bool) (models.Purchase, error) {
	var out models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if before.Locked {
			return ErrLocked
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
		if before.Locked {
			return ErrLocked
		}
		if err := repo.DeletePurchase(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "purchase", id, "delete", actor, before, nil)
	})
}

func (s *Service) AddSale(ctx context.Context, actor string, in SaleInput, force bool) (models.Sale, error) {
	var out models.Sale
	err := s.tx(ctx, func(tx pgx.Tx) error {
		open, err := repo.OpenPeriod(ctx, tx, false)
		if err != nil {
			return err
		}
		e := map[string]string{}
		prod, ok := s.activeProduct(ctx, tx, in.ProductID)
		if !ok {
			e["productId"] = "Chọn sản phẩm có trong danh mục."
		}
		qty, qOK := in.Qty.int()
		if !qOK || qty < 1 {
			e["qty"] = "Số lượng phải là số nguyên từ 1 trở lên."
		}
		price, pOK := in.Price.int()
		if !pOK || price <= 0 {
			e["price"] = "Đơn giá phải là số nguyên lớn hơn 0."
		}
		ship := int64(0)
		if !in.Ship.empty() {
			v, ok := in.Ship.int()
			if !ok || v < 0 {
				e["ship"] = "Phí ship phải là số nguyên ≥ 0."
			}
			ship = v
		}
		date := strings.TrimSpace(in.Date)
		if m := dateErr(date, open); m != "" {
			e["date"] = m
		}
		if len(e) > 0 {
			return &ValidationError{Fields: e}
		}

		if !force { // BR-09 / E9: warn when stock would go negative; never block (GD-05)
			var current int
			err := tx.QueryRow(ctx, `SELECT current FROM stock_by_period WHERE period_id = $1 AND product_id = $2`, open.ID, prod.ID).Scan(&current)
			if err != nil {
				return err
			}
			if current-int(qty) < 0 {
				return &WarningError{Warnings: []string{fmt.Sprintf("Chỉ còn %d cái “%s”, bạn đang bán %d cái. Tồn kho sẽ âm %d cái.",
					current, prod.Name, qty, int(qty)-current)}}
			}
		}

		id, err := repo.InsertSale(ctx, tx, repo.SaleRow{
			PeriodID: open.ID, Date: ptr(date), ProductID: prod.ID, Qty: int(qty), Price: price, Ship: ship,
			Customer: strings.TrimSpace(in.Customer), Note: strings.TrimSpace(in.Note),
		})
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
		if before.Locked {
			return ErrLocked
		}
		if err := repo.DeleteSale(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "sale", id, "delete", actor, before, nil)
	})
}
