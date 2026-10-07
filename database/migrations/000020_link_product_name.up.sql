ALTER TABLE affiliate_links ADD COLUMN product_name text;
CREATE INDEX orders_owner_legacy_link ON orders(user_id,link_id) WHERE link_id IS NOT NULL;
