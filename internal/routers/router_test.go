package routers_test

// Integration tests: real Postgres (TEST_DATABASE_URL), real HTTP router. One test per acceptance criterion (spec §12).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/config"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/db"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/routers"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

const owner = "owner@example.com"

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	h      http.Handler
	cookie string
}

func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureOpenPeriod(ctx, pool, time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AppEnv: "test", AppURL: "http://localhost", OwnerEmail: owner}
	e := &env{t: t, pool: pool, h: routers.New(service.New(pool, cfg))}
	res := e.do("POST", "/api/v1/auth/dev-login", map[string]string{"email": owner, "name": "Owner"}, "")
	if res.Code != 200 {
		t.Fatalf("dev-login: %d %s", res.Code, res.Body)
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == "rakuma_session" {
			e.cookie = c.Value
		}
	}
	return e
}

// do sends a request. auth: "" = owner session, "none" = anonymous, anything else = API key.
func (e *env) do(method, path string, body any, auth string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	switch {
	case auth == "" && e.cookie != "":
		req.AddCookie(&http.Cookie{Name: "rakuma_session", Value: e.cookie})
	case auth != "" && auth != "none":
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) ok(method, path string, body any, want int) map[string]any {
	e.t.Helper()
	res := e.do(method, path, body, "")
	if res.Code != want {
		e.t.Fatalf("%s %s: got %d want %d: %s", method, path, res.Code, want, res.Body)
	}
	// JSON arrays come back under "_" so every call returns a map
	var out map[string]any
	if b := bytes.TrimSpace(res.Body.Bytes()); len(b) > 0 && b[0] == '[' {
		var arr []any
		_ = json.Unmarshal(b, &arr)
		return map[string]any{"_": arr}
	}
	_ = json.Unmarshal(res.Body.Bytes(), &out)
	return out
}

func (e *env) product(name string) string {
	return e.ok("POST", "/api/v1/products", map[string]string{"name": name}, 201)["id"].(string)
}

func (e *env) stock(productID string) map[string]any {
	e.t.Helper()
	out := e.ok("GET", "/api/v1/stock", nil, 200)
	for _, r := range out["rows"].([]any) {
		row := r.(map[string]any)
		if row["productId"] == productID {
			return row
		}
	}
	e.t.Fatalf("product %s not in stock", productID)
	return nil
}

func num(v any) int64 { return int64(v.(float64)) }

func purchase(pid string, price, qty, discount int, extra map[string]any) map[string]any {
	m := map[string]any{"productId": pid, "source": "REGULAR", "price": price, "qty": qty, "discount": discount}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestAC01_PurchaseTotalAndStock(t *testing.T) {
	e := setup(t)
	ninja := e.product("ninja")
	p := e.ok("POST", "/api/v1/purchases", purchase(ninja, 10000, 2, 0, nil), 201)
	if num(p["total"]) != 20000 || num(p["stt"]) != 1 {
		t.Fatalf("total=%v stt=%v", p["total"], p["stt"])
	}
	if s := e.stock(ninja); num(s["incoming"]) != 2 {
		t.Fatalf("incoming=%v", s["incoming"])
	}
}

func TestAC02_PerUnitDiscount(t *testing.T) {
	e := setup(t)
	p := e.ok("POST", "/api/v1/purchases", purchase(e.product("アビスアイ"), 17000, 1, 1190, nil), 201)
	if num(p["total"]) != 15810 {
		t.Fatalf("total=%v", p["total"])
	}
}

func TestAC03_MissingPriceRejected(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	out := e.ok("POST", "/api/v1/purchases", map[string]any{"productId": pid, "qty": 1}, 422)
	if out["errors"].(map[string]any)["price"] == nil {
		t.Fatalf("no price error: %v", out)
	}
	if n := len(e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)); n != 0 {
		t.Fatalf("saved %d rows", n)
	}
}

func TestAC04_ServerSideAuthorization(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	body := purchase(pid, 1000, 1, 0, nil)
	if res := e.do("POST", "/api/v1/purchases", body, "none"); res.Code != 401 {
		t.Fatalf("anonymous POST: %d", res.Code)
	}
	if res := e.do("DELETE", "/api/v1/products/"+pid, nil, "none"); res.Code != 401 {
		t.Fatalf("anonymous DELETE: %d", res.Code)
	}
	if res := e.do("POST", "/api/v1/purchases", body, "rk_live_forged"); res.Code != 401 {
		t.Fatalf("forged key: %d", res.Code)
	}
	read := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "r", "scope": "read"}, 201)["key"].(string)
	write := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "w", "scope": "write"}, 201)["key"].(string)
	if res := e.do("GET", "/api/v1/dashboard", nil, read); res.Code != 200 {
		t.Fatalf("read key GET: %d", res.Code)
	}
	if res := e.do("POST", "/api/v1/purchases", body, read); res.Code != 403 {
		t.Fatalf("read key POST: %d", res.Code)
	}
	if res := e.do("POST", "/api/v1/purchases", body, write); res.Code != 201 {
		t.Fatalf("write key POST: %d %s", res.Code, res.Body)
	}
	for _, path := range []string{"/api/v1/periods/close", "/api/v1/api-keys"} {
		if res := e.do("POST", path, map[string]string{"name": "x"}, write); res.Code != 403 {
			t.Fatalf("write key %s: %d", path, res.Code)
		}
	}
	if res := e.do("GET", "/api/v1/state", nil, write); res.Code != 403 {
		t.Fatalf("write key state: %d", res.Code)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM purchases`).Scan(&n)
	if n != 1 {
		t.Fatalf("purchases=%d, want only the write-key one", n)
	}
	// Revoked keys stop working immediately
	keys := e.ok("GET", "/api/v1/api-keys", nil, 200)
	_ = keys
	var wid string
	_ = e.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE name = 'w'`).Scan(&wid)
	e.ok("POST", "/api/v1/api-keys/"+wid+"/revoke", nil, 200)
	if res := e.do("GET", "/api/v1/dashboard", nil, write); res.Code != 401 {
		t.Fatalf("revoked key: %d", res.Code)
	}
}

