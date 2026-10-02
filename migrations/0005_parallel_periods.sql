-- The owner may open next month before closing the current one (old purchases still waiting for delivery or a
-- rating), so at most two periods are OPEN at once. While the earlier one is open, the later period's opening
-- figures are derived live from it; closing the earlier period (BR-14) stores the same figures.
DROP INDEX uniq_periods_open;

DROP VIEW period_totals;
DROP VIEW stock_by_period;

-- Each period with the period just before it, and whether that one is still open.
CREATE VIEW period_prev AS
SELECT pe.id, prev.id AS prev_id, COALESCE(prev.status = 'OPEN', FALSE) AS prev_open
FROM periods pe
LEFT JOIN LATERAL (
    SELECT p2.id, p2.status FROM periods p2 WHERE p2.start_date < pe.start_date ORDER BY p2.start_date DESC LIMIT 1
) prev ON TRUE;

-- BR-07, BR-08: stock per product per period = opening + purchased (both sources) - sold
CREATE VIEW stock_by_period AS
WITH mv AS (
    SELECT pe.id AS period_id, pr.id AS product_id, pr.name, pr.is_active,
           COALESCE(so.qty, 0) AS stored_opening, COALESCE(pu.qty, 0) AS incoming, COALESCE(sa.qty, 0) AS sold
    FROM periods pe
    CROSS JOIN products pr
    LEFT JOIN stock_openings so ON so.period_id = pe.id AND so.product_id = pr.id
    LEFT JOIN (SELECT period_id, product_id, SUM(quantity) AS qty FROM purchases GROUP BY 1, 2) pu
        ON pu.period_id = pe.id AND pu.product_id = pr.id
    LEFT JOIN (SELECT period_id, product_id, SUM(quantity) AS qty FROM sales GROUP BY 1, 2) sa
        ON sa.period_id = pe.id AND sa.product_id = pr.id
), op AS (
    SELECT m.*,
           CASE WHEN pp.prev_open THEN pm.stored_opening + pm.incoming - pm.sold ELSE m.stored_opening END AS opening
    FROM mv m
    JOIN period_prev pp ON pp.id = m.period_id
    LEFT JOIN mv pm ON pm.period_id = pp.prev_id AND pm.product_id = m.product_id
)
SELECT period_id, product_id, name, is_active, opening, incoming, sold, opening + incoming - sold AS current
FROM op;

-- BR-10..BR-13: money totals per period (cash-flow profit, BR-12; Q-02 open)
CREATE VIEW period_totals AS
WITH mv AS (
    SELECT pe.id AS period_id, pe.opening_cost, pe.opening_revenue,
           COALESCE(pu.amount, 0) AS period_cost, COALESCE(sa.amount, 0) AS period_revenue, COALESCE(sa.cnt, 0) AS sales_count
    FROM periods pe
    LEFT JOIN (SELECT period_id, SUM(total) AS amount FROM purchases GROUP BY 1) pu ON pu.period_id = pe.id
    LEFT JOIN (SELECT period_id, SUM(total) AS amount, COUNT(*) AS cnt FROM sales GROUP BY 1) sa ON sa.period_id = pe.id
), op AS (
    SELECT m.*,
           CASE WHEN pp.prev_open THEN pm.opening_cost + pm.period_cost ELSE m.opening_cost END AS prev_cost,
           CASE WHEN pp.prev_open THEN pm.opening_revenue + pm.period_revenue ELSE m.opening_revenue END AS prev_revenue
    FROM mv m
    JOIN period_prev pp ON pp.id = m.period_id
    LEFT JOIN mv pm ON pm.period_id = pp.prev_id
)
SELECT
    period_id,
    prev_cost,
    prev_revenue,
    prev_revenue - prev_cost AS prev_profit,
    period_cost,
    period_revenue,
    period_revenue - period_cost AS period_profit,
    prev_cost + period_cost AS total_cost,
    prev_revenue + period_revenue AS total_revenue,
    (prev_revenue + period_revenue) - (prev_cost + period_cost) AS total_profit,
    sales_count
FROM op;
