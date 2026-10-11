package routers_test

import (
	"testing"
)

func TestPaymentCardsAndLedger(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	visa := e.ok("POST", "/api/v1/cards", map[string]any{"name": "Visa Rakuten"}, 201)
	e.ok("POST", "/api/v1/cards", map[string]any{"name": " visa rakuten "}, 422) // names match across projects, so unique
	e.ok("POST", "/api/v1/cards", map[string]any{"name": "VCB", "currency": "VND"}, 422)
	jcb := e.ok("POST", "/api/v1/cards", map[string]any{"name": "JCB"}, 201)
	read := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "other-project", "scope": "read"}, 201)["key"].(string)
	if res := e.do("POST", "/api/v1/cards", map[string]any{"name": "X"}, read); res.Code != 403 {
		t.Fatalf("read key created a card: %d", res.Code)
	}

	// A purchase paid by card is a charge of its total on the order date
	p := e.ok("POST", "/api/v1/purchases", purchase(pid, 25000, 4, 750, map[string]any{"date": "2026-09-20", "cardId": visa["id"]}), 201)
	if p["cardName"] != "Visa Rakuten" || num(p["total"]) != 97000 {
		t.Fatalf("purchase: %v", p)
	}
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"date": "2026-09-21"}), 201) // no card: not in the ledger

	res := e.do("GET", "/api/v1/wallet-ledger?from=2026-09-01&to=2026-09-30", nil, read)
	if res.Code != 200 {
		t.Fatalf("ledger: %d %s", res.Code, res.Body)
	}
	rows := e.ok("GET", "/api/v1/wallet-ledger", nil, 200)["_"].([]any)
	if len(rows) != 1 {
		t.Fatalf("ledger rows: %v", rows)
	}
	r := rows[0].(map[string]any)
	if r["id"] != "rakuma:purchase:"+p["id"].(string) || r["wallet"] != "Visa Rakuten" || r["currency"] != "JPY" || r["project"] != "rakuma" ||
		r["date"] != "2026-09-20" || num(r["amount"]) != -97000 || r["category"] != "Mua hàng" {
		t.Fatalf("ledger row: %v", r)
	}

	// Editing without cardId keeps the card (MCP sends the whole row); "" clears it; another card moves the charge
	id := p["id"].(string)
	got := e.ok("PUT", "/api/v1/purchases/"+id, purchase(pid, 25000, 4, 0, map[string]any{"date": "2026-09-20"}), 200)
	if got["cardId"] != visa["id"] || num(got["total"]) != 100000 {
		t.Fatalf("card kept: %v", got)
	}
	e.ok("PUT", "/api/v1/purchases/"+id, purchase(pid, 25000, 4, 0, map[string]any{"date": "2026-09-20", "cardId": jcb["id"]}), 200)
	if rows := e.ok("GET", "/api/v1/wallet-ledger?wallet=jcb", nil, 200)["_"].([]any); len(rows) != 1 || num(rows[0].(map[string]any)["amount"]) != -100000 {
		t.Fatalf("moved to JCB: %v", rows)
	}
	if rows := e.ok("GET", "/api/v1/wallet-ledger?wallet=Visa%20Rakuten", nil, 200)["_"].([]any); len(rows) != 0 {
		t.Fatalf("Visa still charged: %v", rows)
	}
	e.ok("GET", "/api/v1/wallet-ledger?from=20260901", nil, 422)

	// A card in use can be hidden, not deleted; a hidden card cannot be picked for another row
	e.ok("DELETE", "/api/v1/cards/"+jcb["id"].(string), nil, 409)
	e.ok("PATCH", "/api/v1/cards/"+jcb["id"].(string), map[string]any{"active": false}, 200)
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"cardId": jcb["id"]}), 422)
	e.ok("PUT", "/api/v1/purchases/"+id, purchase(pid, 25000, 4, 0, map[string]any{"date": "2026-09-20", "cardId": jcb["id"]}), 200)
	e.ok("DELETE", "/api/v1/cards/"+visa["id"].(string), nil, 204)
}

func TestSyncedAndSplitPurchasesUseTheCard(t *testing.T) {
	e := setup(t)
	a, b := e.product("Storm"), e.product("Abyss")
	card := e.ok("POST", "/api/v1/cards", map[string]any{"name": "Rakuten Card"}, 201)
	other := e.ok("POST", "/api/v1/cards", map[string]any{"name": "Other"}, 201)
	e.ok("PATCH", "/api/v1/cards/"+card["id"].(string), map[string]any{"default": true}, 200)
	e.ok("PATCH", "/api/v1/cards/"+other["id"].(string), map[string]any{"default": true}, 200) // only one default
	e.ok("PATCH", "/api/v1/cards/"+card["id"].(string), map[string]any{"default": true}, 200)
	cards := e.ok("GET", "/api/v1/cards", nil, 200)["_"].([]any)
	defaults := 0
	for _, c := range cards {
		if c.(map[string]any)["default"].(bool) {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("defaults: %v", cards)
	}

	// The sync writes the row with the default card; splitting keeps the card and the total
	e.sync("", rakumaOrder("C1", map[string]any{"price": 30000, "discount": 0}))
	row := e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)[0].(map[string]any)
	if row["cardId"] != card["id"] {
		t.Fatalf("synced row card: %v", row)
	}
	line := func(pid string, qty, price int) map[string]any {
		return map[string]any{"productId": pid, "qty": qty, "price": price}
	}
	e.ok("POST", "/api/v1/purchases/"+row["id"].(string)+"/split", map[string]any{"lines": []any{line(a, 2, 10000), line(b, 1, 10000)}}, 200)
	var sum int64
	for _, r := range e.ok("GET", "/api/v1/wallet-ledger?wallet=Rakuten%20Card", nil, 200)["_"].([]any) {
		sum += int64(num(r.(map[string]any)["amount"]))
	}
	if sum != -30000 {
		t.Fatalf("card total after split: %d", sum)
	}
}
