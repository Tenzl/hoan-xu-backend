-- Stop wallet writers before applying. Existing balances and ledger remain gold.
ALTER TABLE wallet_accounts DROP CONSTRAINT wallet_accounts_kind_check;
ALTER TABLE wallet_accounts DROP CONSTRAINT wallet_accounts_check;
ALTER TABLE wallet_accounts ADD CONSTRAINT wallet_accounts_kind_check CHECK(kind IN ('available','held','gift_held','debt','system','green_available','green_gift_held','green_system'));
ALTER TABLE wallet_accounts ADD CONSTRAINT wallet_accounts_check CHECK(kind IN ('system','green_system') OR balance>=0);
CREATE UNIQUE INDEX one_green_system_account ON wallet_accounts(kind) WHERE kind='green_system';
ALTER TABLE gift_redemptions ADD COLUMN currency text NOT NULL DEFAULT 'gold' CHECK(currency IN ('gold','green'));
ALTER TABLE gift_redemptions ALTER COLUMN currency SET DEFAULT 'green';
CREATE TABLE xu_exchange_policies (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 gold_units bigint NOT NULL CHECK(gold_units BETWEEN 1 AND 1000000),
 green_units bigint NOT NULL CHECK(green_units BETWEEN 1 AND 1000000),
 actor_id uuid REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER immutable_exchange_policies BEFORE UPDATE OR DELETE ON xu_exchange_policies FOR EACH ROW EXECUTE FUNCTION prevent_ledger_mutation();
ALTER TABLE xu_exchange_policies ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON xu_exchange_policies FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
 FOR role_name IN SELECT rolname FROM pg_roles WHERE rolname IN ('anon','authenticated','service_role') LOOP
  EXECUTE format('REVOKE ALL ON xu_exchange_policies FROM %I',role_name);
 END LOOP;
END $$;
CREATE OR REPLACE FUNCTION check_balanced_transaction() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM wallet_entries e JOIN wallet_accounts a ON a.id=e.account_id WHERE e.transaction_id=NEW.transaction_id GROUP BY CASE WHEN a.kind LIKE 'green_%' THEN 'green' ELSE 'gold' END HAVING sum(e.amount)<>0) THEN
  RAISE EXCEPTION 'Unbalanced currency ledger';
 END IF;
 RETURN NEW;
END $$;
