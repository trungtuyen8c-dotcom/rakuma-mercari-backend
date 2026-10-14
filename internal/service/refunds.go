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

func jstToday() string { return time.Now().In(jst).Format("2006-01-02") }

var jst = time.FixedZone("JST", 9*3600)

// cardMonthWarnings returns one warning per card month (card, YYYY-MM-DD) already checked against its statement:
// changing money in it makes the checked total wrong. The owner may go ahead after confirming.
func cardMonthWarnings(ctx context.Context, db repo.DB, lines ...[2]any) ([]string, error) {
	var w []string
	seen := map[string]bool{}
	for _, l := range lines {
		cardID, day := l[0].(int64), l[1].(string)
		if cardID == 0 || len(day) < 7 || seen[fmt.Sprint(cardID, day[:7])] {
			continue
		}
		seen[fmt.Sprint(cardID, day[:7])] = true
		ok, err := repo.IsReconciled(ctx, db, cardID, day)
		if err != nil {
			return nil, err
		}
		if ok {
			c, err := repo.GetCard(ctx, db, cardID)
			if err != nil {
				return nil, err
			}
			w = append(w, fmt.Sprintf("Tháng %s/%s của thẻ %s đã đối soát với sao kê. Thay đổi này làm số đã khớp bị lệch.", day[5:7], day[:4], c.Name))
		}
	}
	return w, nil
}

// SetRefundNote marks a purchase as waiting for a refund (the reason, the expected date…); "" clears it.
func (s *Service) SetRefundNote(ctx context.Context, actor string, id int64, note string) (models.Purchase, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 300 {
		return models.Purchase{}, &ValidationError{Fields: map[string]string{"note": "Ghi chú tối đa 300 ký tự."}}
	}
	var out models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if err := repo.SetPurchaseRefundNote(ctx, tx, id, note); err != nil {
			return err
		}
		if out, err = repo.GetPurchase(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "purchase", id, "refund_note", actor, before, out)
	})
	return out, err
}

type RefundInput struct {
	Date      string `json:"date"`   // the day the money came back; default today (Japan)
	Amount    Flex   `json:"amount"` // default the row total
	Note      string `json:"note"`
	RemoveRow bool   `json:"removeRow"` // full refund, goods not coming: the row leaves purchases and stock
}

// RecordRefund books money paid back to the card of a purchase. The card ledger keeps the charge and adds the refund
// on its own day, as the statement shows them. With removeRow (full refunds only) the purchase row is deleted, so it
// leaves the cost and stock, and its Rakuma order is kept out of the queue.
func (s *Service) RecordRefund(ctx context.Context, actor string, id int64, in RefundInput, force bool) (models.CardRefund, error) {
	var out models.CardRefund
	err := s.tx(ctx, func(tx pgx.Tx) error {
		p, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		e := map[string]string{}
		if p.CardID == 0 {
			e["card"] = "Dòng này chưa ghi thẻ thanh toán. Chọn thẻ cho dòng trước khi ghi hoàn tiền."
		}
		date := strings.TrimSpace(in.Date)
		if date == "" {
			date = jstToday()
		} else if _, err := time.Parse("2006-01-02", date); err != nil {
			e["date"] = "Ngày không hợp lệ."
		}
		// Only the card part comes back to the card; Rakuma returns the points as points
		charged := p.Total - p.Points
		if p.CardID != 0 && charged <= 0 {
			e["card"] = "Dòng này trả hết bằng point, không có tiền thẻ để hoàn."
		}
		amount := charged
		if !in.Amount.empty() {
			v, ok := in.Amount.int()
			if !ok || v <= 0 || v > charged {
				e["amount"] = fmt.Sprintf("Số tiền hoàn phải từ 1 đến %s¥.", money(charged))
			}
			amount = v
		}
		if in.RemoveRow && e["amount"] == "" && amount != charged {
			e["removeRow"] = "Chỉ xoá dòng nhập khi được hoàn đủ tiền."
		}
		if len([]rune(in.Note)) > 300 {
			e["note"] = "Ghi chú tối đa 300 ký tự."
		}
		if len(e) > 0 {
			return &ValidationError{Fields: e}
		}
		if !force {
			w, err := cardMonthWarnings(ctx, tx, [2]any{p.CardID, date})
			if err != nil {
				return err
			}
			if len(w) > 0 {
				return &WarningError{Warnings: w}
			}
		}
		label := fmt.Sprintf("kỳ %s dòng %d", p.PeriodLabel, p.STT)
		if p.ProductName != "" {
			label += " · " + p.ProductName
		}
		if p.Link != "" {
			label += " · " + p.Link
		}
		rid, err := repo.InsertRefund(ctx, tx, models.CardRefund{CardID: p.CardID, ChargeDate: p.ChargeDate, ChargeAmount: charged,
			RefundDate: date, Amount: amount, Label: label, Note: strings.TrimSpace(in.Note)}, p.ID)
		if err != nil {
			return err
		}
		if err := repo.SetPurchaseRefundNote(ctx, tx, id, ""); err != nil {
			return err
		}
		if in.RemoveRow {
			reason := fmt.Sprintf("Hoàn tiền đủ và xoá dòng nhập #%d (kỳ %s dòng %d, %s¥)", id, p.PeriodLabel, p.STT, money(p.Total))
			if err := repo.DismissRakumaForPurchase(ctx, tx, id, actor, "refund_removed", reason); err != nil {
				return err
			}
			if err := repo.DeletePurchase(ctx, tx, id); err != nil {
				return err
			}
			if err := repo.Audit(ctx, tx, "purchase", id, "delete", actor, p, map[string]any{"refundId": rid}); err != nil {
				return err
			}
		}
		if out, err = repo.GetRefund(ctx, tx, rid); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "card_refund", rid, "create", actor, nil, out)
	})
	return out, err
}