func TestAC05_SaleTotalAndRevenue(t *testing.T) {
	e := setup(t)
	box := e.product("151 box")
	e.ok("POST", "/api/v1/purchases", purchase(box, 50000, 1, 0, nil), 201)
	before := num(e.ok("GET", "/api/v1/dashboard", nil, 200)["totals"].(map[string]any)["totalRevenue"])
	s := e.ok("POST", "/api/v1/sales", map[string]any{"productId": box, "qty": 1, "price": 58500, "ship": 0}, 201)
	if num(s["total"]) != 58500 {
		t.Fatalf("total=%v", s["total"])
	}
	after := num(e.ok("GET", "/api/v1/dashboard", nil, 200)["totals"].(map[string]any)["totalRevenue"])
	if after-before != 58500 || num(e.stock(box)["sold"]) != 1 {
		t.Fatalf("revenue delta=%d", after-before)
	}
}

func TestAC06_CurrentStock(t *testing.T) {
	e := setup(t)
	hb := e.product("hồng bé")
	e.ok("PUT", "/api/v1/stock/"+hb+"/opening", map[string]any{"qty": 3}, 204)
	e.ok("POST", "/api/v1/purchases", purchase(hb, 2800, 47, 0, nil), 201)
	e.ok("POST", "/api/v1/sales?force=true", map[string]any{"productId": hb, "qty": 33, "price": 3900}, 201)
	if s := e.stock(hb); num(s["current"]) != 17 {
		t.Fatalf("current=%v", s["current"])
	}
}

func TestAC07_OversellWarnsButAllows(t *testing.T) {
	e := setup(t)
	dream := e.product("dream")
	out := e.ok("POST", "/api/v1/sales", map[string]any{"productId": dream, "qty": 1, "price": 1000}, 409)
	if w := out["warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "Tồn kho sẽ âm 1 cái") {
		t.Fatalf("warnings=%v", out["warnings"])
	}
	e.ok("POST", "/api/v1/sales?force=true", map[string]any{"productId": dream, "qty": 1, "price": 1000}, 201)
	if s := e.stock(dream); num(s["current"]) != -1 {
		t.Fatalf("current=%v", s["current"])
	}
	if neg := e.ok("GET", "/api/v1/dashboard", nil, 200)["negatives"].([]any); len(neg) != 1 {
		t.Fatalf("negatives=%v", neg)
	}
}

