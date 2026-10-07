DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wallet_entries e JOIN wallet_accounts a ON a.id=e.account_id WHERE a.kind LIKE 'green_%') OR EXISTS(SELECT 1 FROM gift_redemptions WHERE currency='green') OR EXISTS(SELECT 1 FROM wallet_accounts WHERE kind LIKE 'green_%' AND balance<>0) OR EXISTS(SELECT 1 FROM xu_exchange_policies WHERE actor_id IS NOT NULL) THEN
  RAISE EXCEPTION 'Cannot roll back: green Xu or exchange history exists';
 END IF;
END $$;
DELETE FROM wallet_accounts WHERE kind LIKE 'green_%';
DROP TABLE xu_exchange_policies;
ALTER TABLE gift_redemptions DROP COLUMN currency;
DROP INDEX one_green_system_account;
ALTER TABLE wallet_accounts DROP CONSTRAINT wallet_accounts_kind_check;
ALTER TABLE wallet_accounts DROP CONSTRAINT wallet_accounts_check;
ALTER TABLE wallet_accounts ADD CONSTRAINT wallet_accounts_kind_check CHECK(kind IN ('available','held','gift_held','debt','system'));
ALTER TABLE wallet_accounts ADD CONSTRAINT wallet_accounts_check CHECK(kind='system' OR balance>=0);
CREATE OR REPLACE FUNCTION check_balanced_transaction() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT coalesce(sum(amount),0) FROM wallet_entries WHERE transaction_id=NEW.transaction_id)<>0 THEN RAISE EXCEPTION 'Unbalanced ledger'; END IF; RETURN NEW; END $$;
