-- New operational tables follow the backend-owner access model.
ALTER TABLE tracking_publishers ENABLE ROW LEVEL SECURITY;
ALTER TABLE link_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_reconciliation_queue ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON tracking_publishers,link_operations,ledger_reconciliation_queue FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
 FOR role_name IN SELECT rolname FROM pg_roles WHERE rolname IN ('anon','authenticated','service_role') LOOP
  EXECUTE format('REVOKE ALL ON tracking_publishers,link_operations,ledger_reconciliation_queue FROM %I',role_name);
 END LOOP;
END $$;
