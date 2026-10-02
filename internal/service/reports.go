package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

// resolvePeriod returns the given period, or the open one when id is 0.
func (s *Service) resolvePeriod(ctx context.Context, db repo.DB, id int64) (models.Period, error) {
	if id == 0 {
		return repo.OpenPeriod(ctx, db, false)
	}
	p, err := repo.GetPeriod(ctx, db, id)
	return p, notFound(err)
}

func (s *Service) Stock(ctx context.Context, periodID int64) (models.Period, []models.StockRow, error) {
	p, err := s.resolvePeriod(ctx, s.Pool, periodID)
	if err != nil {
		return p, nil, err
	}
	rows, err := repo.Stock(ctx, s.Pool, p.ID)
	return p, rows, err
}

// Dashboard (§4.6). Closed periods are recomputed from raw rows and compared with the stored closing figures.
func (s *Service) Dashboard(ctx context.Context, periodID int64) (models.Dashboard, error) {
	var d models.Dashboard
	p, err := s.resolvePeriod(ctx, s.Pool, periodID)
	if err != nil {
		return d, err
	}
	t, err := repo.PeriodTotals(ctx, s.Pool, p.ID)
	if err != nil {
		return d, err
	}
	stock, err := repo.Stock(ctx, s.Pool, p.ID)
	if err != nil {
		return d, err
	}
	d.Period, d.Totals, d.Negatives = p, t, []models.StockRow{}
	for _, r := range stock {
		if r.Current < 0 {
			d.Negatives = append(d.Negatives, r)
		}
	}
	d.Matches = p.Status != "CLOSED" || (p.ClosingCost != nil && p.ClosingRevenue != nil && *p.ClosingCost == t.TotalCost && *p.ClosingRevenue == t.TotalRevenue)
	return d, nil
}

// Analysis (§4.8, BR-15): COGS of the sold units taken LIFO from the latest purchases, over all periods.
func (s *Service) Analysis(ctx context.Context, productID int64) (models.Analysis, error) {
	a := models.Analysis{ProductID: productID, Lots: []models.AnalysisLot{}}
	p, err := repo.GetProduct(ctx, s.Pool, productID)
	if err != nil {
		return a, notFound(err)
	}
	a.ProductName = p.Name
	sales, err := repo.ListSales(ctx, s.Pool, 0)
	if err != nil {
		return a, err
	}
	for _, r := range sales {
		if r.ProductID == productID {
			a.SoldQty += r.Qty
			a.SoldAmount += r.Total
		}
	}
	all, err := repo.ListPurchases(ctx, s.Pool, 0)
	if err != nil {
		return a, err
	}
	var purs []models.Purchase
	for _, r := range all {
		if r.ProductID == productID {
			purs = append(purs, r)
			a.InQty += r.Qty
			a.InAmount += r.Total
		}
	}
	need := a.SoldQty
	for i := len(purs) - 1; i >= 0 && need > 0; i-- {
		r := purs[i]
		take := min(need, r.Qty)
		unit := r.Price - r.Discount
		a.COGS += int64(take) * unit
		need -= take
		a.Lots = append(a.Lots, models.AnalysisLot{PurchaseID: r.ID, Date: r.Date, PeriodLabel: r.PeriodLabel, STT: r.STT,
			UnitCost: unit, Qty: r.Qty, Take: take, Cost: int64(take) * unit})
	}
	a.Uncovered = need
	a.Profit = a.SoldAmount - a.COGS
	return a, nil
}

// NextPeriodLabel: "09/2026" -> "10/2026", "12/2026" -> "01/2027".
func NextPeriodLabel(label string) (string, error) {
	t, err := time.Parse("01/2006", label)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 1, 0).Format("01/2006"), nil
}

// MonthRange returns the first and last day of a "MM/YYYY" period.
func MonthRange(label string) (string, string, error) {
	t, err := time.Parse("01/2006", label)
	if err != nil {
		return "", "", err
	}
	return t.Format("2006-01-02"), t.AddDate(0, 1, -1).Format("2006-01-02"), nil
}

// ClosePeriod (§4.7, BR-14): stores closing totals, opens the next month with those totals as its opening figures,
// and carries each product's current stock into the new period's openings. The old period becomes read-only.
func (s *Service) ClosePeriod(ctx context.Context, actor string) (models.Period, error) {
	var next models.Period
	err := s.tx(ctx, func(tx pgx.Tx) error {
		open, err := repo.OpenPeriod(ctx, tx, true)
		if err != nil {
			if err == pgx.ErrNoRows {
				return &ConflictError{Msg: "Không có kỳ nào đang mở."} // E8
			}
			return err
		}
		t, err := repo.PeriodTotals(ctx, tx, open.ID)
		if err != nil {
			return err
		}
		stock, err := repo.Stock(ctx, tx, open.ID)
		if err != nil {
			return err
		}
		label, err := NextPeriodLabel(open.Label)
		if err != nil {
			return err
		}
		start, end, err := MonthRange(label)
		if err != nil {
			return err
		}
		if err := repo.ClosePeriod(ctx, tx, open.ID, t.TotalCost, t.TotalRevenue); err != nil {
			return err
		}
		id, err := repo.InsertPeriod(ctx, tx, label, start, end, t.TotalCost, t.TotalRevenue)
		if err != nil {
			return err
		}
		for _, r := range stock {
			if err := repo.UpsertOpening(ctx, tx, id, r.ProductID, r.Current); err != nil {
				return err
			}
		}
		next, err = repo.GetPeriod(ctx, tx, id)
		if err != nil {
			return err
		}
		closed, _ := repo.GetPeriod(ctx, tx, open.ID)
		return repo.Audit(ctx, tx, "period", open.ID, "close", actor, open,
			map[string]any{"closed": closed, "next": next, "note": fmt.Sprintf("%d products carried forward", len(stock))})
	})
	return next, err
}

// State bundles everything the web UI renders; the UI derives its views from these rows.
func (s *Service) State(ctx context.Context, user *models.User) (models.State, error) {
	var st models.State
	var err error
	if st.Products, err = repo.ListProducts(ctx, s.Pool); err != nil {
		return st, err
	}
	if st.Purchases, err = repo.ListPurchases(ctx, s.Pool, 0); err != nil {
		return st, err
	}
	if st.Sales, err = repo.ListSales(ctx, s.Pool, 0); err != nil {
		return st, err
	}
	if st.Periods, err = repo.ListPeriods(ctx, s.Pool); err != nil {
		return st, err
	}
	if st.Openings, err = repo.Openings(ctx, s.Pool); err != nil {
		return st, err
	}
	if st.APIKeys, err = repo.ListAPIKeys(ctx, s.Pool); err != nil {
		return st, err
	}
	if st.Settings, err = s.Settings(ctx); err != nil {
		return st, err
	}
	if st.Rakuma, err = repo.ListRakumaOrders(ctx, s.Pool); err != nil {
		return st, err
	}
	st.User = user
	return st, nil
}
