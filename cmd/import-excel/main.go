// import-excel loads rakuma_t7.xlsx into an empty database and checks the totals (spec §15.3).
//
//	import-excel -inspect rakuma_t7.xlsx   # print sheets and header rows to confirm the column layout
//	import-excel rakuma_t7.xlsx            # import; rolls back if totals do not match the expected figures
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/config"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/db"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/importer"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

func main() {
	lay := importer.DefaultLayout()
	inspect := flag.Bool("inspect", false, "only print sheets and header rows")
	flag.StringVar(&lay.PurDate, "pur-date-col", lay.PurDate, "purchase order-date column (unconfirmed)")
	flag.StringVar(&lay.PurCheck, "pur-check-col", lay.PurCheck, `purchase "check" column (unconfirmed)`)
	flag.StringVar(&lay.PurReview, "pur-review-col", lay.PurReview, `purchase "đánh giá" column (unconfirmed)`)
	flag.StringVar(&lay.PurNote, "pur-note-col", lay.PurNote, "purchase note column (unconfirmed)")
	flag.StringVar(&lay.SaleDate, "sale-date-col", lay.SaleDate, "sale date column (unconfirmed)")
	flag.StringVar(&lay.SaleCustomer, "sale-customer-col", lay.SaleCustomer, `sale "Ghi chú"/customer column (unconfirmed)`)
	exp := importer.Expect{}
	flag.Int64Var(&exp.TotalCost, "expect-cost", 69824506, "expected total cost after import")
	flag.Int64Var(&exp.TotalRevenue, "expect-revenue", 69815821, "expected total revenue after import")
	flag.IntVar(&exp.Stock, "expect-stock", 560, "expected total stock after import")
	noCheck := flag.Bool("no-check", false, "commit even if totals differ from the expected figures")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: import-excel [flags] rakuma_t7.xlsx")
		flag.PrintDefaults()
		os.Exit(2)
	}
	path := flag.Arg(0)
	if *inspect {
		if err := importer.Inspect(path, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
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
	if err := service.EnsureOpenPeriod(ctx, pool, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "period:", err)
		os.Exit(1)
	}
	var want *importer.Expect
	if !*noCheck {
		want = &exp
	}
	rep, err := importer.Run(ctx, pool, path, lay, want)
	for _, n := range rep.Notes {
		fmt.Println("note:", n)
	}
	for _, m := range rep.TotalMismatches {
		fmt.Println("total mismatch:", m)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "import failed:", err)
		os.Exit(1)
	}
	fmt.Printf("OK: %d sản phẩm, %d dòng nhập, %d dòng lô lớn, %d đơn bán\n", rep.Products, rep.Regular, rep.Bulk, rep.Sales)
	fmt.Printf("Vốn kỳ trước %d, doanh thu kỳ trước %d\n", rep.OpeningCost, rep.OpeningRevenue)
	fmt.Printf("Tổng vốn %d, tổng doanh thu %d, tổng tồn %d\n", rep.TotalCost, rep.TotalRevenue, rep.Stock)
}