// Owner decision: a 0¥ sale records opened stock ("bóc hàng"); it needs confirmation.
func TestZeroPriceSaleIsOpenedStock(t *testing.T) {
	e := setup(t)
	pid := e.product("op 17")
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 3, 0, nil), 201)
	out := e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 1, "price": 0}, 409)
	if w := out["warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "bóc hàng") {
		t.Fatalf("warnings=%v", out["warnings"])
	}
	s := e.ok("POST", "/api/v1/sales?force=true", map[string]any{"productId": pid, "qty": 1, "price": 0}, 201)
	if num(s["total"]) != 0 || s["note"] != "Bóc hàng" {
		t.Fatalf("sale=%v", s)
	}
	if st := e.stock(pid); num(st["current"]) != 2 {
		t.Fatalf("stock=%v", st)
	}
	if rev := num(e.ok("GET", "/api/v1/dashboard", nil, 200)["totals"].(map[string]any)["periodRevenue"]); rev != 0 {
		t.Fatalf("revenue=%d", rev)
	}
	e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 1, "price": -5}, 422)
}

func TestAC08_DuplicateLinkWarns(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	link := map[string]any{"link": "https://jp.mercari.com/item/m21525628171"}
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, link), 201)
	out := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, link), 409)
	if w := out["warnings"].([]any); !strings.Contains(w[0].(string), "dòng số 1") {
		t.Fatalf("warnings=%v", w)
	}
	e.ok("POST", "/api/v1/purchases?force=true", purchase(pid, 1000, 1, 0, link), 201)
	rows := e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)
	if !rows[0].(map[string]any)["dupLink"].(bool) {
		t.Fatal("dupLink not flagged")
	}
}

func TestAC09_MergedShipmentNotFlagged(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	merged := map[string]any{"tracking": "622955048670", "merged": true}
	for range 3 {
		e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, merged), 201)
	}
	for _, r := range e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any) {
		if r.(map[string]any)["dupTracking"].(bool) {
			t.Fatal("merged row flagged as duplicate")
		}
	}
	// Same tracking without the merge mark is a duplicate (BR-06)
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"tracking": "622955048670"}), 409)
}

func TestAC10_DashboardPreviousPeriod(t *testing.T) {
	e := setup(t)
	if _, err := e.pool.Exec(context.Background(), `UPDATE periods SET opening_cost = 29166752, opening_revenue = 30478272`); err != nil {
		t.Fatal(err)
	}
	tot := e.ok("GET", "/api/v1/dashboard", nil, 200)["totals"].(map[string]any)
	if num(tot["prevProfit"]) != 1311520 || num(tot["totalCost"]) != 29166752 || num(tot["totalRevenue"]) != 30478272 {
		t.Fatalf("totals=%v", tot)
	}
}

func TestAC11_ClosePeriodCarriesForward(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	e.ok("POST", "/api/v1/purchases", purchase(pid, 10000, 5, 0, nil), 201)
	e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 2, "price": 15000}, 201)
	next := e.ok("POST", "/api/v1/periods/close", nil, 200)
	if next["label"] != "10/2026" || next["start"] != "2026-10-01" || next["end"] != "2026-10-31" {
		t.Fatalf("next=%v", next)
	}
	if num(next["openingCost"]) != 50000 || num(next["openingRevenue"]) != 30000 {
		t.Fatalf("opening=%v/%v", next["openingCost"], next["openingRevenue"])
	}
	if s := e.stock(pid); num(s["opening"]) != 3 || num(s["current"]) != 3 {
		t.Fatalf("stock=%v", s)
	}
	d := e.ok("GET", "/api/v1/dashboard?period_id=1", nil, 200)
	if !d["matches"].(bool) {
		t.Fatal("closed period does not reconcile")
	}
}

