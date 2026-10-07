CREATE TABLE ledger_reconciliation_queue (
 account_id uuid PRIMARY KEY REFERENCES wallet_accounts(id) ON DELETE CASCADE,
 revision bigint NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION queue_ledger_reconciliation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO ledger_reconciliation_queue(account_id) VALUES(NEW.id)
 ON CONFLICT(account_id) DO UPDATE SET revision=ledger_reconciliation_queue.revision+1,updated_at=now();
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_reconciliation AFTER INSERT OR UPDATE OF balance ON wallet_accounts
 FOR EACH ROW EXECUTE FUNCTION queue_ledger_reconciliation();
INSERT INTO ledger_reconciliation_queue(account_id) SELECT id FROM wallet_accounts;
