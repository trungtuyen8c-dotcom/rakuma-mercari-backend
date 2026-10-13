package routers_test

import (
	"fmt"
	"strings"
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

	// Totals per day, week (Monday start) and month, per card
	e.ok("POST", "/api/v1/purchases", purchase(pid, 5000, 1, 0, map[string]any{"date": "2026-09-22", "cardId": jcb["id"]}), 201) // 09-20 is a Sunday, 09-22 and 09-23 fall in the next week
	e.ok("POST", "/api/v1/purchases", purchase(pid, 3000, 1, 0, map[string]any{"date": "2026-09-23", "cardId": jcb["id"]}), 201)
	totals := func(group string) []any {
		return e.ok("GET", "/api/v1/wallet-ledger/totals?group="+group+"&wallet=JCB", nil, 200)["_"].([]any)
	}
	if w := totals("week"); len(w) != 2 || w[0].(map[string]any)["period"] != "2026-09-14" || num(w[0].(map[string]any)["amount"]) != -100000 ||
		w[1].(map[string]any)["period"] != "2026-09-21" || num(w[1].(map[string]any)["amount"]) != -8000 || num(w[1].(map[string]any)["count"]) != 2 {
		t.Fatalf("week totals: %v", w)
	}
	if m := totals("month"); len(m) != 1 || m[0].(map[string]any)["period"] != "2026-09-01" || num(m[0].(map[string]any)["amount"]) != -108000 {
		t.Fatalf("month totals: %v", m)
	}
	if d := totals("day"); len(d) != 3 {
		t.Fatalf("day totals: %v", d)
	}
	e.ok("GET", "/api/v1/wallet-ledger/totals?group=year", nil, 422)

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

	// A queued order approved by hand also gets the default card
	o := e.sync("", rakumaOrder("Q1", unpaid))["_"].([]any)[0].(map[string]any)
	if p := e.ok("POST", "/api/v1/rakuma/orders/"+o["id"].(string)+"/approve", purchase(a, 1000, 1, 0, map[string]any{"date": "2026-09-20"}), 201); p["cardId"] != card["id"] {
		t.Fatalf("approved row card: %v", p)
	}
	e.ok("DELETE", "/api/v1/purchases/"+o["id"].(string), nil, 204)

	// The sync writes the row with the default card; splitting keeps the card and the total
	e.sync("", rakumaOrder("C1", map[string]any{"price": 30000, "discount": 0}))
	var row map[string]any
	for _, p := range e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any) {
		if p := p.(map[string]any); p["link"] == "https://item.fril.jp/C1" {
			row = p
		}
	}
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

func TestRefundKeepsChargeAndRefundInLedger(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	card := e.ok("POST", "/api/v1/cards", map[string]any{"name": "4309"}, 201)
	cid := card["id"].(string)
	row := func(price int, extra map[string]any) string {
		m := map[string]any{"date": "2026-09-10", "cardId": cid}
		for k, v := range extra {
			m[k] = v
		}
		return e.ok("POST", "/api/v1/purchases", purchase(pid, price, 1, 0, m), 201)["id"].(string)
	}
	full, part := row(24000, map[string]any{"link": "https://item.fril.jp/F1"}), row(10000, nil)
	noCard := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, nil), 201)["id"].(string)

	// Waiting for a refund: a note next to the row
	if p := e.ok("PUT", "/api/v1/purchases/"+full+"/refund-note", map[string]any{"note": "Shop huỷ đơn, chờ hoàn 3-5 ngày"}, 200); p["refundNote"] != "Shop huỷ đơn, chờ hoàn 3-5 ngày" {
		t.Fatalf("refund note: %v", p)
	}
	e.ok("POST", "/api/v1/purchases/"+noCard+"/refund", map[string]any{}, 422)
	e.ok("POST", "/api/v1/purchases/"+part+"/refund", map[string]any{"amount": 3000, "removeRow": true}, 422) // only full refunds remove the row

	// Full refund, goods not coming: the row leaves purchases, the ledger keeps the charge and adds the refund
	e.ok("POST", "/api/v1/purchases/"+full+"/refund", map[string]any{"date": "2026-09-15", "note": "về thẻ", "removeRow": true}, 201)
	for _, p := range e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any) {
		if p.(map[string]any)["id"] == full {
			t.Fatal("refunded row still in purchases")
		}
	}
	// Partial refund (one item missing): the row stays
	e.ok("POST", "/api/v1/purchases/"+part+"/refund", map[string]any{"date": "2026-09-16", "amount": 3000}, 201)

	rows := e.ok("GET", "/api/v1/wallet-ledger?wallet=4309", nil, 200)["_"].([]any)
	got := []string{}
	var net int64
	for _, r := range rows {
		r := r.(map[string]any)
		got = append(got, r["date"].(string)+" "+r["category"].(string)+" "+fmt.Sprint(num(r["amount"])))
		net += int64(num(r["amount"]))
	}
	want := "2026-09-10 Mua hàng -24000|2026-09-10 Mua hàng -10000|2026-09-15 Hoàn tiền 24000|2026-09-16 Hoàn tiền 3000"
	if strings.Join(got, "|") != want || net != -7000 {
		t.Fatalf("ledger:\n%s\nwant\n%s (net %d)", strings.Join(got, "|"), want, net)
	}
	m := e.ok("GET", "/api/v1/wallet-ledger/totals?group=month&wallet=4309", nil, 200)["_"].([]any)
	if len(m) != 1 || num(m[0].(map[string]any)["amount"]) != -7000 {
		t.Fatalf("month total: %v", m)
	}
	refunds := e.ok("GET", "/api/v1/state", nil, 200)["cardRefunds"].([]any)
	if len(refunds) != 2 || refunds[0].(map[string]any)["purchaseId"] != "" || num(refunds[0].(map[string]any)["chargeAmount"]) != 24000 {
		t.Fatalf("refunds: %v", refunds)
	}
	// Deleting the refund of a removed row takes its kept charge too
	e.ok("DELETE", "/api/v1/card-refunds/"+refunds[0].(map[string]any)["id"].(string), nil, 204)
	if rows := e.ok("GET", "/api/v1/wallet-ledger?wallet=4309", nil, 200)["_"].([]any); len(rows) != 2 {
		t.Fatalf("after deleting refund: %v", rows)
	}
	e.ok("DELETE", "/api/v1/cards/"+cid, nil, 409) // has refunds and purchases
}

