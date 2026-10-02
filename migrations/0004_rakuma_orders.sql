-- Orders pulled from the owner's Rakuma purchase history. A row waits in the review queue until the owner picks a
-- product (purchase_id set) or dismisses it. Re-syncs upsert by order_no and refresh status, tracking and messages.
-- The buyer's shipping address is shown on Rakuma but is never stored.
CREATE TABLE rakuma_orders (
    id                  BIGSERIAL PRIMARY KEY,
    order_no            TEXT NOT NULL,
    item_url            TEXT NOT NULL,
    title               TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT '',
    order_date          DATE,
    price               BIGINT NOT NULL CHECK (price > 0),
    discount            BIGINT NOT NULL DEFAULT 0 CHECK (discount >= 0 AND discount <= price),
    carrier             TEXT NOT NULL DEFAULT '',
    tracking_no         TEXT,
    seller              TEXT NOT NULL DEFAULT '',
    summary             TEXT NOT NULL DEFAULT '',
    reply_draft         TEXT NOT NULL DEFAULT '',
    purchase_id         BIGINT,
    is_dismissed        BOOLEAN NOT NULL DEFAULT FALSE,
    messages_handled_at TIMESTAMPTZ,
    synced_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_rakuma_orders_purchases FOREIGN KEY (purchase_id) REFERENCES purchases (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX uniq_rakuma_orders_order_no ON rakuma_orders (order_no);
CREATE INDEX idx_rakuma_orders_purchase_id ON rakuma_orders (purchase_id) WHERE purchase_id IS NOT NULL;

-- Transaction messages. sent_at is Rakuma's display text, kept as-is; a message is new when created_at is after
-- the order's messages_handled_at.
CREATE TABLE rakuma_messages (
    id         BIGSERIAL PRIMARY KEY,
    order_id   BIGINT NOT NULL,
    sender     TEXT NOT NULL CHECK (sender IN ('seller', 'buyer')),
    sent_at    TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_rakuma_messages_rakuma_orders FOREIGN KEY (order_id) REFERENCES rakuma_orders (id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX uniq_rakuma_messages_order_id_content ON rakuma_messages (order_id, sender, sent_at, md5(body));
