-- Owner decision (2026-10-04): an order whose price or title could not be read from Rakuma is still synced, with
-- price 0 and an empty title, and the owner fills them in when approving it.
ALTER TABLE rakuma_orders DROP CONSTRAINT rakuma_orders_price_check;
ALTER TABLE rakuma_orders ADD CONSTRAINT rakuma_orders_price_check CHECK (price >= 0);