func TestReconciledMonthWarnsBeforeChanges(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	cid := e.ok("POST", "/api/v1/cards", map[string]any{"name": "4309"}, 201)["id"].(string)
	id := e.ok("POST", "/api/v1/purchases", purchase(pid, 5000, 1, 0, map[string]any{"date": "2026-09-10", "cardId": cid}), 201)["id"].(string)

	r := e.ok("PUT", "/api/v1/cards/"+cid+"/reconciliations/2026-09", nil, 200)
	if num(r["amount"]) != -5000 || r["month"] != "2026-09" {
		t.Fatalf("reconcile: %v", r)
	}
	e.ok("PUT", "/api/v1/cards/"+cid+"/reconciliations/2026-9", nil, 422)

	// Changing the price, adding or deleting a carded row in that month asks first; the note-only change does not
	edit := purchase(pid, 6000, 1, 0, map[string]any{"date": "2026-09-10"})
	if res := e.do("PUT", "/api/v1/purchases/"+id, edit, ""); res.Code != 409 || !strings.Contains(res.Body.String(), "Tháng 09/2026 của thẻ 4309 đã đối soát") {
		t.Fatalf("edit warning: %d %s", res.Code, res.Body)
	}
	e.ok("PUT", "/api/v1/purchases/"+id, purchase(pid, 5000, 1, 0, map[string]any{"date": "2026-09-10", "note": "ghi chú"}), 200)
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"date": "2026-09-11", "cardId": cid}), 409)
	other := e.ok("POST", "/api/v1/cards", map[string]any{"name": "JCB"}, 201)["id"].(string)
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"date": "2026-09-11", "cardId": other}), 201) // another card's month is not checked
	e.ok("DELETE", "/api/v1/purchases/"+id, nil, 409)
	e.ok("POST", "/api/v1/purchases/"+id+"/refund", map[string]any{"date": "2026-09-20"}, 409)
	e.ok("PUT", "/api/v1/purchases/"+id+"?force=true", edit, 200)

	// The kept amount shows that the month changed after the check
	if rec := e.ok("GET", "/api/v1/state", nil, 200)["cardReconciliations"].([]any); len(rec) != 1 || num(rec[0].(map[string]any)["amount"]) != -5000 {
		t.Fatalf("reconciliation kept: %v", rec)
	}
	// Picking the card from the table: a checked month asks first; the same card is a no-op
	if res := e.do("PUT", "/api/v1/purchases/"+id+"/card", map[string]any{"cardId": other}, ""); res.Code != 409 {
		t.Fatalf("card change in checked month: %d %s", res.Code, res.Body)
	}
	if p := e.ok("PUT", "/api/v1/purchases/"+id+"/card?force=true", map[string]any{"cardId": other}, 200); p["cardId"] != other {
		t.Fatalf("card set: %v", p)
	}
	e.ok("PUT", "/api/v1/purchases/"+id+"/card", map[string]any{"cardId": "999"}, 422)
	e.ok("DELETE", "/api/v1/cards/"+cid+"/reconciliations/2026-09", nil, 204)
	if p := e.ok("PUT", "/api/v1/purchases/"+id+"/card", map[string]any{"cardId": ""}, 200); p["cardId"] != "0" {
		t.Fatalf("card cleared: %v", p)
	}
	e.ok("DELETE", "/api/v1/purchases/"+id, nil, 204)
}

