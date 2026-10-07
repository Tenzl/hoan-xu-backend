CREATE TABLE wallet_user_totals (
 user_id uuid PRIMARY KEY REFERENCES users(id),
 gold_total bigint NOT NULL DEFAULT 0 CHECK(gold_total>=0),
 gold_used bigint NOT NULL DEFAULT 0 CHECK(gold_used>=0)
);
COMMENT ON COLUMN wallet_user_totals.gold_total IS 'Current cashback sum of approved orders, including manual orders; excludes check-in rewards.';
COMMENT ON COLUMN wallet_user_totals.gold_used IS 'Gold spent in completed bank withdrawals and successful gold-to-green conversions; excludes green bonuses.';
ALTER TABLE wallet_user_totals ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON wallet_user_totals FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
 FOR role_name IN SELECT rolname FROM pg_roles WHERE rolname IN ('anon','authenticated','service_role') LOOP
  EXECUTE format('REVOKE ALL ON wallet_user_totals FROM %I',role_name);
 END LOOP;
END $$;
