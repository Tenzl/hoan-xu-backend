ALTER TABLE orders ADD COLUMN approved_at timestamptz;

UPDATE orders o SET approved_at = coalesce(
  (SELECT min(e.created_at) FROM order_events e WHERE e.order_id=o.id AND e.action='approved'),
  (SELECT t.created_at FROM wallet_transactions t WHERE t.reference='order_credit:'||o.id::text)
) WHERE o.status='approved';

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM orders WHERE status='approved' AND approved_at IS NULL) THEN
    RAISE EXCEPTION 'Approved orders lack approval evidence. Reconcile order_events or order_credit transactions before migrating.';
  END IF;
END $$;

ALTER TABLE orders ADD CONSTRAINT approved_orders_have_time CHECK(status <> 'approved' OR approved_at IS NOT NULL);
CREATE INDEX orders_leaderboard_period ON orders(approved_at,user_id) INCLUDE(cashback) WHERE status='approved';