func TestPointPayment(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	visa := e.ok("POST", "/api/v1/cards", map[string]any{"name": "4356"}, 201)
	e.ok("POST", "/api/v1/purchases", purchase(pid, 25000, 1, 0, map[string]any{"cardId": visa["id"], "points": 5000}), 422) // no point wallet yet
	pt := e.ok("POST", "/api/v1/cards", map[string]any{"name": "Rakuma point", "currency": "PT"}, 201)
	e.ok("PATCH", "/api/v1/cards/"+pt["id"].(string), map[string]any{"default": true}, 422)
	e.ok("POST", "/api/v1/purchases", purchase(pid, 25000, 1, 0, map[string]any{"cardId": pt["id"]}), 422)                     // a point wallet is not a card
	e.ok("POST", "/api/v1/purchases", purchase(pid, 25000, 1, 0, map[string]any{"pointCardId": visa["id"], "points": 1}), 422) // nor the reverse
	e.ok("POST", "/api/v1/purchases", purchase(pid, 25000, 1, 0, map[string]any{"points": 25001}), 422)

	// The cost stays the full total (points are capital); the card pays the rest, the only point wallet is picked
	p := e.ok("POST", "/api/v1/purchases", purchase(pid, 25000, 1, 0, map[string]any{"date": "2026-09-20", "cardId": visa["id"], "points": 5000}), 201)
	if num(p["total"]) != 25000 || num(p["points"]) != 5000 || p["pointCardId"] != pt["id"] || p["pointCardName"] != "Rakuma point" {
		t.Fatalf("purchase: %v", p)
	}
	id := p["id"].(string)
	ledger := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, r := range e.ok("GET", "/api/v1/wallet-ledger", nil, 200)["_"].([]any) {
			out[r.(map[string]any)["id"].(string)] = r.(map[string]any)
		}
		return out
	}
	l := ledger()
	if c := l["rakuma:purchase:"+id]; c == nil || num(c["amount"]) != -20000 || c["wallet"] != "4356" || c["currency"] != "JPY" {
		t.Fatalf("card line: %v", l)
	}
	if c := l["rakuma:purchase:"+id+":point"]; c == nil || num(c["amount"]) != -5000 || c["wallet"] != "Rakuma point" || c["currency"] != "PT" || c["category"] != "Tiêu point" {
		t.Fatalf("point line: %v", l)
	}

	// Editing without points keeps them; paying all in points leaves no card line
	e.ok("PUT", "/api/v1/purchases/"+id, purchase(pid, 25000, 1, 0, map[string]any{"date": "2026-09-20"}), 200)
	if l = ledger(); len(l) != 2 {
		t.Fatalf("points kept: %v", l)
	}
	e.ok("PUT", "/api/v1/purchases/"+id, purchase(pid, 25000, 1, 0, map[string]any{"date": "2026-09-20", "points": 25000}), 200)
	if l = ledger(); len(l) != 1 || l["rakuma:purchase:"+id+":point"] == nil {
		t.Fatalf("all points: %v", l)
	}
	e.ok("POST", "/api/v1/purchases/"+id+"/refund", map[string]any{}, 422) // nothing went on the card

	// A refund gives back only the card part; removing the row needs that part in full
	e.ok("PUT", "/api/v1/purchases/"+id, purchase(pid, 25000, 1, 0, map[string]any{"date": "2026-09-20", "points": 5000}), 200)
	e.ok("POST", "/api/v1/purchases/"+id+"/refund", map[string]any{"amount": 25000}, 422)
	e.ok("POST", "/api/v1/purchases/"+id+"/refund", map[string]any{"amount": 20000, "date": "2026-09-25", "removeRow": true}, 201)
	l = ledger()
	if len(l) != 2 || num(l["rakuma:refund:1:charge"]["amount"]) != -20000 || num(l["rakuma:refund:1"]["amount"]) != 20000 {
		t.Fatalf("after refund: %v", l)
	}

	// Split: the points stay on the first row, which must hold them
	q := e.ok("POST", "/api/v1/purchases", purchase(pid, 30000, 1, 0, map[string]any{"date": "2026-09-21", "cardId": visa["id"], "points": 8000}), 201)
	qid := q["id"].(string)
	line := func(price int) map[string]any { return map[string]any{"productId": pid, "qty": 1, "price": price} }
	e.ok("POST", "/api/v1/purchases/"+qid+"/split", map[string]any{"lines": []any{line(5000), line(25000)}}, 422)
	e.ok("POST", "/api/v1/purchases/"+qid+"/split", map[string]any{"lines": []any{line(20000), line(10000)}}, 200)
	l = ledger()
	if num(l["rakuma:purchase:"+qid+":point"]["amount"]) != -8000 || num(l["rakuma:purchase:"+qid]["amount"]) != -12000 {
		t.Fatalf("split: %v", l)
	}
	e.ok("DELETE", "/api/v1/cards/"+pt["id"].(string), nil, 409)
}
