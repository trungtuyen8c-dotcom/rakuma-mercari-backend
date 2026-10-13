package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

func (s *Service) cardNameErr(ctx context.Context, db repo.DB, name string, selfID int64) string {
	switch n := strings.TrimSpace(name); {
	case n == "":
		return "Tên thẻ không được để trống."
	case len([]rune(n)) > 60:
		return "Tên thẻ tối đa 60 ký tự."
	}
	taken, err := repo.CardNameTaken(ctx, db, name, selfID)
	if err != nil || taken {
		return "Đã có thẻ tên này."
	}
	return ""
}

// AddCard registers a payment card (JPY) or a point wallet (PT). Its name must be exactly the name used in the other
// projects sharing it. No VND card: Rakuma purchases are in yen and a card in another currency would need a rate.
func (s *Service) AddCard(ctx context.Context, actor, name, currency string) (models.Card, error) {
	var out models.Card
	if currency == "" {
		currency = "JPY"
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		e := map[string]string{}
		if m := s.cardNameErr(ctx, tx, name, 0); m != "" {
			e["name"] = m
		}
		if currency != "JPY" && currency != "PT" {
			e["currency"] = "Chọn thẻ yên (JPY) hoặc ví point (PT)."
		}
		if len(e) > 0 {
			return &ValidationError{Fields: e}
		}
		id, err := repo.InsertCard(ctx, tx, name, currency)
		if err != nil {
			return err
		}
		if out, err = repo.GetCard(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "card", id, "create", actor, nil, out)
	})
	return out, err
}

type CardPatch struct {
	Name    *string `json:"name"`
	Default *bool   `json:"default"`
	Active  *bool   `json:"active"`
}

// UpdateCard renames, hides or makes a card the default. Renaming changes the name on every ledger line, since the
// ledger is derived from the purchases; a hidden card cannot be the default.
func (s *Service) UpdateCard(ctx context.Context, actor string, id int64, p CardPatch) (models.Card, error) {
	var out models.Card
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetCard(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		c := before
		if p.Name != nil {
			if m := s.cardNameErr(ctx, tx, *p.Name, id); m != "" {
				return &ValidationError{Fields: map[string]string{"name": m}}
			}
			c.Name = *p.Name
		}
		if p.Active != nil {
			c.Active = *p.Active
		}
		if p.Default != nil {
			c.Default = *p.Default
		}
		if !c.Active {
			c.Default = false
		}
		if c.Default && c.Currency != "JPY" {
			return &ValidationError{Fields: map[string]string{"default": "Ví point không làm thẻ mặc định được."}}
		}
		if err := repo.UpdateCard(ctx, tx, c); err != nil {
			return err
		}
		if out, err = repo.GetCard(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "card", id, "update", actor, before, out)
	})
	return out, err
}

// DeleteCard works only for a card no purchase uses; otherwise it can be hidden.
func (s *Service) DeleteCard(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetCard(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if before.TxCount > 0 {
			return &ConflictError{Msg: "Thẻ đã dùng cho dòng nhập, chỉ có thể ẩn."}
		}
		if err := repo.DeleteCard(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "card", id, "delete", actor, before, nil)
	})
}

// CardLedger returns this project's charges on the shared cards (see models.LedgerRow).
func (s *Service) CardLedger(ctx context.Context, from, to, card string) ([]models.LedgerRow, error) {
	e := map[string]string{}
	for k, v := range map[string]string{"from": from, "to": to} {
		if _, err := time.Parse("2006-01-02", v); v != "" && err != nil {
			e[k] = "Ngày phải có dạng YYYY-MM-DD."
		}
	}
	if len(e) > 0 {
		return nil, &ValidationError{Fields: e}
	}
	return repo.CardLedger(ctx, s.Pool, from, to, card)
}

// cardFor resolves a purchase's card (currency JPY) or point wallet (PT): nil keeps the row's current one (or none on a
// new row), "" or "0" clears it, otherwise an active one (or the row's current one, even if hidden since).
func cardFor(ctx context.Context, db repo.DB, in *string, current int64, currency string) (int64, string) {
	bad := "Chọn thẻ có trong danh sách."
	if currency == "PT" {
		bad = "Chọn ví point có trong danh sách."
	}
	if in == nil {
		return current, ""
	}
	v := strings.TrimSpace(*in)
	if v == "" || v == "0" {
		return 0, ""
	}
	id, ok := parseID(v)
	if !ok {
		return 0, bad
	}
	c, err := repo.GetCard(ctx, db, id)
	if err != nil || !c.Active && id != current || c.Currency != currency {
		return 0, bad
	}
	return id, ""
}

// LedgerTotal is a card's charges added up over one day, week (Monday to Sunday) or month, for checking against the
// card statement and the other projects.
type LedgerTotal struct {
	Period   string `json:"period"` // first day of the day/week/month, YYYY-MM-DD
	Wallet   string `json:"wallet"`
	Currency string `json:"currency"`
	Count    int    `json:"count"`
	Amount   int64  `json:"amount"` // signed, like the ledger rows
}

// CardLedgerTotals groups the ledger by day, week or month and card, oldest first.
func (s *Service) CardLedgerTotals(ctx context.Context, group, from, to, card string) ([]LedgerTotal, error) {
	if group != "day" && group != "week" && group != "month" {
		return nil, &ValidationError{Fields: map[string]string{"group": "Chọn day, week hoặc month."}}
	}
	rows, err := s.CardLedger(ctx, from, to, card)
	if err != nil {
		return nil, err
	}
	out := []LedgerTotal{}
	at := map[string]int{}
	for _, r := range rows {
		d, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			return nil, err
		}
		switch group {
		case "week":
			d = d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
		case "month":
			d = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
		k := d.Format("2006-01-02") + "|" + r.Wallet
		i, ok := at[k]
		if !ok {
			i = len(out)
			at[k] = i
			out = append(out, LedgerTotal{Period: d.Format("2006-01-02"), Wallet: r.Wallet, Currency: r.Currency})
		}
		out[i].Count++
		out[i].Amount += r.Amount
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Period != out[b].Period {
			return out[a].Period < out[b].Period
		}
		return out[a].Wallet < out[b].Wallet
	})
	return out, nil
}

// onlyPointWallet returns the active point wallet when there is exactly one, else 0.
func onlyPointWallet(ctx context.Context, db repo.DB) (int64, error) {
	cards, err := repo.ListCards(ctx, db)
	if err != nil {
		return 0, err
	}
	var id int64
	for _, c := range cards {
		if c.Currency == "PT" && c.Active {
			if id != 0 {
				return 0, nil
			}
			id = c.ID
		}
	}
	return id, nil
}
