-- Owner request (2026-10-05): an order still in progress that no longer appears in the Rakuma purchase lists
-- (cancelled, payment expired, removed) is flagged so the owner checks it. Cleared once it shows up again.
ALTER TABLE rakuma_orders ADD COLUMN missing_since TIMESTAMPTZ;
