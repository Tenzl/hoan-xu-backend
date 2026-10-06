-- Additive: historical links and financial records are not backfilled or removed.
ALTER TABLE affiliate_links ADD COLUMN tracking_sub_ids jsonb,
 ADD COLUMN expires_at timestamptz,
 ADD COLUMN payout_factor numeric(3,2),
 ADD COLUMN effective_share_bps integer CHECK(effective_share_bps BETWEEN 0 AND 10000),
 ADD COLUMN lifecycle_status text CHECK(lifecycle_status IN ('active','cancelled')),
 ADD COLUMN cancellation_notified_at timestamptz;
ALTER TABLE affiliate_links ADD CONSTRAINT signed_saved_link_metadata CHECK (
 tracking_sub_ids IS NULL OR (
 jsonb_typeof(tracking_sub_ids)='array' AND jsonb_array_length(tracking_sub_ids)=5
 AND expires_at IS NOT NULL AND expires_at=created_at+interval '144 hours'
 AND payout_factor IS NOT NULL AND payout_factor BETWEEN 0 AND 1
 AND effective_share_bps IS NOT NULL AND lifecycle_status IS NOT NULL));
CREATE INDEX affiliate_links_expiry ON affiliate_links(expires_at,id)
 WHERE tracking_sub_ids IS NOT NULL AND cancellation_notified_at IS NULL;
CREATE INDEX orders_link_tracking ON orders(user_id,tracking_code,status) WHERE tracking_code IS NOT NULL;
ALTER TABLE orders DROP CONSTRAINT signed_link_metadata;
ALTER TABLE orders ADD CONSTRAINT signed_link_metadata CHECK(cashback_mode <> 'signed_link' OR
 (tracking_code IS NOT NULL AND tracking_sub_ids IS NOT NULL AND link_created_at IS NOT NULL
 AND link_expires_at IS NOT NULL AND link_expires_at IN
 (link_created_at+interval '144 hours',link_created_at+interval '168 hours')));
