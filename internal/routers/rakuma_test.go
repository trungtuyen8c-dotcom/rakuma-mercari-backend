package routers_test

import (
	"strings"
	"testing"
)

func rakumaOrder(no string, extra map[string]any) map[string]any {
	m := map[string]any{
		"orderNo": no, "link": "https://item.fril.jp/" + no, "title": "MEGA 30th CELEBRATION", "status": "商品発送待ち",
		"date": "2026-09-20", "price": 18000, "discount": 900, "seller": "YAMATAKA",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// unpaid orders stay in the approval queue; paid ones become purchase rows on sync
var unpaid = map[string]any{"status": "支払い (期限 10/12 23:59)"}

func (e *env) sync(key string, orders ...map[string]any) map[string]any {
	e.t.Helper()
	res := e.do("POST", "/api/v1/rakuma/sync", map[string]any{"orders": orders}, key)
	if res.Code != 200 {
		e.t.Fatalf("sync: %d %s", res.Code, res.Body)
	}
	return e.ok("GET", "/api/v1/rakuma/orders", nil, 200)
}

func TestRakumaSyncUpsertsAndKeepsKnownFields(t *testing.T) {
	e := setup(t)
	write := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "claude", "scope": "write"}, 201)["key"].(string)
	read := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "r", "scope": "read"}, 201)["key"].(string)
	if res := e.do("POST", "/api/v1/rakuma/sync", map[string]any{"orders": []any{rakumaOrder("A1", nil)}}, read); res.Code != 403 {
		t.Fatalf("read key sync: %d", res.Code)
	}

	msg := map[string]any{"from": "seller", "at": "10/02 12:00", "body": "本日発送します"}
	e.sync(write, rakumaOrder("A1", map[string]any{"summary": "Gửi hôm nay", "messages": []any{msg}}))
	// Second sync: same message again, tracking appears, date and summary missing
	list := e.sync(write, rakumaOrder("A1", map[string]any{"date": "", "summary": "", "tracking": "1234-5678-9012",
		"status": "発送済み", "messages": []any{msg}}))["_"].([]any)
	if len(list) != 1 {
		t.Fatalf("want 1 order, got %d", len(list))
	}
	o := list[0].(map[string]any)
	if o["date"] != "2026-09-20" || o["summary"] != "Gửi hôm nay" || o["tracking"] != "1234-5678-9012" || o["status"] != "発送済み" {
		t.Fatalf("upsert lost data: %v", o)
	}
	if len(o["messages"].([]any)) != 1 || num(o["newMessages"]) != 1 {
		t.Fatalf("message not deduplicated: %v", o)
	}

	// Marking handled clears the unread count; a later seller message counts again
	id := o["id"].(string)
	if out := e.ok("POST", "/api/v1/rakuma/orders/"+id+"/messages-handled", nil, 200); num(out["newMessages"]) != 0 {
		t.Fatalf("handled: %v", out)
	}
	o = e.sync(write, rakumaOrder("A1", map[string]any{"messages": []any{msg,
		map[string]any{"from": "seller", "at": "10/03 09:00", "body": "発送しました"}}}))["_"].([]any)[0].(map[string]any)
	if num(o["newMessages"]) != 1 {
		t.Fatalf("new message not counted: %v", o)
	}

	// Invalid rows reject the whole batch
	out := e.do("POST", "/api/v1/rakuma/sync", map[string]any{"orders": []any{rakumaOrder("B1", map[string]any{"price": -1})}}, write)
	if out.Code != 422 {
		t.Fatalf("invalid sync: %d %s", out.Code, out.Body)
	}
}

