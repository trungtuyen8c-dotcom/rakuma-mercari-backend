-- Core schema (spec §10, §15.2). Money is integer yen (BIGINT). Stock and reports are views, never stored.

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO settings (key, value) VALUES
    ('exchange_rate', '175'),
    ('default_service_fee', '0'),
    ('default_intl_shipping', '0'),
    ('import_tax_rate', '0');

CREATE TABLE products (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL CHECK (btrim(name) <> ''),
    is_active  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- BR-01: unique name, ignoring leading/trailing spaces
CREATE UNIQUE INDEX uniq_products_name ON products (btrim(name));

CREATE TABLE periods (
    id              BIGSERIAL PRIMARY KEY,
    label           TEXT NOT NULL,
    start_date      DATE NOT NULL,
    end_date        DATE NOT NULL CHECK (end_date >= start_date),
    status          TEXT NOT NULL CHECK (status IN ('OPEN', 'CLOSED')),
    opening_cost    BIGINT NOT NULL CHECK (opening_cost >= 0),
    opening_revenue BIGINT NOT NULL CHECK (opening_revenue >= 0),
    closing_cost    BIGINT,
    closing_revenue BIGINT,
    closed_at       TIMESTAMPTZ,
    CHECK ((status = 'OPEN') = (closed_at IS NULL))
);
CREATE UNIQUE INDEX uniq_periods_label ON periods (label);
-- Exactly one open period at a time
CREATE UNIQUE INDEX uniq_periods_open ON periods (status) WHERE status = 'OPEN';

CREATE TABLE purchases (
    id            BIGSERIAL PRIMARY KEY,
    period_id     BIGINT NOT NULL,
    source        TEXT NOT NULL CHECK (source IN ('REGULAR', 'BULK')),
    order_date    DATE,
    item_url      TEXT,
    product_id    BIGINT NOT NULL,
    unit_price    BIGINT NOT NULL CHECK (unit_price > 0),
    quantity      INTEGER NOT NULL CHECK (quantity >= 1),
    unit_discount BIGINT NOT NULL DEFAULT 0 CHECK (unit_discount >= 0 AND unit_discount <= unit_price),
    total         BIGINT GENERATED ALWAYS AS ((unit_price - unit_discount) * quantity) STORED, -- BR-03
    tracking_no   TEXT,
    merge_group   TEXT,
    is_checked    BOOLEAN NOT NULL DEFAULT FALSE,
    is_reviewed   BOOLEAN NOT NULL DEFAULT FALSE,
    note          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_purchases_periods FOREIGN KEY (period_id) REFERENCES periods (id),
    CONSTRAINT fk_purchases_products FOREIGN KEY (product_id) REFERENCES products (id)
);
CREATE INDEX idx_purchases_period_id ON purchases (period_id);
CREATE INDEX idx_purchases_product_id ON purchases (product_id);
CREATE INDEX idx_purchases_item_url ON purchases (item_url) WHERE item_url IS NOT NULL;
CREATE INDEX idx_purchases_tracking_no ON purchases (tracking_no) WHERE tracking_no IS NOT NULL;

CREATE TABLE sales (
    id            BIGSERIAL PRIMARY KEY,
    period_id     BIGINT NOT NULL,
    sale_date     DATE,
    product_id    BIGINT NOT NULL,
    quantity      INTEGER NOT NULL CHECK (quantity >= 1),
    unit_price    BIGINT NOT NULL CHECK (unit_price > 0),
    shipping_fee  BIGINT NOT NULL DEFAULT 0 CHECK (shipping_fee >= 0),
    total         BIGINT GENERATED ALWAYS AS (quantity * unit_price - shipping_fee) STORED, -- BR-04
    customer_name TEXT NOT NULL DEFAULT '',
    note          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_sales_periods FOREIGN KEY (period_id) REFERENCES periods (id),
    CONSTRAINT fk_sales_products FOREIGN KEY (product_id) REFERENCES products (id)
);
CREATE INDEX idx_sales_period_id ON sales (period_id);
CREATE INDEX idx_sales_product_id ON sales (product_id);

-- Opening qty per product per period. May be negative when a negative closing stock is carried forward (BR-14).
CREATE TABLE stock_openings (
    period_id  BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    qty        INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT uniq_stock_openings_period_id_product_id PRIMARY KEY (period_id, product_id),
    CONSTRAINT fk_stock_openings_periods FOREIGN KEY (period_id) REFERENCES periods (id),
    CONSTRAINT fk_stock_openings_products FOREIGN KEY (product_id) REFERENCES products (id) ON DELETE CASCADE
);

CREATE TABLE audit_logs (
    id        BIGSERIAL PRIMARY KEY,
    entity    TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    action    TEXT NOT NULL,
    actor     TEXT NOT NULL,
    before    JSONB,
    after     JSONB,
    at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_logs_entity_entity_id ON audit_logs (entity, entity_id);

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY, -- SHA-256 of the cookie token
    email      TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE api_keys (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    scope        TEXT NOT NULL CHECK (scope IN ('read', 'write')),
    key_hash     TEXT NOT NULL, -- SHA-256 hex; the full key is shown once
    masked       TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE UNIQUE INDEX uniq_api_keys_key_hash ON api_keys (key_hash);

-- BR-07, BR-08: stock per product per period = opening + purchased (both sources) - sold
CREATE VIEW stock_by_period AS
SELECT
    pe.id AS period_id,
    pr.id AS product_id,
    pr.name,
    pr.is_active,
    COALESCE(so.qty, 0) AS opening,
    COALESCE(pu.qty, 0) AS incoming,
    COALESCE(sa.qty, 0) AS sold,
    COALESCE(so.qty, 0) + COALESCE(pu.qty, 0) - COALESCE(sa.qty, 0) AS current
FROM periods pe
CROSS JOIN products pr
LEFT JOIN stock_openings so ON so.period_id = pe.id AND so.product_id = pr.id
LEFT JOIN (SELECT period_id, product_id, SUM(quantity) AS qty FROM purchases GROUP BY 1, 2) pu
    ON pu.period_id = pe.id AND pu.product_id = pr.id
LEFT JOIN (SELECT period_id, product_id, SUM(quantity) AS qty FROM sales GROUP BY 1, 2) sa
    ON sa.period_id = pe.id AND sa.product_id = pr.id;

-- BR-10..BR-13: money totals per period (cash-flow profit, BR-12; Q-02 open)
CREATE VIEW period_totals AS
SELECT
    pe.id AS period_id,
    pe.opening_cost AS prev_cost,
    pe.opening_revenue AS prev_revenue,
    pe.opening_revenue - pe.opening_cost AS prev_profit,
    COALESCE(pu.amount, 0) AS period_cost,
    COALESCE(sa.amount, 0) AS period_revenue,
    COALESCE(sa.amount, 0) - COALESCE(pu.amount, 0) AS period_profit,
    pe.opening_cost + COALESCE(pu.amount, 0) AS total_cost,
    pe.opening_revenue + COALESCE(sa.amount, 0) AS total_revenue,
    (pe.opening_revenue + COALESCE(sa.amount, 0)) - (pe.opening_cost + COALESCE(pu.amount, 0)) AS total_profit,
    COALESCE(sa.cnt, 0) AS sales_count
FROM periods pe
LEFT JOIN (SELECT period_id, SUM(total) AS amount FROM purchases GROUP BY 1) pu ON pu.period_id = pe.id
LEFT JOIN (SELECT period_id, SUM(total) AS amount, COUNT(*) AS cnt FROM sales GROUP BY 1) sa ON sa.period_id = pe.id;
