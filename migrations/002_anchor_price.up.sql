-- Rename base_price to anchor_price (hidden internal reference, not exposed in API)
ALTER TABLE market_items RENAME COLUMN base_price TO anchor_price;