func TestRakumaApproveCreatesPurchaseAndTracksLater(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	o := e.sync("", rakumaOrder("A1", unpaid))["_"].([]any)[0].(map[string]any)
	id := o["id"].(string)

	// The owner splits a lump order: qty and per-unit figures come from the form; link comes from the order
	p := e.ok("POST", "/api/v1/rakuma/orders/"+id+"/approve", purchase(pid, 18000, 1, 900, map[string]any{"date": "2026-09-20",
		"link": "https://evil.example"}), 201)
	if num(p["total"]) != 17100 || p["link"] != "https://item.fril.jp/A1" {
		t.Fatalf("approved purchase: %v", p)
	}
	e.ok("POST", "/api/v1/rakuma/orders/"+id+"/approve", purchase(pid, 18000, 1, 0, nil), 409)

	// Tracking that shows up on a later sync is copied onto the purchase
	e.sync("", rakumaOrder("A1", map[string]any{"tracking": "TRK-1"}))
	got := e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)[0].(map[string]any)
	if got["tracking"] != "TRK-1" {
		t.Fatalf("tracking not copied: %v", got)
	}

	// Closed periods stay editable (owner decision 2026-10-03), so a later tracking number is still copied
	e.ok("POST", "/api/v1/periods/close", nil, 200)
	e.sync("", rakumaOrder("A1", map[string]any{"tracking": "TRK-2"}))
	got = e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)[0].(map[string]any)
	if got["tracking"] != "TRK-2" {
		t.Fatalf("closed purchase not updated: %v", got)
	}
}

func TestRakumaUnknownPriceAndTitleStayBlank(t *testing.T) {
	e := setup(t)
	o := e.sync("", rakumaOrder("U1", map[string]any{"price": "", "discount": "", "title": ""}))["_"].([]any)[0].(map[string]any)
	if num(o["price"]) != 0 || o["title"] != "" {
		t.Fatalf("unknown fields: %v", o)
	}
	// A later sync that reads them fills them in; one that misses them again keeps what is stored
	e.sync("", rakumaOrder("U1", nil))
	o = e.sync("", rakumaOrder("U1", map[string]any{"price": "", "discount": "", "title": ""}))["_"].([]any)[0].(map[string]any)
	if num(o["price"]) != 18000 || num(o["discount"]) != 900 || o["title"] != "MEGA 30th CELEBRATION" {
		t.Fatalf("known fields lost: %v", o)
	}
}

func TestRakumaSyncWarnsDuplicates(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"link": "https://item.fril.jp/D1", "tracking": "TRK-9"}), 201)
	sync := func(o map[string]any) []any {
		t.Helper()
		return e.ok("POST", "/api/v1/rakuma/sync", map[string]any{"orders": []any{o}}, 200)["warnings"].([]any)
	}

	// A queued order whose tracking number is on another row is reported
	if w := sync(rakumaOrder("D3", map[string]any{"tracking": "TRK-9"})); len(w) != 1 {
		t.Fatalf("queued duplicates: %v", w)
	}
	if w := sync(rakumaOrder("D2", unpaid)); len(w) != 0 {
		t.Fatalf("clean order warned: %v", w)
	}

	// An approved order whose later tracking number repeats another row's
	var id string
	for _, o := range e.ok("GET", "/api/v1/rakuma/orders", nil, 200)["_"].([]any) {
		if o := o.(map[string]any); o["orderNo"] == "D2" {
			id = o["id"].(string)
		}
	}
	e.ok("POST", "/api/v1/rakuma/orders/"+id+"/approve", purchase(pid, 18000, 1, 0, nil), 201)
	if w := sync(rakumaOrder("D2", map[string]any{"tracking": "TRK-9"})); len(w) != 1 {
		t.Fatalf("tracking duplicate after approve: %v", w)
	}
}

