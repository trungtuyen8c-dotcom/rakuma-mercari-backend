-- TRUE while the order's tracking number was read from the seller's chat message (seller shipped without らくまパック)
-- rather than shown by Rakuma; such a number only fills an empty purchase row, never replaces one the owner typed.
ALTER TABLE rakuma_orders ADD COLUMN tracking_from_chat BOOLEAN NOT NULL DEFAULT FALSE;
