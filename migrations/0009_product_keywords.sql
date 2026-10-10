-- Owner request (2026-10-10): words that identify a product in a Rakuma item title (e.g. "30th, セレブレーション"),
-- comma-separated, so approving a synced order can pick the product by itself.
ALTER TABLE products ADD COLUMN rakuma_keywords TEXT NOT NULL DEFAULT '';
