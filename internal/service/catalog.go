package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

func (s *Service) nameErr(ctx context.Context, db repo.DB, name string, selfID int64) string {
	if strings.TrimSpace(name) == "" {
		return "Tên sản phẩm không được để trống."
	}
	taken, err := repo.ProductNameTaken(ctx, db, name, selfID)
	if err != nil || taken {
		return "Sản phẩm này đã có trong danh mục." // BR-01 / E7
	}
	return ""
}

// AddProduct also creates the stock row for the open period with opening = 0 (§4.1 step 4, AC-14).
func (s *Service) AddProduct(ctx context.Context, actor, name string) (models.Product, error) {
	var out models.Product
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if m := s.nameErr(ctx, tx, name, 0); m != "" {
			return &ValidationError{Fields: map[string]string{"name": m}}
		}
		id, err := repo.InsertProduct(ctx, tx, name)
		if err != nil {
			return err
		}
		open, err := repo.OpenPeriod(ctx, tx, false)
		if err != nil {
			return err
		}
		if err := repo.UpsertOpening(ctx, tx, open.ID, id, 0); err != nil {
			return err
		}
		out, err = repo.GetProduct(ctx, tx, id)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "product", id, "create", actor, nil, out)
	})
	return out, err
}

type ProductPatch struct {
	Name   *string `json:"name"`
	Active *bool   `json:"active"`
}

func (s *Service) UpdateProduct(ctx context.Context, actor string, id int64, p ProductPatch) (models.Product, error) {
	var out models.Product
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetProduct(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		name, active := before.Name, before.Active
		if p.Name != nil {
			if m := s.nameErr(ctx, tx, *p.Name, id); m != "" {
				return &ValidationError{Fields: map[string]string{"name": m}}
			}
			name = *p.Name
		}
		if p.Active != nil {
			active = *p.Active
		}
		if err := repo.UpdateProduct(ctx, tx, id, name, active); err != nil {
			return err
		}
		out, err = repo.GetProduct(ctx, tx, id)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "product", id, "update", actor, before, out)
	})
	return out, err
}

// DeleteProduct only works for products without transactions; others can only be hidden (E11).
func (s *Service) DeleteProduct(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetProduct(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if before.TxCount > 0 {
			return &ConflictError{Msg: "Sản phẩm đã có giao dịch, chỉ có thể ẩn."}
		}
		if err := repo.DeleteProduct(ctx, tx, id); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "product", id, "delete", actor, before, nil)
	})
}

// SetOpening edits the open period's opening stock after a stock count (§4.5 step 3).
func (s *Service) SetOpening(ctx context.Context, actor string, productID int64, qty Flex) error {
	v, ok := qty.int()
	if !ok || v < 0 {
		return &ValidationError{Fields: map[string]string{"qty": "Tồn đầu kỳ phải là số nguyên ≥ 0."}}
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := repo.GetProduct(ctx, tx, productID); err != nil {
			return notFound(err)
		}
		open, err := repo.OpenPeriod(ctx, tx, true)
		if err != nil {
			return err
		}
		var before int
		_ = tx.QueryRow(ctx, `SELECT qty FROM stock_openings WHERE period_id = $1 AND product_id = $2`, open.ID, productID).Scan(&before)
		if err := repo.UpsertOpening(ctx, tx, open.ID, productID, int(v)); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "stock_opening", fmt.Sprintf("%d:%d", open.ID, productID), "update", actor,
			map[string]int{"qty": before}, map[string]int{"qty": int(v)})
	})
}

type SettingsInput struct {
	Rate       Flex `json:"rate"`
	ServiceFee Flex `json:"serviceFee"`
	Shipping   Flex `json:"shipping"`
	Tax        Flex `json:"tax"`
}

func (s *Service) Settings(ctx context.Context) (models.Settings, error) {
	st, err := repo.GetSettings(ctx, s.Pool)
	st.OwnerEmail = s.Cfg.OwnerEmail
	return st, err
}

func (s *Service) SaveSettings(ctx context.Context, actor string, in SettingsInput) (models.Settings, error) {
	e := map[string]string{}
	rate, ok := in.Rate.float()
	if !ok || rate <= 0 {
		e["rate"] = "Tỷ giá phải là số lớn hơn 0."
	}
	fee, ok := in.ServiceFee.int()
	if !ok || fee < 0 {
		e["serviceFee"] = "Phí phải là số nguyên ≥ 0."
	}
	ship, ok := in.Shipping.int()
	if !ok || ship < 0 {
		e["shipping"] = "Phí phải là số nguyên ≥ 0."
	}
	tax, ok := in.Tax.float()
	if !ok || tax < 0 || tax > 1 {
		e["tax"] = "Thuế nhập khẩu từ 0 đến 1 (0,1 = 10%)."
	}
	if len(e) > 0 {
		return models.Settings{}, &ValidationError{Fields: e}
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetSettings(ctx, tx)
		if err != nil {
			return err
		}
		vals := map[string]string{
			repo.KeyExchangeRate:  strconv.FormatFloat(rate, 'f', -1, 64),
			repo.KeyServiceFee:    strconv.FormatInt(fee, 10),
			repo.KeyIntlShipping:  strconv.FormatInt(ship, 10),
			repo.KeyImportTaxRate: strconv.FormatFloat(tax, 'f', -1, 64),
		}
		for k, v := range vals {
			if err := repo.SetSetting(ctx, tx, k, v); err != nil {
				return err
			}
		}
		after, err := repo.GetSettings(ctx, tx)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "settings", "global", "update", actor, before, after)
	})
	if err != nil {
		return models.Settings{}, err
	}
	return s.Settings(ctx)
}
