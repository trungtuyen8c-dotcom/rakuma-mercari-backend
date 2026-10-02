package routers_test

import "testing"

// The owner opens October while September still has purchases waiting for delivery; closing September later must
// give October the same opening figures it showed live (BR-14).
func TestOpenNextPeriodWhileCurrentStaysOpen(t *testing.T) {
	e := setup(t)
	write := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "claude", "scope": "write"}, 201)["key"].(string)
	pid := e.product("ninja")
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 5, 0, map[string]any{"date": "2026-09-10"}), 201)

	if res := e.do("POST", "/api/v1/periods/open-next", nil, write); res.Code != 201 {
		t.Fatalf("open next: %d %s", res.Code, res.Body)
	}
	e.ok("POST", "/api/v1/periods/open-next", nil, 409) // at most two open

	periods := e.ok("GET", "/api/v1/periods", nil, 200)["_"].([]any)
	sep, oct := periods[0].(map[string]any), periods[1].(map[string]any)
	if sep["status"] != "OPEN" || oct["status"] != "OPEN" || oct["label"] != "10/2026" {
		t.Fatalf("periods: %v", periods)
	}

	// Rows go to the period of their date; no date means the newest open period; periodId picks explicitly
	p := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"date": "2026-09-20"}), 201)
	if p["periodLabel"] != "09/2026" {
		t.Fatalf("september row: %v", p)
	}
	s := e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 2, "price": 3000, "date": "2026-10-03"}, 201)
	if s["periodLabel"] != "10/2026" {
		t.Fatalf("october sale: %v", s)
	}
	if p := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, nil), 201); p["periodLabel"] != "10/2026" {
		t.Fatalf("dateless row: %v", p)
	}
	if p := e.ok("POST", "/api/v1/purchases", purchase(pid, 500, 1, 0, map[string]any{"periodId": sep["id"]}), 201); p["periodLabel"] != "09/2026" {
		t.Fatalf("explicit period: %v", p)
	}
	out := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"date": "2026-11-01"}), 422)
	if out["errors"].(map[string]any)["date"] == nil {
		t.Fatalf("november accepted: %v", out)
	}
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"periodId": oct["id"], "date": "2026-09-01"}), 422)

	// October's opening is live from September: cost 5000+1000+500, stock 5+1+1
	octID := oct["id"].(string)
	live := e.ok("GET", "/api/v1/dashboard?period_id="+octID, nil, 200)
	if tt := live["totals"].(map[string]any); num(tt["prevCost"]) != 6500 || num(tt["totalCost"]) != 7500 || num(tt["totalRevenue"]) != 6000 {
		t.Fatalf("live october totals: %v", tt)
	}
	stockRow := func() map[string]any {
		for _, r := range e.ok("GET", "/api/v1/stock?period_id="+octID, nil, 200)["rows"].([]any) {
			if r.(map[string]any)["productId"] == pid {
				return r.(map[string]any)
			}
		}
		t.Fatal("no stock row")
		return nil
	}
	if r := stockRow(); num(r["opening"]) != 7 || num(r["current"]) != 6 {
		t.Fatalf("live october stock: %v", r)
	}

	// Closing September keeps October and stores the same figures
	if next := e.ok("POST", "/api/v1/periods/close", nil, 200); next["id"] != octID || num(next["openingCost"]) != 6500 {
		t.Fatalf("close september: %v", next)
	}
	if tt := e.ok("GET", "/api/v1/dashboard?period_id="+octID, nil, 200)["totals"].(map[string]any); num(tt["totalCost"]) != 7500 {
		t.Fatalf("stored october totals: %v", tt)
	}
	if r := stockRow(); num(r["opening"]) != 7 || num(r["current"]) != 6 {
		t.Fatalf("stored october stock: %v", r)
	}
	if n := len(e.ok("GET", "/api/v1/periods", nil, 200)["_"].([]any)); n != 2 {
		t.Fatalf("close created an extra period: %d", n)
	}
	// With one open period again, the next month can be opened
	e.ok("POST", "/api/v1/periods/open-next", nil, 201)
}

// "Today still counts as September, October starts tomorrow": the cut-off moves September's end.
func TestOpenNextPeriodWithCutOff(t *testing.T) {
	e := setup(t)
	write := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "claude", "scope": "write"}, 201)["key"].(string)
	pid := e.product("ninja")
	p := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 2, 0, map[string]any{"date": "2026-09-30"}), 201)

	e.ok("POST", "/api/v1/periods/open-next", map[string]string{"start": "2026-09-30"}, 409) // a September row is dated 30/09
	e.ok("POST", "/api/v1/periods/open-next", map[string]string{"start": "2026-11-01"}, 422)
	res := e.do("POST", "/api/v1/periods/open-next", map[string]string{"start": "2026-10-03"}, write)
	if res.Code != 201 {
		t.Fatalf("open with cut-off: %d %s", res.Code, res.Body)
	}
	periods := e.ok("GET", "/api/v1/periods", nil, 200)["_"].([]any)
	sep, oct := periods[0].(map[string]any), periods[1].(map[string]any)
	if sep["end"] != "2026-10-02" || oct["start"] != "2026-10-03" || oct["end"] != "2026-10-31" {
		t.Fatalf("cut-off: %v %v", sep, oct)
	}
	if s := e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 1, "price": 2000, "date": "2026-10-02"}, 201); s["periodLabel"] != "09/2026" {
		t.Fatalf("2/10 sale: %v", s)
	}
	if s := e.ok("POST", "/api/v1/sales", map[string]any{"productId": pid, "qty": 1, "price": 2000, "date": "2026-10-03"}, 201); s["periodLabel"] != "10/2026" {
		t.Fatalf("3/10 sale: %v", s)
	}

	// A write key can tick a purchase and add its tracking number later
	id := p["id"].(string)
	res = e.do("PATCH", "/api/v1/purchases/"+id, map[string]any{"checked": true, "reviewed": true, "tracking": "623160585353"}, write)
	if res.Code != 200 {
		t.Fatalf("write patch: %d %s", res.Code, res.Body)
	}
	got := e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)[0].(map[string]any)
	if got["checked"] != true || got["reviewed"] != true || got["tracking"] != "623160585353" {
		t.Fatalf("patched: %v", got)
	}
	e.ok("PATCH", "/api/v1/purchases/"+id, map[string]any{"tracking": "bad no!"}, 422)
}
