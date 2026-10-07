-- Pause financial writers while applying the schema and backfill migrations.
WITH earned AS (
 SELECT user_id,sum(cashback)::bigint AS amount FROM orders WHERE status='approved' GROUP BY user_id
), paid AS (
 SELECT user_id,sum(amount)::bigint AS amount FROM withdrawals WHERE status='paid' GROUP BY user_id
), exchanged AS (
 SELECT a.user_id,sum(-e.amount)::bigint AS amount FROM wallet_entries e
 JOIN wallet_accounts a ON a.id=e.account_id JOIN wallet_transactions t ON t.id=e.transaction_id
 WHERE a.kind='available' AND e.amount<0 AND left(t.reference,12)='xu_exchange:' GROUP BY a.user_id
)
INSERT INTO wallet_user_totals(user_id,gold_total,gold_used)
SELECT u.id,coalesce(earned.amount,0),coalesce(paid.amount,0)+coalesce(exchanged.amount,0)
FROM users u LEFT JOIN earned ON earned.user_id=u.id LEFT JOIN paid ON paid.user_id=u.id LEFT JOIN exchanged ON exchanged.user_id=u.id
ON CONFLICT(user_id) DO UPDATE SET gold_total=excluded.gold_total,gold_used=excluded.gold_used;
