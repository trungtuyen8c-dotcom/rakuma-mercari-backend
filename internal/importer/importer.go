// Package importer loads rakuma_t7.xlsx into an empty database (spec §15.3) and reconciles the totals.
//
// Column letters confirmed by spec §15.1 are fixed. Columns the spec does not pin down (order date, check/review marks,
// customer, notes) are configurable in Layout; run with Inspect first to see the header rows and confirm them.
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

type Layout struct {
	FirstRow int // first data row (row 4 in every sheet, §15.1)

	// Purchases ("NHẬP HÀNG" and "box 30th"). Confirmed: C link, D product, E price, F qty, G discount, H total, I tracking.
	PurDate, PurCheck, PurReview, PurNote string // assumed, verify with Inspect

	// Sales ("BÁN HÀNG"). Confirmed: C product, D qty, E unit price, F total, G shipping.
	SaleDate, SaleCustomer string // assumed, verify with Inspect
}

func DefaultLayout() Layout {
	return Layout{FirstRow: 4, PurDate: "B", PurCheck: "J", PurReview: "K", PurNote: "L", SaleDate: "B", SaleCustomer: "H"}
}

type Expect struct {
	TotalCost, TotalRevenue int64
	Stock                   int
}

type Report struct {
	Products, Regular, Bulk, Sales int
	OpeningCost, OpeningRevenue    int64
	TotalCost, TotalRevenue        int64
	Stock                          int
	TotalMismatches                []string // rows where our computed total differs from the Excel total column
	Notes                          []string
}

// Sheet names carry emoji prefixes; match on the text part.
var sheetKeys = map[string]string{
	"catalog": "DANH MỤC", "regular": "NHẬP HÀNG", "bulk": "BOX 30TH", "sales": "BÁN HÀNG", "stock": "TỒN KHO", "settings": "CÀI ĐẶT",
}

func findSheets(f *excelize.File) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range f.GetSheetList() {
		up := strings.ToUpper(name)
		for k, key := range sheetKeys {
			if strings.Contains(up, key) {
				out[k] = name
			}
		}
	}
	for k, key := range sheetKeys {
		if out[k] == "" {
			return nil, fmt.Errorf("không thấy sheet chứa %q (có: %v)", key, f.GetSheetList())
		}
	}
	return out, nil
}

// Inspect prints the sheet names and the first rows of each sheet so the column layout can be confirmed.
func Inspect(path string, w io.Writer) error {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, name := range f.GetSheetList() {
		rows, err := f.GetRows(name)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "== %s (%d rows)\n", name, len(rows))
		for i := 0; i < len(rows) && i < 5; i++ {
			var cells []string
			for j, v := range rows[i] {
				if strings.TrimSpace(v) != "" {
					col, _ := excelize.ColumnNumberToName(j + 1)
					cells = append(cells, fmt.Sprintf("%s=%q", col, v))
				}
			}
			fmt.Fprintf(w, "  row %d: %s\n", i+1, strings.Join(cells, " "))
		}
	}
	return nil
}

// sheet holds one worksheet's raw cell values, read once (reading per cell re-parses the whole sheet).
type sheet struct {
	name string
	rows [][]string
}

func loadSheet(f *excelize.File, name string) sheet {
	rows, _ := f.GetRows(name, excelize.Options{RawCellValue: true})
	return sheet{name: name, rows: rows}
}

func (s sheet) cell(col string, row int) string {
	if col == "" || row < 1 || row > len(s.rows) {
		return ""
	}
	c, err := excelize.ColumnNameToNumber(col)
	if err != nil || c > len(s.rows[row-1]) {
		return ""
	}
	return strings.TrimSpace(s.rows[row-1][c-1])
}

func (s sheet) lastRow() int { return len(s.rows) }

func parseMoney(v string) (int64, bool) {
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", ""), 64)
	if err != nil || f != math.Trunc(f) {
		return 0, false
	}
	return int64(f), true
}

