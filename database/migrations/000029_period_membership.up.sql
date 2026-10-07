ALTER TABLE cashback_policies
 ADD COLUMN period_months integer NOT NULL DEFAULT 6 CHECK(period_months BETWEEN 1 AND 12),
 ADD COLUMN anchor_date date NOT NULL DEFAULT (date_trunc('year',now() AT TIME ZONE 'Asia/Ho_Chi_Minh'))::date,
 ADD COLUMN date_basis text NOT NULL DEFAULT 'approved' CHECK(date_basis IN ('approved','ordered'));
ALTER TABLE cashback_tiers ALTER COLUMN min_approved_orders DROP NOT NULL;
ALTER TABLE cashback_tiers DROP CONSTRAINT cashback_tiers_tier_code_check;
ALTER TABLE cashback_tiers
 ADD CONSTRAINT cashback_tiers_tier_code_check CHECK(tier_code IN ('bronze','platinum','diamond','member','silver','gold')),
 ADD COLUMN name_vi text CHECK(char_length(btrim(name_vi)) BETWEEN 1 AND 40),
 ADD COLUMN name_en text CHECK(char_length(btrim(name_en)) BETWEEN 1 AND 40),
 ADD COLUMN min_gold_total bigint CHECK(min_gold_total BETWEEN 0 AND 1000000000000),
 ADD COLUMN exchange_bonus_percent integer CHECK(exchange_bonus_percent BETWEEN 0 AND 100),
 ADD CONSTRAINT period_tier_fields CHECK((min_gold_total IS NULL AND exchange_bonus_percent IS NULL AND name_vi IS NULL AND name_en IS NULL) OR (min_gold_total IS NOT NULL AND exchange_bonus_percent IS NOT NULL AND name_vi IS NOT NULL AND name_en IS NOT NULL)),
 ADD CONSTRAINT period_tier_threshold UNIQUE(policy_id,min_gold_total);
ALTER TABLE affiliate_links DROP CONSTRAINT affiliate_links_tier_code_check;
ALTER TABLE affiliate_links ADD CONSTRAINT affiliate_links_tier_code_check CHECK(tier_code IN ('bronze','platinum','diamond','member','silver','gold'));
ALTER TABLE orders DROP CONSTRAINT orders_tier_code_check;
ALTER TABLE orders ADD CONSTRAINT orders_tier_code_check CHECK(tier_code IN ('bronze','platinum','diamond','member','silver','gold'));
-- Applied while writers are paused; the migration runner uses transactions.
CREATE INDEX orders_membership_approved ON orders(user_id,approved_at) INCLUDE(cashback) WHERE status='approved';
CREATE INDEX orders_membership_ordered ON orders(user_id,ordered_at) INCLUDE(cashback,approved_at) WHERE status='approved';
