package service

import (
	"context"
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

// AddCard registers a payment card. Its name must be exactly the name used in the other projects sharing it.
// Only yen cards for now: Rakuma purchases are in yen and a card in another currency would need a rate.
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
		if currency != "JPY" {
			e["currency"] = "Hiện chỉ hỗ trợ thẻ yên (JPY)."
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

// cardFor resolves a purchase's card: nil keeps the row's current card (or none on a new row), "" or "0" clears it,
// otherwise an active card (or the row's current one, even if hidden since).
func cardFor(ctx context.Context, db repo.DB, in *string, current int64) (int64, string) {
	if in == nil {
		return current, ""
	}
	v := strings.TrimSpace(*in)
	if v == "" || v == "0" {
		return 0, ""
	}
	id, ok := parseID(v)
	if !ok {
		return 0, "Chọn thẻ có trong danh sách."
	}
	c, err := repo.GetCard(ctx, db, id)
	if err != nil || !c.Active && id != current {
		return 0, "Chọn thẻ có trong danh sách."
	}
	return id, ""
}
