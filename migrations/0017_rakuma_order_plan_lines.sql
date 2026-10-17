-- A combined order ("đơn gộp") the owner splits in the extension preview: one line per product, each with qty, unit
-- price and the share of the coupon per item, adding up to what the link cost. The sync writes these as merged rows.
-- Shape: [{"productId": "56", "qty": 2, "price": 10000, "discount": 133}, ...]
ALTER TABLE rakuma_orders ADD COLUMN plan_lines JSONB;