func TestRakumaSyncLinksPurchaseEnteredByHand(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	p := e.ok("POST", "/api/v1/purchases", purchase(pid, 1000, 1, 0, map[string]any{"link": "https://item.fril.jp/H1"}), 201)

	res := e.ok("POST", "/api/v1/rakuma/sync", map[string]any{"orders": []any{rakumaOrder("H1", map[string]any{"tracking": "TRK-H"})}}, 200)
	if num(res["linked"]) != 1 || len(res["warnings"].([]any)) != 0 {
		t.Fatalf("hand-entered order not linked: %v", res)
	}
	o := e.ok("GET", "/api/v1/rakuma/orders", nil, 200)["_"].([]any)[0].(map[string]any)
	if o["purchaseId"] != p["id"] {
		t.Fatalf("order not attached to purchase %v: %v", p["id"], o)
	}
	got := e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)[0].(map[string]any)
	if got["tracking"] != "TRK-H" {
		t.Fatalf("tracking not copied onto the linked row: %v", got)
	}
	// Re-sync is a no-op; a second order with the same link stays queued with a warning
	res = e.ok("POST", "/api/v1/rakuma/sync", map[string]any{"orders": []any{rakumaOrder("H1", nil),
		rakumaOrder("H2", map[string]any{"link": "https://item.fril.jp/H1"})}}, 200)
	if num(res["linked"]) != 0 || len(res["warnings"].([]any)) != 1 {
		t.Fatalf("re-sync: %v", res)
	}
}

func TestRakumaSyncWritesPaidOrdersToPurchases(t *testing.T) {
	e := setup(t)
	mega := e.product("MEGA 30th")
	e.ok("PATCH", "/api/v1/products/"+mega, map[string]any{"keywords": "30th celebration"}, 200)
	e.product("Storm Emerald")
	purchases := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, p := range e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any) {
			p := p.(map[string]any)
			out[p["link"].(string)] = p
		}
		return out
	}

	res := e.ok("POST", "/api/v1/rakuma/sync", map[string]any{"orders": []any{
		// known product, lump "4BOX" price that divides evenly
		rakumaOrder("P1", map[string]any{"title": "ポケモン 30th CELEBRATION 4BOX", "price": 100000, "discount": 3000, "tracking": "TRK-P1"}),
		// unknown product, lump price that does not divide: one row at the lump price, product left empty
		rakumaOrder("P2", map[string]any{"title": "まとめ売り 3BOX", "price": 70000, "discount": 0}),
		// unpaid: stays queued
		rakumaOrder("P3", unpaid),
	}}, 200)
	if num(res["purchased"]) != 2 {
		t.Fatalf("purchased: %v", res)
	}
	got := purchases()
	p1, p2 := got["https://item.fril.jp/P1"], got["https://item.fril.jp/P2"]
	if p1["productId"] != mega || num(p1["qty"]) != 4 || num(p1["price"]) != 25000 || num(p1["discount"]) != 750 ||
		num(p1["total"]) != 97000 || p1["tracking"] != "TRK-P1" || p1["date"] != "2026-09-20" {
		t.Fatalf("P1 row: %v", p1)
	}
	if p2["productId"] != "0" || p2["productName"] != "" || num(p2["qty"]) != 1 || num(p2["price"]) != 70000 {
		t.Fatalf("P2 row: %v", p2)
	}
	if _, ok := got["https://item.fril.jp/P3"]; ok {
		t.Fatal("unpaid order written")
	}

	// A row without a product can be edited (price first, product later), and a new manual row still needs one
	e.ok("PUT", "/api/v1/purchases/"+p2["id"].(string), purchase("", 23000, 3, 0, map[string]any{"date": "2026-09-20",
		"link": "https://item.fril.jp/P2"}), 200)
	e.ok("PUT", "/api/v1/purchases/"+p2["id"].(string), purchase(mega, 23000, 3, 0, map[string]any{"date": "2026-09-20",
		"link": "https://item.fril.jp/P2"}), 200)
	if p := purchases()["https://item.fril.jp/P2"]; p["productId"] != mega || num(p["total"]) != 69000 {
		t.Fatalf("edited P2: %v", p)
	}
	e.ok("POST", "/api/v1/purchases", purchase("", 1000, 1, 0, nil), 422)

	// Re-sync writes nothing twice; once paid, the queued order becomes a row too
	res = e.ok("POST", "/api/v1/rakuma/sync", map[string]any{"orders": []any{rakumaOrder("P1", nil), rakumaOrder("P3", nil)}}, 200)
	if num(res["purchased"]) != 1 || len(purchases()) != 3 {
		t.Fatalf("re-sync: %v", res)
	}
}

