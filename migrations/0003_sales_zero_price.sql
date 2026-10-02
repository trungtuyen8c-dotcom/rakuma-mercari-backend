-- Owner decision (2026-10-02): a sale at 0¥ records opened stock ("bóc hàng"): it reduces stock with no revenue.
ALTER TABLE sales DROP CONSTRAINT sales_unit_price_check;
ALTER TABLE sales ADD CONSTRAINT sales_unit_price_check CHECK (unit_price >= 0);
