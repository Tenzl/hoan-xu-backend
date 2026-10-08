-- The deletion transaction preserves attribution before SET NULL detaches orders.
ALTER TABLE orders DROP CONSTRAINT orders_link_id_fkey;
ALTER TABLE orders ADD CONSTRAINT orders_link_id_fkey
 FOREIGN KEY(link_id) REFERENCES affiliate_links(id) ON DELETE SET NULL;
CREATE INDEX affiliate_links_retention ON affiliate_links(created_at,id)
 WHERE tracking_sub_ids IS NOT NULL;