func TestAC12_ClosedPeriodIsReadOnly(t *testing.T) {
	// Owner decision (2026-10-03): working alone, the owner edits closed periods too; later periods follow.
	e := setup(t)
	pid := e.product("ninja")
	pur := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 2, 0, nil), 201)["id"].(string)
	sale := e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 1, "price": 2000}, 201)["id"].(string)
	next := e.ok("POST", "/api/v1/periods/close", nil, 200)
	e.ok("PATCH", "/api/v1/purchases/"+pur, map[string]any{"checked": true}, 200)
	e.ok("DELETE", "/api/v1/sales/"+sale, nil, 204)
	// The next period's opening follows the edit: cost 2000, revenue 0 after the sale is gone, stock 2
	nx := e.ok("GET", "/api/v1/dashboard?period_id="+next["id"].(string), nil, 200)["totals"].(map[string]any)
	if num(nx["prevCost"]) != 2000 || num(nx["prevRevenue"]) != 0 || num(nx["stockTotal"]) != 2 {
		t.Fatalf("next period did not follow: %v", nx)
	}
	// Dates still have to fall inside an open period unless a period is chosen explicitly
	out := e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 1, "price": 1, "date": "2026-09-20"}, 422)
	if out["errors"].(map[string]any)["date"] == nil {
		t.Fatalf("date not rejected: %v", out)
	}
}

func TestAC13_AnalysisLIFO(t *testing.T) {
	e := setup(t)
	pid := e.product("30th Celebration")
	e.ok("POST", "/api/v1/purchases", purchase(pid, 100, 5, 0, nil), 201)
	e.ok("POST", "/api/v1/purchases", purchase(pid, 250, 3, 50, nil), 201)
	e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 6, "price": 400}, 201)
	a := e.ok("GET", "/api/v1/analysis/products/"+pid, nil, 200)
	// LIFO: 3 from the latest lot at 200, then 3 from the older lot at 100
	if num(a["cogs"]) != 900 || num(a["profit"]) != 1500 || num(a["uncovered"]) != 0 || len(a["lots"].([]any)) != 2 {
		t.Fatalf("analysis=%v", a)
	}
}

func TestAC14_NewProductHasStockRow(t *testing.T) {
	e := setup(t)
	abc := e.product("abc")
	if s := e.stock(abc); num(s["opening"]) != 0 || num(s["current"]) != 0 {
		t.Fatalf("stock=%v", s)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM stock_openings WHERE product_id = $1`, abc).Scan(&n)
	if n != 1 {
		t.Fatalf("stock_openings rows=%d", n)
	}
}

func TestErrorCases(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	// E1: only the owner can sign in
	if res := e.do("POST", "/api/v1/auth/dev-login", map[string]string{"email": "helper@example.com"}, "none"); res.Code != 403 {
		t.Fatalf("E1: %d", res.Code)
	}
	// E4: discount above price
	out := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 2000, nil), 422)
	if out["errors"].(map[string]any)["discount"] != "Giảm giá không được lớn hơn giá nhập." {
		t.Fatalf("E4: %v", out)
	}
	// E7: duplicate name, ignoring surrounding spaces (BR-01)
	e.ok("POST", "/api/v1/products", map[string]string{"name": "  ninja "}, 422)
	// E11: product with transactions can only be hidden
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, nil), 201)
	e.ok("DELETE", "/api/v1/products/"+pid, nil, 409)
	e.ok("PATCH", "/api/v1/products/"+pid, map[string]any{"active": false}, 200)
	// Hidden products cannot be used in new rows
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, nil), 422)
	// E2: missing row
	e.ok("DELETE", "/api/v1/sales/999", nil, 404)
}

func TestAuditLogWritten(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, nil), 201)
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM audit_logs WHERE actor = $1 AND entity IN ('product', 'purchase')`, owner).Scan(&n)
	if n != 2 {
		t.Fatalf("audit rows=%d", n)
	}
}

func TestStateForUI(t *testing.T) {
	e := setup(t)
	pid := e.product("ninja")
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, nil), 201)
	st := e.ok("GET", "/api/v1/state", nil, 200)
	for _, k := range []string{"products", "purchases", "sales", "periods", "openings", "apiKeys", "settings", "user"} {
		if _, ok := st[k]; !ok {
			t.Fatalf("state missing %s", k)
		}
	}
	if st["user"].(map[string]any)["email"] != owner {
		t.Fatalf("user=%v", st["user"])
	}
	if fmt.Sprint(st["settings"].(map[string]any)["rate"]) != "175" {
		t.Fatalf("settings=%v", st["settings"])
	}
}
