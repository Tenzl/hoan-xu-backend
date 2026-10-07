DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wallet_entries e JOIN wallet_accounts a ON a.id=e.account_id WHERE a.kind LIKE 'green_%') OR EXISTS(SELECT 1 FROM gift_redemptions WHERE currency='green') OR EXISTS(SELECT 1 FROM wallet_accounts WHERE kind LIKE 'green_%' AND balance<>0) OR EXISTS(SELECT 1 FROM xu_exchange_policies WHERE actor_id IS NOT NULL) THEN
  RAISE EXCEPTION 'Cannot roll back: green Xu or exchange policy history exists';
 END IF;
END $$;
DELETE FROM wallet_accounts WHERE kind LIKE 'green_%';
-- Only the untouched bootstrap policy can be removed.
DROP TRIGGER immutable_exchange_policies ON xu_exchange_policies;
DELETE FROM xu_exchange_policies;
CREATE TRIGGER immutable_exchange_policies BEFORE UPDATE OR DELETE ON xu_exchange_policies FOR EACH ROW EXECUTE FUNCTION prevent_ledger_mutation();
