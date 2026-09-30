// seed-demo fills an EMPTY database with the UI prototype's sample data (periods 06-09/2026) for local development.
// Never run it against real data: it refuses when products or periods with transactions already exist.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/config"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/db"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

//go:embed demo.json
var demoJSON []byte

type demo struct {
	Products []string `json:"products"`
	Periods  []struct {
		ID, Label, Start, End, Status string
		OpeningCost, OpeningRevenue    int64
		ClosingCost, ClosingRevenue    *int64
		ClosedAt                       *string
	} `json:"periods"`
	Openings  map[string]map[string]int `json:"openings"`
	Purchases []struct {
		PeriodID, Source, Date, Link, ProductID, Tracking, Note string
		Price, Discount                                         int64
		Qty                                                     int
		Merged, Checked, Reviewed                               bool
	} `json:"purchases"`
	Sales []struct {
		PeriodID, Date, ProductID, Customer, Note string
		Qty                                       int
		Price, Ship                               int64
	} `json:"sales"`
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func main() {
	var d demo
	if err := json.Unmarshal(demoJSON, &d); err != nil {
		panic(err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, config.Load().DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var used int
		if err := tx.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM products) + (SELECT COUNT(*) FROM purchases) + (SELECT COUNT(*) FROM sales)`).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("database đã có dữ liệu, không nạp dữ liệu mẫu")
		}
		// The server may have created an empty open period on start; replace it.
		if _, err := tx.Exec(ctx, `DELETE FROM stock_openings; DELETE FROM periods`); err != nil {
			return err
		}
		prod := map[string]int64{}
		for i, name := range d.Products {
			id, err := repo.InsertProduct(ctx, tx, name)
			if err != nil {
				return err
			}
			prod[fmt.Sprintf("p%d", i+1)] = id
		}
		per := map[string]int64{}
		for _, p := range d.Periods {
			var id int64
			err := tx.QueryRow(ctx, `INSERT INTO periods (label, start_date, end_date, status, opening_cost, opening_revenue, closing_cost, closing_revenue, closed_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date) RETURNING id`,
				p.Label, p.Start, p.End, p.Status, p.OpeningCost, p.OpeningRevenue, p.ClosingCost, p.ClosingRevenue, p.ClosedAt).Scan(&id)
			if err != nil {
				return err
			}
			per[p.ID] = id
		}
		for pk, m := range d.Openings {
			for prk, q := range m {
				if err := repo.UpsertOpening(ctx, tx, per[pk], prod[prk], q); err != nil {
					return err
				}
			}
		}
		for _, r := range d.Purchases {
			row := repo.PurchaseRow{PeriodID: per[r.PeriodID], Source: r.Source, Date: opt(r.Date), Link: opt(r.Link), ProductID: prod[r.ProductID],
				Price: r.Price, Qty: r.Qty, Discount: r.Discount, Tracking: opt(r.Tracking), Checked: r.Checked, Reviewed: r.Reviewed, Note: r.Note}
			if r.Merged {
				g := r.Tracking
				row.MergeGroup = &g
			}
			if _, err := repo.InsertPurchase(ctx, tx, row); err != nil {
				return err
			}
		}
		for _, r := range d.Sales {
			if _, err := repo.InsertSale(ctx, tx, repo.SaleRow{PeriodID: per[r.PeriodID], Date: opt(r.Date), ProductID: prod[r.ProductID],
				Qty: r.Qty, Price: r.Price, Ship: r.Ship, Customer: r.Customer, Note: r.Note}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed-demo:", err)
		os.Exit(1)
	}
	fmt.Printf("OK: %d sản phẩm, %d kỳ, %d dòng nhập, %d đơn bán\n", len(d.Products), len(d.Periods), len(d.Purchases), len(d.Sales))
}