// tracking numbers were stored as numbers in Excel (§13); render them back as plain digits.
func parseTracking(v string) string {
	if v == "" {
		return ""
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && (strings.ContainsAny(v, "eE.")) && f == math.Trunc(f) {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	return v
}

// dayOnly reports a date cell that holds just a day number (e.g. "16"), as typed by hand in the sheet.
func dayOnly(v string) (int, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f != math.Trunc(f) || f < 1 || f > 31 {
		return 0, false
	}
	return int(f), true
}

// withDay puts day d into the month of prev ("YYYY-MM-DD"); false if that date does not exist.
func withDay(prev string, d int) (string, bool) {
	t, err := time.Parse("2006-01-02", prev)
	if err != nil {
		return "", false
	}
	out := time.Date(t.Year(), t.Month(), d, 0, 0, 0, 0, time.UTC)
	if out.Month() != t.Month() {
		return "", false
	}
	return out.Format("2006-01-02"), true
}

func parseDate(v string) (string, bool) {
	if v == "" {
		return "", true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		if f < 36526 { // before 2000-01-01: not a real order/sale date
			return "", false
		}
		t, err := excelize.ExcelDateToTime(f, false)
		if err != nil {
			return "", false
		}
		return t.Format("2006-01-02"), true
	}
	for _, layout := range []string{"2006-01-02", "02/01/2006", "2/1/2006", "01-02-06"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

type purRow struct {
	sheet, source            string
	row                      int
	date, link, product      string
	price, qty, disc, xtotal int64
	hasTotal                 bool
	tracking, note           string
	checked, reviewed        bool
}

type saleRow struct {
	row                 int
	date, product, cust string
	qty, price, ship    int64
	xtotal              int64
	hasTotal            bool
}

// Run imports into an empty database inside one transaction; on a reconciliation mismatch nothing is committed.
func Run(ctx context.Context, pool *pgxpool.Pool, path string, lay Layout, exp *Expect) (Report, error) {
	var rep Report
	f, err := excelize.OpenFile(path)
	if err != nil {
		return rep, err
	}
	defer f.Close()
	sh, err := findSheets(f)
	if err != nil {
		return rep, err
	}

	// Catalog (BR-01): names in column B
	cat := loadSheet(f, sh["catalog"])
	var names []string
	seen := map[string]bool{}
	for r := lay.FirstRow; r <= cat.lastRow(); r++ {
		n := cat.cell("B", r)
		if n == "" || seen[n] {
			if n != "" {
				rep.Notes = append(rep.Notes, fmt.Sprintf("Danh mục dòng %d: tên trùng %q, bỏ qua", r, n))
			}
			continue
		}
		seen[n] = true
		names = append(names, n)
	}

	var bad []string
	readPurchases := func(key, source string) []purRow {
		s := loadSheet(f, sh[key])
		prevDate := ""
		var out []purRow
		for r := lay.FirstRow; r <= s.lastRow(); r++ {
			p := purRow{sheet: s.name, source: source, row: r, product: s.cell("D", r)}
			if p.product == "" { // BR-02: rows without a product are empty
				continue
			}
			var okP, okQ bool
			p.price, okP = parseMoney(s.cell("E", r))
			p.qty, okQ = parseMoney(s.cell("F", r))
			p.disc, _ = parseMoney(s.cell("G", r))
			p.xtotal, p.hasTotal = parseMoney(s.cell("H", r))
			p.link = s.cell("C", r)
			p.tracking = parseTracking(s.cell("I", r))
			var okD bool
			raw := s.cell(lay.PurDate, r)
			if d, ok := dayOnly(raw); ok && prevDate != "" {
				p.date, okD = withDay(prevDate, d)
				rep.Notes = append(rep.Notes, fmt.Sprintf("%s dòng %d: ngày %q hiểu là %s (tháng của dòng trên)", s.name, r, raw, p.date))
			} else {
				p.date, okD = parseDate(raw)
			}
			if okD && p.date != "" {
				prevDate = p.date
			}
			if !okP || !okQ || p.price <= 0 || p.qty < 1 || p.disc < 0 || p.disc > p.price || !okD {
				bad = append(bad, fmt.Sprintf("%s dòng %d (%s): giá=%q SL=%q giảm=%q ngày=%q", s.name, r, p.product,
					s.cell("E", r), s.cell("F", r), s.cell("G", r), s.cell(lay.PurDate, r)))
				continue
			}
			// §15.3: "ok"/"Ok" -> checked, "done"/"Done"/"done " -> reviewed, anything else goes to the note
			var notes []string
			if n := s.cell(lay.PurNote, r); n != "" {
				notes = append(notes, n)
			}
			for _, c := range []struct {
				col, word string
				flag      *bool
			}{{lay.PurCheck, "ok", &p.checked}, {lay.PurReview, "done", &p.reviewed}} {
				v := s.cell(c.col, r)
				switch {
				case strings.EqualFold(v, c.word):
					*c.flag = true
				case v != "":
					notes = append(notes, v)
				}
			}
			p.note = strings.Join(notes, "; ")
			out = append(out, p)
		}
		return out
	}
	regular := readPurchases("regular", "REGULAR")
	bulk := readPurchases("bulk", "BULK")

	ss := loadSheet(f, sh["sales"])
	var sales []saleRow
	prevSale := ""
	for r := lay.FirstRow; r <= ss.lastRow(); r++ {
		s := saleRow{row: r, product: ss.cell("C", r)}
		if s.product == "" {
			continue
		}
		var okQ, okP, okD bool
		s.qty, okQ = parseMoney(ss.cell("D", r))
		s.price, okP = parseMoney(ss.cell("E", r))
		s.ship, _ = parseMoney(ss.cell("G", r))
		s.xtotal, s.hasTotal = parseMoney(ss.cell("F", r))
		raw := ss.cell(lay.SaleDate, r)
		if d, ok := dayOnly(raw); ok && prevSale != "" {
			s.date, okD = withDay(prevSale, d)
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s dòng %d: ngày %q hiểu là %s (tháng của dòng trên)", ss.name, r, raw, s.date))
		} else {
			s.date, okD = parseDate(raw)
		}
		if okD && s.date != "" {
			prevSale = s.date
		}
		s.cust = ss.cell(lay.SaleCustomer, r)
		if !okQ || !okP || s.qty < 1 || s.price < 0 || s.ship < 0 || !okD {
			bad = append(bad, fmt.Sprintf("%s dòng %d (%s): SL=%q giá=%q ship=%q ngày=%q", ss.name, r, s.product,
				ss.cell("D", r), ss.cell("E", r), ss.cell("G", r), ss.cell(lay.SaleDate, r)))
			continue
		}
		sales = append(sales, s)
	}
	if len(bad) > 0 {
		return rep, fmt.Errorf("%d dòng không hợp lệ, sửa trong Excel rồi chạy lại:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}

	// Opening stock: stock sheet B name, C opening (products missing there start at 0, §13)
	st := loadSheet(f, sh["stock"])
	openings := map[string]int64{}
	for r := lay.FirstRow; r <= st.lastRow(); r++ {
		if n := st.cell("B", r); n != "" {
			q, _ := parseMoney(st.cell("C", r))
			openings[n] = q
		}
	}
	set := loadSheet(f, sh["settings"])
	rep.OpeningCost, _ = parseMoney(set.cell("B", 9))
	rep.OpeningRevenue, _ = parseMoney(set.cell("B", 10))

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var used int
		if err := tx.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM products) + (SELECT COUNT(*) FROM purchases) + (SELECT COUNT(*) FROM sales)`).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return errors.New("database đã có dữ liệu; chỉ import vào database trống")
		}
		open, err := repo.OpenPeriod(ctx, tx, true)
		if err != nil {
			return fmt.Errorf("không có kỳ đang mở: %w", err)
		}

		ids := map[string]int64{}
		for _, n := range names {
			id, err := repo.InsertProduct(ctx, tx, n)
			if err != nil {
				return fmt.Errorf("sản phẩm %q: %w", n, err)
			}
			ids[n] = id
		}
		for n := range openings {
			if _, ok := ids[n]; !ok {
				rep.Notes = append(rep.Notes, fmt.Sprintf("Tồn kho có %q nhưng danh mục không có, bỏ qua", n))
			}
		}
		for _, n := range names {
			if _, ok := openings[n]; !ok {
				rep.Notes = append(rep.Notes, fmt.Sprintf("Thêm dòng tồn đầu = 0 cho %q (thiếu trong sheet Tồn kho)", n))
			}
			if err := repo.UpsertOpening(ctx, tx, open.ID, ids[n], int(openings[n])); err != nil {
				return err
			}
		}

		// Group rows sharing a tracking number into one merge_group (§15.3)
		trackCount := map[string]int{}
		for _, p := range append(append([]purRow{}, regular...), bulk...) {
			if p.tracking != "" {
				trackCount[p.tracking]++
			}
		}
		earliest := open.Start
		for _, p := range append(append([]purRow{}, regular...), bulk...) {
			pid, ok := ids[p.product]
			if !ok {
				return fmt.Errorf("%s dòng %d: sản phẩm %q không có trong danh mục", p.sheet, p.row, p.product)
			}
			row := repo.PurchaseRow{PeriodID: open.ID, Source: p.source, ProductID: pid, Price: p.price, Qty: int(p.qty),
				Discount: p.disc, Checked: p.checked, Reviewed: p.reviewed, Note: p.note}
			if p.date != "" {
				row.Date = &p.date
				earliest = min(earliest, p.date)
			}
			if p.link != "" {
				row.Link = &p.link
			}
			if p.tracking != "" {
				t := p.tracking
				row.Tracking = &t
				if trackCount[t] > 1 {
					row.MergeGroup = &t
				}
			}
			if _, err := repo.InsertPurchase(ctx, tx, row); err != nil {
				return fmt.Errorf("%s dòng %d: %w", p.sheet, p.row, err)
			}
			if got := (p.price - p.disc) * p.qty; p.hasTotal && got != p.xtotal {
				rep.TotalMismatches = append(rep.TotalMismatches, fmt.Sprintf("%s dòng %d: Excel %d, tính lại %d", p.sheet, p.row, p.xtotal, got))
			}
		}
		for _, s := range sales {
			pid, ok := ids[s.product]
			if !ok {
				return fmt.Errorf("%s dòng %d: sản phẩm %q không có trong danh mục", ss.name, s.row, s.product)
			}
			row := repo.SaleRow{PeriodID: open.ID, ProductID: pid, Qty: int(s.qty), Price: s.price, Ship: s.ship, Customer: s.cust}
			if s.price == 0 { // 0¥ rows record opened stock (owner decision)
				row.Note = service.OpenedStockNote
			}
			if s.date != "" {
				row.Date = &s.date
				earliest = min(earliest, s.date)
			}
			if _, err := repo.InsertSale(ctx, tx, row); err != nil {
				return fmt.Errorf("%s dòng %d: %w", ss.name, s.row, err)
			}
			if got := s.qty*s.price - s.ship; s.hasTotal && got != s.xtotal {
				rep.TotalMismatches = append(rep.TotalMismatches, fmt.Sprintf("%s dòng %d: Excel %d, tính lại %d", ss.name, s.row, s.xtotal, got))
			}
		}

		// The Excel data spans several months in one sheet; widen the open period so imported dates fall inside it.
		if _, err := tx.Exec(ctx, `UPDATE periods SET opening_cost = $2, opening_revenue = $3, start_date = LEAST(start_date, $4::date) WHERE id = $1`,
			open.ID, rep.OpeningCost, rep.OpeningRevenue, earliest); err != nil {
			return err
		}
		if earliest < open.Start {
			rep.Notes = append(rep.Notes, fmt.Sprintf("Kỳ %s được mở rộng từ %s để chứa ngày cũ nhất trong file", open.Label, earliest))
		}

		t, err := repo.PeriodTotals(ctx, tx, open.ID)
		if err != nil {
			return err
		}
		rep.Products, rep.Regular, rep.Bulk, rep.Sales = len(names), len(regular), len(bulk), len(sales)
		rep.TotalCost, rep.TotalRevenue, rep.Stock = t.TotalCost, t.TotalRevenue, t.StockTotal
		sort.Strings(rep.Notes)

		if exp != nil && (t.TotalCost != exp.TotalCost || t.TotalRevenue != exp.TotalRevenue || t.StockTotal != exp.Stock) {
			return fmt.Errorf("đối chiếu LỆCH, không lưu gì: tổng vốn %d (cần %d), doanh thu %d (cần %d), tồn %d (cần %d)",
				t.TotalCost, exp.TotalCost, t.TotalRevenue, exp.TotalRevenue, t.StockTotal, exp.Stock)
		}
		return repo.Audit(ctx, tx, "import", path, "import_excel", "import-excel", nil, rep)
	})
	return rep, err
}
