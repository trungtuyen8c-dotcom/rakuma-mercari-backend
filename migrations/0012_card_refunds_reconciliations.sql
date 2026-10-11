-- Refunds and monthly statement checks on the shared payment cards.

-- Non-empty while the owner waits for a refund of this purchase (cancelled order, goods not coming), with the reason.
ALTER TABLE purchases ADD COLUMN refund_note TEXT NOT NULL DEFAULT '';

-- Money paid back to a card. The charge stays in the card ledger even when the purchase row is removed after a full
-- refund, so the ledger keeps both lines the card statement shows: the charge and the refund.
CREATE TABLE card_refunds (
    id            BIGSERIAL PRIMARY KEY,
    purchase_id   BIGINT, -- NULL once the purchase row was removed; the charge below then stands in for it
    card_id       BIGINT NOT NULL,
    charge_date   DATE NOT NULL,
    charge_amount BIGINT NOT NULL CHECK (charge_amount >= 0),
    refund_date   DATE NOT NULL,
    amount        BIGINT NOT NULL CHECK (amount > 0),
    label         TEXT NOT NULL DEFAULT '', -- what was refunded: period, row, product, link
    note          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_card_refunds_purchases FOREIGN KEY (purchase_id) REFERENCES purchases (id) ON DELETE SET NULL,
    CONSTRAINT fk_card_refunds_cards FOREIGN KEY (card_id) REFERENCES cards (id) ON DELETE RESTRICT
);
CREATE INDEX idx_card_refunds_purchase_id ON card_refunds (purchase_id) WHERE purchase_id IS NOT NULL;
CREATE INDEX idx_card_refunds_card_id ON card_refunds (card_id);

-- A card's month checked against its statement: the net amount at that moment, to show later edits.
CREATE TABLE card_reconciliations (
    card_id    BIGINT NOT NULL,
    month      DATE NOT NULL, -- first day of the month
    amount     BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_card_reconciliations PRIMARY KEY (card_id, month),
    CONSTRAINT fk_card_reconciliations_cards FOREIGN KEY (card_id) REFERENCES cards (id) ON DELETE RESTRICT
);