func TestRakumaFlagsOrdersMissingFromRakuma(t *testing.T) {
	e := setup(t)
	sync := func(listed []string, orders ...map[string]any) []any {
		t.Helper()
		if orders == nil {
			orders = []map[string]any{}
		}
		return e.ok("POST", "/api/v1/rakuma/sync", map[string]any{"orders": orders, "listedLinks": listed}, 200)["warnings"].([]any)
	}
	byNo := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, o := range e.ok("GET", "/api/v1/rakuma/orders", nil, 200)["_"].([]any) {
			out[o.(map[string]any)["orderNo"].(string)] = o.(map[string]any)
		}
		return out
	}
	link := func(no string) string { return "https://item.fril.jp/" + no }
	sync(nil, rakumaOrder("M1", nil), rakumaOrder("M2", nil), rakumaOrder("M3", map[string]any{"status": "取引完了 (09/21)"}))

	// M1 drops off the lists (e.g. payment expired); finished M3 falls off 購入済's first page and is not flagged
	if w := sync([]string{link("M2")}); len(w) != 1 {
		t.Fatalf("missing warnings: %v", w)
	}
	got := byNo()
	if got["M1"]["missingSince"] == "" || got["M2"]["missingSince"] != "" || got["M3"]["missingSince"] != "" {
		t.Fatalf("missing flags: %v", got)
	}
	// Still reported on the next sync, cleared once listed again
	if w := sync([]string{link("M2")}); len(w) != 1 {
		t.Fatalf("still missing: %v", w)
	}
	if w := sync([]string{link("M1"), link("M2")}); len(w) != 0 || byNo()["M1"]["missingSince"] != "" {
		t.Fatalf("not cleared: %v", w)
	}
	// Syncs without listedLinks (older callers) change nothing
	if w := sync(nil, rakumaOrder("M2", nil)); len(w) != 0 {
		t.Fatalf("no listing: %v", w)
	}
}

func TestRakumaDismissAndState(t *testing.T) {
	e := setup(t)
	id := e.sync("", rakumaOrder("A1", nil))["_"].([]any)[0].(map[string]any)["id"].(string)
	if out := e.ok("POST", "/api/v1/rakuma/orders/"+id+"/dismiss", map[string]any{"dismissed": true}, 200); out["dismissed"] != true {
		t.Fatalf("dismiss: %v", out)
	}
	st := e.ok("GET", "/api/v1/state", nil, 200)
	if r := st["rakuma"].([]any); len(r) != 1 || r[0].(map[string]any)["dismissed"] != true {
		t.Fatalf("state rakuma: %v", st["rakuma"])
	}
	e.ok("POST", "/api/v1/rakuma/orders/999/dismiss", map[string]any{"dismissed": true}, 404)
}

func TestRakumaRatingAndIssueNote(t *testing.T) {
	e := setup(t)
	pid := e.product("MEGA 30th")
	id := e.sync("", rakumaOrder("A1", map[string]any{"image": "https://img.fril.jp/a.jpg", "status": unpaid["status"]}))["_"].([]any)[0].(map[string]any)["id"].(string)
	e.ok("POST", "/api/v1/rakuma/orders/"+id+"/approve", purchase(pid, 18000, 1, 900, nil), 201)

	out := e.ok("PATCH", "/api/v1/rakuma/orders/"+id, map[string]any{"issueNote": "  Hộp bị móp, đã nhắn shop  "}, 200)
	if out["issueNote"] != "Hộp bị móp, đã nhắn shop" || out["image"] != "https://img.fril.jp/a.jpg" {
		t.Fatalf("issue note: %v", out)
	}
	// Rating keeps the note and ticks "reviewed" on the linked purchase
	out = e.ok("PATCH", "/api/v1/rakuma/orders/"+id, map[string]any{"rating": "GOOD"}, 200)
	if out["rating"] != "GOOD" || out["issueNote"] != "Hộp bị móp, đã nhắn shop" {
		t.Fatalf("rating: %v", out)
	}
	if p := e.ok("GET", "/api/v1/purchases", nil, 200)["_"].([]any)[0].(map[string]any); p["reviewed"] != true {
		t.Fatalf("purchase not reviewed: %v", p)
	}
	e.ok("PATCH", "/api/v1/rakuma/orders/"+id, map[string]any{"rating": "SUPER"}, 422)
}

