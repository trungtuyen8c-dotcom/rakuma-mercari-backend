-- The owner's choice for an order, made in the extension's preview before syncing: which product and how many items.
-- The sync writes the purchase row with it (instead of guessing from the title) once the order is paid, and later
-- scans show it again instead of the original guess.
ALTER TABLE rakuma_orders ADD COLUMN plan_product_id BIGINT;
ALTER TABLE rakuma_orders ADD COLUMN plan_qty INT CHECK (plan_qty BETWEEN 1 AND 99);
ALTER TABLE rakuma_orders ADD CONSTRAINT fk_rakuma_orders_plan_products FOREIGN KEY (plan_product_id) REFERENCES products (id) ON DELETE SET NULL;
