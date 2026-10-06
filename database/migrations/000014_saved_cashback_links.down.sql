-- Rolling application code back requires retaining support for already-issued v2 tokens.
-- Keep both validity intervals on orders to preserve financial records.
DROP INDEX orders_link_tracking;
DROP INDEX affiliate_links_expiry;
ALTER TABLE affiliate_links DROP CONSTRAINT signed_saved_link_metadata;
ALTER TABLE affiliate_links DROP COLUMN cancellation_notified_at, DROP COLUMN lifecycle_status,
 DROP COLUMN effective_share_bps, DROP COLUMN payout_factor, DROP COLUMN expires_at, DROP COLUMN tracking_sub_ids;
