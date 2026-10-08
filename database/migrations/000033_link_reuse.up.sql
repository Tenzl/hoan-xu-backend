-- Keep signed attribution and financial history when hiding links from the list.
ALTER TABLE affiliate_links ADD COLUMN deleted_at timestamptz;
CREATE INDEX affiliate_links_visible_product ON affiliate_links(user_id,channel,original_url,created_at DESC)
 WHERE deleted_at IS NULL AND tracking_sub_ids IS NOT NULL;
ALTER TABLE affiliate_links DROP CONSTRAINT signed_saved_link_metadata;
ALTER TABLE affiliate_links ADD CONSTRAINT signed_saved_link_metadata CHECK (
 tracking_sub_ids IS NULL OR (
 jsonb_typeof(tracking_sub_ids)='array' AND jsonb_array_length(tracking_sub_ids)=5
 AND expires_at IS NOT NULL AND expires_at IN (created_at+interval '120 hours',created_at+interval '144 hours')
 AND payout_factor IS NOT NULL AND payout_factor BETWEEN 0 AND 1
 AND effective_share_bps IS NOT NULL AND lifecycle_status IS NOT NULL));
ALTER TABLE orders DROP CONSTRAINT signed_link_metadata;
ALTER TABLE orders ADD CONSTRAINT signed_link_metadata CHECK(cashback_mode <> 'signed_link' OR
 (tracking_code IS NOT NULL AND tracking_sub_ids IS NOT NULL AND link_created_at IS NOT NULL
 AND link_expires_at IS NOT NULL AND link_expires_at IN
 (link_created_at+interval '120 hours',link_created_at+interval '144 hours',link_created_at+interval '168 hours')));
