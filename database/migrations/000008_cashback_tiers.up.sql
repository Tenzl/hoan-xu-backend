ALTER TABLE cashback_policies ADD COLUMN mode text NOT NULL DEFAULT 'fixed' CHECK (mode IN ('fixed','tiered'));
CREATE TABLE cashback_tiers (
 policy_id uuid NOT NULL REFERENCES cashback_policies(id),
 tier_code text NOT NULL CHECK (tier_code IN ('bronze','platinum','diamond')),
 min_approved_orders bigint NOT NULL CHECK (min_approved_orders>=0),
 min_share_bps integer NOT NULL CHECK (min_share_bps BETWEEN 0 AND 10000),
 max_share_bps integer NOT NULL CHECK (max_share_bps BETWEEN min_share_bps AND 10000),
 PRIMARY KEY(policy_id,tier_code), UNIQUE(policy_id,min_approved_orders)
);
ALTER TABLE affiliate_links ADD COLUMN tier_code text CHECK (tier_code IN ('bronze','platinum','diamond')),
 ADD COLUMN min_share_bps integer, ADD COLUMN max_share_bps integer;
UPDATE affiliate_links l SET min_share_bps=(p.share_percent*100)::integer,max_share_bps=(p.share_percent*100)::integer FROM cashback_policies p WHERE p.id=l.policy_id;
ALTER TABLE affiliate_links ALTER COLUMN min_share_bps SET NOT NULL, ALTER COLUMN max_share_bps SET NOT NULL,
 ADD CONSTRAINT link_share_range CHECK (min_share_bps BETWEEN 0 AND 10000 AND max_share_bps BETWEEN min_share_bps AND 10000);
ALTER TABLE orders ADD COLUMN tier_code text CHECK (tier_code IN ('bronze','platinum','diamond')), ADD COLUMN share_bps integer;
UPDATE orders o SET share_bps=(p.share_percent*100)::integer FROM cashback_policies p WHERE p.id=o.policy_id;
ALTER TABLE orders ALTER COLUMN share_bps SET NOT NULL, ADD CONSTRAINT order_share_range CHECK (share_bps BETWEEN 0 AND 10000);
-- Legacy insert compatibility is deliberately limited to fixed policies.
CREATE FUNCTION fill_fixed_link_share() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.min_share_bps IS NULL AND NEW.max_share_bps IS NULL THEN
  SELECT (share_percent*100)::integer,(share_percent*100)::integer INTO NEW.min_share_bps,NEW.max_share_bps FROM cashback_policies WHERE id=NEW.policy_id AND mode='fixed';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER fixed_link_share BEFORE INSERT ON affiliate_links FOR EACH ROW EXECUTE FUNCTION fill_fixed_link_share();
CREATE FUNCTION fill_fixed_order_share() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.share_bps IS NULL THEN
  SELECT (share_percent*100)::integer INTO NEW.share_bps FROM cashback_policies WHERE id=NEW.policy_id AND mode='fixed';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER fixed_order_share BEFORE INSERT ON orders FOR EACH ROW EXECUTE FUNCTION fill_fixed_order_share();
WITH new_policy AS (
 INSERT INTO cashback_policies(share_percent,mode)
 SELECT share_percent,'tiered' FROM cashback_policies ORDER BY created_at DESC,id DESC LIMIT 1 RETURNING id,share_percent
)
INSERT INTO cashback_tiers(policy_id,tier_code,min_approved_orders,min_share_bps,max_share_bps)
SELECT p.id,t.code,t.threshold,(p.share_percent*100)::integer,(p.share_percent*100)::integer
FROM new_policy p CROSS JOIN (VALUES ('bronze',0),('platinum',30),('diamond',100)) t(code,threshold);
UPDATE app_settings SET settings=settings-'sharePercent';
CREATE INDEX orders_approved_user ON orders(user_id) WHERE status='approved';
