package importer_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/db"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/importer"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

// buildWorkbook mimics rakuma_t7.xlsx: same sheet names, data from row 4, columns per spec §15.1.
func buildWorkbook(t *testing.T) string {
	t.Helper()
	f := excelize.NewFile()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	sheets := []string{"⚙️ CÀI ĐẶT", "📋 DANH MỤC SP", "📦 NHẬP HÀNG", "box 30th", "💰 BÁN HÀNG", "🗃️ TỒN KHO"}
	for _, s := range sheets {
		_, err := f.NewSheet(s)
		must(err)
	}
	must(f.DeleteSheet("Sheet1"))
	set := func(sheet, cell string, v any) { must(f.SetCellValue(sheet, cell, v)) }

	set("⚙️ CÀI ĐẶT", "B9", 1000)
	set("⚙️ CÀI ĐẶT", "B10", 2000)
	for i, n := range []string{"ninja", "30th Celebration", "dream"} {
		set("📋 DANH MỤC SP", "B"+itoa(4+i), n)
	}
	// dream is missing from the stock sheet on purpose (§13)
	set("🗃️ TỒN KHO", "B4", "ninja")
	set("🗃️ TỒN KHO", "C4", 3)
	set("🗃️ TỒN KHO", "B5", "30th Celebration")
	set("🗃️ TỒN KHO", "C5", 1)

	// Purchases: C link, D product, E price, F qty, G discount, H total, I tracking (number), J check, K review
	pur := [][]any{
		{"https://jp.mercari.com/item/m1", "ninja", 10000, 2, nil, 20000, 622955048670, "ok", "Done"},
		{"https://jp.mercari.com/item/m2", "ninja", 17000, 1, 1190, 15810, 622955048670, "Ok", "done "},
		{nil, "dream", 500, 1, nil, 500, nil, nil, "lấy ở tabata"},
	}
	for i, r := range pur {
		row := itoa(4 + i)
		for j, col := range []string{"C", "D", "E", "F", "G", "H", "I", "J", "K"} {
			if r[j] != nil {
				set("📦 NHẬP HÀNG", col+row, r[j])
			}
		}
	}
	set("📦 NHẬP HÀNG", "B4", time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC))
	set("box 30th", "C4", "https://item.fril.jp/abc")
	set("box 30th", "D4", "30th Celebration")
	set("box 30th", "E4", 5000)
	set("box 30th", "F4", 10)
	set("box 30th", "H4", 50000)

	// Sales: B date, C product, D qty, E price, F total, G ship, H customer
	set("💰 BÁN HÀNG", "B4", "2026-07-15")
	set("💰 BÁN HÀNG", "C4", "30th Celebration")
	set("💰 BÁN HÀNG", "D4", 4)
	set("💰 BÁN HÀNG", "E4", 6800)
	set("💰 BÁN HÀNG", "F4", 27200)
	set("💰 BÁN HÀNG", "H4", "masai")
	set("💰 BÁN HÀNG", "C5", "dream")
	set("💰 BÁN HÀNG", "D5", 2)
	set("💰 BÁN HÀNG", "E5", 1000)
	set("💰 BÁN HÀNG", "F5", 2000)
	// 0¥ row = opened stock ("bóc hàng"): reduces stock, no revenue
	set("💰 BÁN HÀNG", "C6", "dream")
	set("💰 BÁN HÀNG", "D6", 1)
	set("💰 BÁN HÀNG", "E6", 0)
	set("💰 BÁN HÀNG", "F6", 0)
	set("💰 BÁN HÀNG", "B6", 16) // day number only -> month of the row above (2026-07)

	path := filepath.Join(t.TempDir(), "rakuma_t7.xlsx")
	must(f.SaveAs(path))
	return path
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestImportReconciles(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureOpenPeriod(ctx, pool, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	path := buildWorkbook(t)

	// Cost = 1000 + 20000 + 15810 + 500 + 50000; revenue = 2000 + 27200 + 2000; stock = (3+3-0) + (1+10-4) + (0+1-2-1)
	want := importer.Expect{TotalCost: 87310, TotalRevenue: 31200, Stock: 11}

	// A wrong expectation rolls everything back
	if _, err := importer.Run(ctx, pool, path, importer.DefaultLayout(), &importer.Expect{TotalCost: 1}); err == nil || !strings.Contains(err.Error(), "LỆCH") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM products`).Scan(&n)
	if n != 0 {
		t.Fatalf("rollback left %d products", n)
	}

	rep, err := importer.Run(ctx, pool, path, importer.DefaultLayout(), &want)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Products != 3 || rep.Regular != 3 || rep.Bulk != 1 || rep.Sales != 3 || len(rep.TotalMismatches) != 0 {
		t.Fatalf("report=%+v", rep)
	}

	var tracking, note string
	var merged, checked, reviewed bool
	if err := pool.QueryRow(ctx, `SELECT tracking_no, merge_group IS NOT NULL, is_checked, is_reviewed FROM purchases WHERE item_url = 'https://jp.mercari.com/item/m2'`).
		Scan(&tracking, &merged, &checked, &reviewed); err != nil {
		t.Fatal(err)
	}
	if tracking != "622955048670" || !merged || !checked || !reviewed {
		t.Fatalf("tracking=%q merged=%v checked=%v reviewed=%v", tracking, merged, checked, reviewed)
	}
	_ = pool.QueryRow(ctx, `SELECT note FROM purchases WHERE item_url IS NULL AND source = 'REGULAR'`).Scan(&note)
	if note != "lấy ở tabata" {
		t.Fatalf("note=%q", note)
	}
	var opened string
	_ = pool.QueryRow(ctx, `SELECT note FROM sales WHERE unit_price = 0`).Scan(&opened)
	if opened != service.OpenedStockNote {
		t.Fatalf("0¥ sale note=%q", opened)
	}
	var dayOnly string
	_ = pool.QueryRow(ctx, `SELECT to_char(sale_date, 'YYYY-MM-DD') FROM sales WHERE unit_price = 0`).Scan(&dayOnly)
	if dayOnly != "2026-07-16" {
		t.Fatalf("day-only date=%q", dayOnly)
	}
	var start string
	_ = pool.QueryRow(ctx, `SELECT to_char(start_date, 'YYYY-MM-DD') FROM periods WHERE status = 'OPEN'`).Scan(&start)
	if start != "2026-07-03" {
		t.Fatalf("period start=%s", start)
	}
	// A second import into a non-empty database is refused
	if _, err := importer.Run(ctx, pool, path, importer.DefaultLayout(), &want); err == nil {
		t.Fatal("second import should fail")
	}
}
