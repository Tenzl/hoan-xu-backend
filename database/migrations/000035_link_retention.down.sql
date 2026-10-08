-- Removed links cannot be restored by a schema rollback. Financial rows remain.
DROP INDEX affiliate_links_retention;
ALTER TABLE orders DROP CONSTRAINT orders_link_id_fkey;
ALTER TABLE orders ADD CONSTRAINT orders_link_id_fkey
 FOREIGN KEY(link_id) REFERENCES affiliate_links(id);