func TestRakumaRepliesFlow(t *testing.T) {
	e := setup(t)
	write := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "claude", "scope": "write"}, 201)["key"].(string)
	id := e.sync(write, rakumaOrder("A1", nil))["_"].([]any)[0].(map[string]any)["id"].(string)

	e.ok("POST", "/api/v1/rakuma/orders/"+id+"/replies", map[string]any{"body": " "}, 422)
	out := e.ok("POST", "/api/v1/rakuma/orders/"+id+"/replies", map[string]any{"body": "Cảm ơn, mình chờ hàng nhé"}, 201)
	r := out["replies"].([]any)[0].(map[string]any)
	if r["status"] != "PENDING" || r["bodyVi"] != "Cảm ơn, mình chờ hàng nhé" {
		t.Fatalf("reply: %v", r)
	}
	rid := r["id"].(string)

	// Only the owner writes replies; Claude's write key marks them sent, once
	if res := e.do("POST", "/api/v1/rakuma/orders/"+id+"/replies", map[string]any{"body": "x"}, write); res.Code != 403 {
		t.Fatalf("write key reply: %d", res.Code)
	}
	if res := e.do("POST", "/api/v1/rakuma/replies/"+rid+"/sent", map[string]any{"bodyJa": "ありがとうございます。"}, write); res.Code != 200 {
		t.Fatalf("mark sent: %d %s", res.Code, res.Body)
	}
	if res := e.do("POST", "/api/v1/rakuma/replies/"+rid+"/sent", map[string]any{"bodyJa": "again"}, write); res.Code != 409 {
		t.Fatalf("second send: %d", res.Code)
	}
	e.ok("DELETE", "/api/v1/rakuma/replies/"+rid, nil, 409)
	got := e.ok("GET", "/api/v1/rakuma/orders", nil, 200)["_"].([]any)[0].(map[string]any)["replies"].([]any)[0].(map[string]any)
	if got["status"] != "SENT" || got["bodyJa"] != "ありがとうございます。" || got["sentAt"] == "" {
		t.Fatalf("sent reply: %v", got)
	}

	// A pending reply can be cancelled
	rid2 := e.ok("POST", "/api/v1/rakuma/orders/"+id+"/replies", map[string]any{"body": "Hủy tin này"}, 201)["replies"].([]any)[1].(map[string]any)["id"].(string)
	if out := e.ok("DELETE", "/api/v1/rakuma/replies/"+rid2, nil, 200); len(out["replies"].([]any)) != 1 {
		t.Fatalf("delete reply: %v", out)
	}
	e.ok("DELETE", "/api/v1/rakuma/replies/999", nil, 404)
}

