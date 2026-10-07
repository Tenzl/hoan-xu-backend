-- Keep access private on rollback; do not restore public grants.
ALTER TABLE tracking_publishers DISABLE ROW LEVEL SECURITY;
ALTER TABLE link_operations DISABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_reconciliation_queue DISABLE ROW LEVEL SECURITY;
