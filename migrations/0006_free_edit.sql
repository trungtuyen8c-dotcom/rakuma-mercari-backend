-- The owner works alone and edits any period, closed or not; one edit must flow into every later figure.
-- So only the first period keeps stored opening figures (the Excel carry-in); every later period's opening is
-- the previous period's live closing. Closing a period still records closing_cost/closing_revenue as a snapshot.
-- Totals can be corrected directly through per-period adjustments: adjust_cost/adjust_revenue for money and
-- stock_adjustments for each product's quantity.
ALTER TABLE periods
    ADD COLUMN adjust_cost    BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN adjust_revenue BIGINT NOT NULL DEFAULT 0;

CREATE TABLE stock_adjustments (
    period_id  BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    qty        INTEGER NOT NULL DEFAULT 0, -- added to the period's stock; negative = fewer items than recorded
    CONSTRAINT uniq_stock_adjustments_period_id_product_id PRIMARY KEY (period_id, product_id),
    CONSTRAINT fk_stock_adjustments_periods FOREIGN KEY (period_id) REFERENCES periods (id),
    CONSTRAINT fk_stock_adjustments_products FOREIGN KEY (product_id) REFERENCES products (id) ON DELETE CASCADE
);

DROP VIEW period_totals;
DROP VIEW stock_by_period;
DROP VIEW period_prev;

CREATE VIEW period_chain AS
SELECT id, row_number() OVER (ORDER BY start_date) AS n FROM periods;

-- BR-07, BR-08: current = opening + purchased (both sources) - sold + adjustment; opening chains from the period before
CREATE VIEW stock_by_period AS
WITH RECURSIVE mv AS (
    SELECT pc.n, pe.id AS period_id, pr.id AS product_id, pr.name, pr.is_active,
           COALESCE(so.qty, 0) AS stored_opening, COALESCE(pu.qty, 0) AS incoming, COALESCE(sa.qty, 0) AS sold,
           COALESCE(adj.qty, 0) AS adjust
    FROM periods pe
    JOIN period_chain pc ON pc.id = pe.id
    CROSS JOIN products pr
    LEFT JOIN stock_openings so ON so.period_id = pe.id AND so.product_id = pr.id
    LEFT JOIN stock_adjustments adj ON adj.period_id = pe.id AND adj.product_id = pr.id
    LEFT JOIN (SELECT period_id, product_id, SUM(quantity) AS qty FROM purchases GROUP BY 1, 2) pu
        ON pu.period_id = pe.id AND pu.product_id = pr.id
    LEFT JOIN (SELECT period_id, product_id, SUM(quantity) AS qty FROM sales GROUP BY 1, 2) sa
        ON sa.period_id = pe.id AND sa.product_id = pr.id
), chain AS (
    SELECT m.n, m.period_id, m.product_id, m.stored_opening::bigint AS opening,
           (m.stored_opening + m.incoming - m.sold + m.adjust)::bigint AS current
    FROM mv m WHERE m.n = 1
    UNION ALL
    SELECT m.n, m.period_id, m.product_id, c.current, (c.current + m.incoming - m.sold + m.adjust)::bigint
    FROM chain c JOIN mv m ON m.n = c.n + 1 AND m.product_id = c.product_id
)
SELECT m.period_id, m.product_id, m.name, m.is_active, c.opening::int AS opening, m.incoming::int AS incoming,
       m.sold::int AS sold, m.adjust::int AS adjust, c.current::int AS current
FROM mv m JOIN chain c ON c.period_id = m.period_id AND c.product_id = m.product_id;

-- BR-10..BR-13: money totals per period (cash-flow profit, BR-12; Q-02 open), chained the same way
CREATE VIEW period_totals AS
WITH RECURSIVE mv AS (
    SELECT pc.n, pe.id AS period_id, pe.opening_cost, pe.opening_revenue, pe.adjust_cost, pe.adjust_revenue,
           COALESCE(pu.amount, 0) AS period_cost, COALESCE(sa.amount, 0) AS period_revenue, COALESCE(sa.cnt, 0) AS sales_count
    FROM periods pe
    JOIN period_chain pc ON pc.id = pe.id
    LEFT JOIN (SELECT period_id, SUM(total) AS amount FROM purchases GROUP BY 1) pu ON pu.period_id = pe.id
    LEFT JOIN (SELECT period_id, SUM(total) AS amount, COUNT(*) AS cnt FROM sales GROUP BY 1) sa ON sa.period_id = pe.id
), chain AS (
    SELECT n, period_id, opening_cost::numeric AS prev_cost, opening_revenue::numeric AS prev_revenue,
           (opening_cost + period_cost + adjust_cost)::numeric AS total_cost,
           (opening_revenue + period_revenue + adjust_revenue)::numeric AS total_revenue
    FROM mv WHERE n = 1
    UNION ALL
    SELECT m.n, m.period_id, c.total_cost, c.total_revenue,
           c.total_cost + m.period_cost + m.adjust_cost, c.total_revenue + m.period_revenue + m.adjust_revenue
    FROM chain c JOIN mv m ON m.n = c.n + 1
)
SELECT
    m.period_id,
    c.prev_cost::bigint AS prev_cost,
    c.prev_revenue::bigint AS prev_revenue,
    (c.prev_revenue - c.prev_cost)::bigint AS prev_profit,
    m.period_cost::bigint AS period_cost,
    m.period_revenue::bigint AS period_revenue,
    (m.period_revenue - m.period_cost)::bigint AS period_profit,
    c.total_cost::bigint AS total_cost,
    c.total_revenue::bigint AS total_revenue,
    (c.total_revenue - c.total_cost)::bigint AS total_profit,
    m.sales_count::bigint AS sales_count,
    m.adjust_cost,
    m.adjust_revenue
FROM mv m JOIN chain c ON c.period_id = m.period_id;
