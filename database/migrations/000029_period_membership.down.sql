DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM cashback_tiers WHERE min_gold_total IS NOT NULL) OR EXISTS(SELECT 1 FROM affiliate_links WHERE tier_code IN ('member','silver','gold')) OR EXISTS(SELECT 1 FROM orders WHERE tier_code IN ('member','silver','gold')) THEN
  RAISE EXCEPTION 'Period membership is in use; use a forward migration';
 END IF;
END $$;
DROP INDEX orders_membership_ordered;
DROP INDEX orders_membership_approved;
ALTER TABLE orders DROP CONSTRAINT orders_tier_code_check;
ALTER TABLE orders ADD CONSTRAINT orders_tier_code_check CHECK(tier_code IN ('bronze','platinum','diamond'));
ALTER TABLE affiliate_links DROP CONSTRAINT affiliate_links_tier_code_check;
ALTER TABLE affiliate_links ADD CONSTRAINT affiliate_links_tier_code_check CHECK(tier_code IN ('bronze','platinum','diamond'));
ALTER TABLE cashback_tiers DROP CONSTRAINT period_tier_fields, DROP CONSTRAINT period_tier_threshold, DROP COLUMN name_vi, DROP COLUMN name_en, DROP COLUMN min_gold_total, DROP COLUMN exchange_bonus_percent;
ALTER TABLE cashback_tiers DROP CONSTRAINT cashback_tiers_tier_code_check;
ALTER TABLE cashback_tiers ADD CONSTRAINT cashback_tiers_tier_code_check CHECK(tier_code IN ('bronze','platinum','diamond'));
ALTER TABLE cashback_tiers ALTER COLUMN min_approved_orders SET NOT NULL;
ALTER TABLE cashback_policies DROP COLUMN period_months, DROP COLUMN anchor_date, DROP COLUMN date_basis;
