-- Points are capital the owner buys with VND, so a purchase paid partly with points keeps its full cost. A point
-- wallet ("Rakuma point") is a card row with currency 'PT' (1 point = ¥1 face value), matched across projects by name
-- like the cards; the hub prices points at their VND cost. The card is charged only the rest: total - points.
ALTER TABLE cards DROP CONSTRAINT cards_currency_check;
ALTER TABLE cards ADD CONSTRAINT cards_currency_check CHECK (currency IN ('JPY', 'PT'));
ALTER TABLE cards ADD CONSTRAINT cards_default_is_yen CHECK (NOT is_default OR currency = 'JPY');

ALTER TABLE purchases ADD COLUMN points BIGINT NOT NULL DEFAULT 0 CHECK (points >= 0);
ALTER TABLE purchases ADD COLUMN point_card_id BIGINT;
ALTER TABLE purchases ADD CONSTRAINT fk_purchases_point_cards FOREIGN KEY (point_card_id) REFERENCES cards (id) ON DELETE RESTRICT;
ALTER TABLE purchases ADD CONSTRAINT purchases_points_need_wallet CHECK (points = 0 OR point_card_id IS NOT NULL);
CREATE INDEX idx_purchases_point_card_id ON purchases (point_card_id) WHERE point_card_id IS NOT NULL;
