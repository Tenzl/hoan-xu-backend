-- Do not invalidate previously issued tokens or discard hidden-link state.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM affiliate_links WHERE deleted_at IS NOT NULL OR
  (tracking_sub_ids IS NOT NULL AND expires_at=created_at+interval '120 hours'))
 OR EXISTS(SELECT 1 FROM orders WHERE cashback_mode='signed_link' AND link_expires_at=link_created_at+interval '120 hours') THEN
  RAISE EXCEPTION 'Cannot roll back: retain support for deleted links and issued five-day tracking';
 END IF;
END $$;
DROP INDEX affiliate_links_visible_product;
ALTER TABLE affiliate_links DROP COLUMN deleted_at;
ALTER TABLE affiliate_links DROP CONSTRAINT signed_saved_link_metadata;
ALTER TABLE affiliate_links ADD CONSTRAINT signed_saved_link_metadata CHECK (
 tracking_sub_ids IS NULL OR (
 jsonb_typeof(tracking_sub_ids)='array' AND jsonb_array_length(tracking_sub_ids)=5
 AND expires_at IS NOT NULL AND expires_at=created_at+interval '144 hours'
 AND payout_factor IS NOT NULL AND payout_factor BETWEEN 0 AND 1
 AND effective_share_bps IS NOT NULL AND lifecycle_status IS NOT NULL));
ALTER TABLE orders DROP CONSTRAINT signed_link_metadata;
ALTER TABLE orders ADD CONSTRAINT signed_link_metadata CHECK(cashback_mode <> 'signed_link' OR
 (tracking_code IS NOT NULL AND tracking_sub_ids IS NOT NULL AND link_created_at IS NOT NULL
 AND link_expires_at IS NOT NULL AND link_expires_at IN
 (link_created_at+interval '144 hours',link_created_at+interval '168 hours')));
