-- Rows created by the Rakuma sync may not know their product yet; the owner fills it in later.
-- A row without a product counts in cost totals but in no product's stock.
ALTER TABLE purchases ALTER COLUMN product_id DROP NOT NULL;
