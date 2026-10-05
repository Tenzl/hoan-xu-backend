-- Never discard variable rates or historical snapshots through a rollback.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM affiliate_links WHERE tier_code IS NOT NULL) OR EXISTS(SELECT 1 FROM orders WHERE tier_code IS NOT NULL)
 THEN RAISE EXCEPTION 'Tiered links/orders exist; restore a backup instead of discarding financial snapshots'; END IF;
END $$;
DROP INDEX orders_approved_user;
DROP TRIGGER fixed_link_share ON affiliate_links;
DROP TRIGGER fixed_order_share ON orders;
DROP FUNCTION fill_fixed_link_share();
DROP FUNCTION fill_fixed_order_share();
DROP TABLE cashback_tiers;
DELETE FROM cashback_policies WHERE mode='tiered' AND NOT EXISTS(SELECT 1 FROM affiliate_links WHERE policy_id=cashback_policies.id) AND NOT EXISTS(SELECT 1 FROM orders WHERE policy_id=cashback_policies.id);
ALTER TABLE affiliate_links DROP COLUMN tier_code,DROP COLUMN min_share_bps,DROP COLUMN max_share_bps;
ALTER TABLE orders DROP COLUMN tier_code,DROP COLUMN share_bps;
ALTER TABLE cashback_policies DROP COLUMN mode;