func TestRakumaBroadcastSkipsClosedChats(t *testing.T) {
	e := setup(t)
	write := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "claude", "scope": "write"}, 201)["key"].(string)
	closed := false
	list := e.sync(write, rakumaOrder("A1", nil), rakumaOrder("B1", map[string]any{"chatOpen": closed}), rakumaOrder("C1", nil))["_"].([]any)
	ids := map[string]string{}
	for _, o := range list {
		m := o.(map[string]any)
		ids[m["orderNo"].(string)] = m["id"].(string)
	}
	// A later sync without chatOpen keeps the stored value
	e.sync(write, rakumaOrder("B1", nil))

	e.ok("POST", "/api/v1/rakuma/broadcast", map[string]any{"orderIds": []string{}, "body": "Còn hàng không?"}, 422)
	out := e.ok("POST", "/api/v1/rakuma/broadcast", map[string]any{
		"orderIds": []string{ids["A1"], ids["B1"], ids["C1"], ids["C1"], "999"}, "body": "Shop còn hàng không?"}, 201)
	if num(out["queued"]) != 2 || num(out["skipped"]) != 2 {
		t.Fatalf("broadcast: %v", out)
	}
	e.ok("POST", "/api/v1/rakuma/orders/"+ids["B1"]+"/replies", map[string]any{"body": "x"}, 409)

	// Claude finds C1's chat closed when sending: the reply is skipped and the order marked closed
	var rid string
	for _, o := range e.ok("GET", "/api/v1/rakuma/orders", nil, 200)["_"].([]any) {
		m := o.(map[string]any)
		if m["orderNo"] == "C1" {
			r := m["replies"].([]any)[0].(map[string]any)
			if r["kind"] != "BROADCAST" {
				t.Fatalf("kind: %v", r)
			}
			rid = r["id"].(string)
		}
	}
	res := e.do("POST", "/api/v1/rakuma/replies/"+rid+"/skip", map[string]any{"reason": "取引メッセージ非公開", "chatClosed": true}, write)
	if res.Code != 200 {
		t.Fatalf("skip: %d %s", res.Code, res.Body)
	}
	o := e.ok("GET", "/api/v1/rakuma/orders", nil, 200)["_"].([]any)
	for _, x := range o {
		m := x.(map[string]any)
		if m["orderNo"] == "C1" && (m["chatOpen"] != false || m["replies"].([]any)[0].(map[string]any)["status"] != "SKIPPED") {
			t.Fatalf("skipped order: %v", m)
		}
	}
	if res := e.do("POST", "/api/v1/rakuma/replies/"+rid+"/sent", map[string]any{"bodyJa": "x"}, write); res.Code != 409 {
		t.Fatalf("send after skip: %d", res.Code)
	}
}

// Owner request 2026-10-10: Claude stores the Japanese text first; the owner sends it from the Chrome extension.
func TestRakumaReplyTranslation(t *testing.T) {
	e := setup(t)
	write := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "claude", "scope": "write"}, 201)["key"].(string)
	read := e.ok("POST", "/api/v1/api-keys", map[string]string{"name": "r", "scope": "read"}, 201)["key"].(string)
	id := e.sync(write, rakumaOrder("A1", nil))["_"].([]any)[0].(map[string]any)["id"].(string)
	rid := e.ok("POST", "/api/v1/rakuma/orders/"+id+"/replies", map[string]any{"body": "Cảm ơn shop"}, 201)["replies"].([]any)[0].(map[string]any)["id"].(string)
	path := "/api/v1/rakuma/replies/" + rid + "/translation"

	if res := e.do("PUT", path, map[string]any{"bodyJa": "ありがとうございます。"}, read); res.Code != 403 {
		t.Fatalf("read key: %d", res.Code)
	}
	if res := e.do("PUT", path, map[string]any{"bodyJa": "  "}, write); res.Code != 422 {
		t.Fatalf("empty: %d", res.Code)
	}
	if res := e.do("PUT", path, map[string]any{"bodyJa": strings.Repeat("あ", 251)}, write); res.Code != 422 {
		t.Fatalf("over 250: %d", res.Code)
	}
	if res := e.do("PUT", path, map[string]any{"bodyJa": " ありがとうございます。 "}, write); res.Code != 200 {
		t.Fatalf("save: %d %s", res.Code, res.Body)
	}
	got := e.ok("GET", "/api/v1/rakuma/orders", nil, 200)["_"].([]any)[0].(map[string]any)["replies"].([]any)[0].(map[string]any)
	if got["status"] != "PENDING" || got["bodyJa"] != "ありがとうございます。" {
		t.Fatalf("translated reply should stay pending with its Japanese text: %v", got)
	}
	// Once sent it is final
	e.ok("POST", "/api/v1/rakuma/replies/"+rid+"/sent", map[string]any{"bodyJa": "ありがとうございます。"}, 200)
	if res := e.do("PUT", path, map[string]any{"bodyJa": "変更"}, write); res.Code != 409 {
		t.Fatalf("after sent: %d", res.Code)
	}
}