// DeleteRefund removes a refund booked by mistake. When its purchase row was removed, the kept charge goes too.
func (s *Service) DeleteRefund(ctx context.Context, actor string, id int64, force bool) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		x, err := repo.GetRefund(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if !force {
			lines := [][2]any{{x.CardID, x.RefundDate}}
			if x.PurchaseID == "" {
				lines = append(lines, [2]any{x.CardID, x.ChargeDate})
			}
			w, err := cardMonthWarnings(ctx, tx, lines...)
			if err != nil {
				return err
			}
			if len(w) > 0 {
				return &WarningError{Warnings: w}
			}
		}
		if err := repo.DeleteRefund(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "card_refund", id, "delete", actor, x, nil)
	})
}

// Reconcile marks a card's month (YYYY-MM) as checked against its statement and keeps the net amount, so a later
// change shows up; later edits in that month ask for confirmation first.
func (s *Service) Reconcile(ctx context.Context, actor string, cardID int64, month string) (models.CardReconciliation, error) {
	var out models.CardReconciliation
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return out, &ValidationError{Fields: map[string]string{"month": "Tháng phải có dạng YYYY-MM."}}
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := repo.GetCard(ctx, tx, cardID); err != nil {
			return notFound(err)
		}
		c, _ := repo.GetCard(ctx, tx, cardID)
		rows, err := repo.CardLedger(ctx, tx, start.Format("2006-01-02"), start.AddDate(0, 1, -1).Format("2006-01-02"), c.Name)
		if err != nil {
			return err
		}
		var net int64
		for _, r := range rows {
			net += r.Amount
		}
		if err := repo.UpsertReconciliation(ctx, tx, cardID, month, net); err != nil {
			return err
		}
		out = models.CardReconciliation{CardID: cardID, Month: month, Amount: net}
		return repo.Audit(ctx, tx, "card_reconciliation", cardID, "reconcile", actor, nil, out)
	})
	return out, err
}

func (s *Service) Unreconcile(ctx context.Context, actor string, cardID int64, month string) error {
	if _, err := time.Parse("2006-01", month); err != nil {
		return &ValidationError{Fields: map[string]string{"month": "Tháng phải có dạng YYYY-MM."}}
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := repo.DeleteReconciliation(ctx, tx, cardID, month); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "card_reconciliation", cardID, "unreconcile", actor, map[string]string{"month": month}, nil)
	})
}

// SetPurchaseCard sets or clears ("") the card that paid a purchase, straight from the table. Moving the charge out of
// or into a card month already checked against the statement asks first.
func (s *Service) SetPurchaseCard(ctx context.Context, actor string, id int64, cardID string, force bool) (models.Purchase, error) {
	var out models.Purchase
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetPurchase(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		cid, m := cardFor(ctx, tx, &cardID, before.CardID, "JPY")
		if m != "" {
			return &ValidationError{Fields: map[string]string{"cardId": m}}
		}
		if cid == before.CardID {
			out = before
			return nil
		}
		if !force {
			w, err := cardMonthWarnings(ctx, tx, [2]any{before.CardID, before.ChargeDate}, [2]any{cid, before.ChargeDate})
			if err != nil {
				return err
			}
			if len(w) > 0 {
				return &WarningError{Warnings: w}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE purchases SET card_id = NULLIF($2, 0), updated_at = now() WHERE id = $1`, id, cid); err != nil {
			return err
		}
		if out, err = repo.GetPurchase(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "purchase", id, "card", actor, before, out)
	})
	return out, err
}
