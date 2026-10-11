-- Payment cards shared with the other projects (order, yahoo). A card is matched across projects by its exact name;
-- each project keeps only its own charges, and the order project holds the opening balance (its roadmap N3).
CREATE TABLE cards (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    currency   TEXT NOT NULL DEFAULT 'JPY' CHECK (currency IN ('JPY')),
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    is_active  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uniq_cards_name ON cards (lower(btrim(name)));
CREATE UNIQUE INDEX uniq_cards_is_default ON cards (is_default) WHERE is_default;

-- The card a purchase was paid with; its total is that card's charge ("Mua hàng") on the order date.
ALTER TABLE purchases ADD COLUMN card_id BIGINT;
ALTER TABLE purchases ADD CONSTRAINT fk_purchases_cards FOREIGN KEY (card_id) REFERENCES cards (id) ON DELETE RESTRICT;
CREATE INDEX idx_purchases_card_id ON purchases (card_id) WHERE card_id IS NOT NULL;
