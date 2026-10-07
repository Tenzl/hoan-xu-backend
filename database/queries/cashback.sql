-- name: CurrentCashbackPolicy :one
SELECT id::text,created_at,tax_bps,period_months,anchor_date,date_basis FROM cashback_policies WHERE mode='tiered' ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: CashbackTiers :many
SELECT tier_code,coalesce(name_vi,'')::text AS name_vi,coalesce(name_en,'')::text AS name_en,coalesce(min_gold_total,0)::bigint AS min_gold_total,coalesce(exchange_bonus_percent,0)::integer AS exchange_bonus_percent,min_share_bps,max_share_bps FROM cashback_tiers WHERE policy_id=$1 ORDER BY min_gold_total NULLS FIRST,min_approved_orders;

-- name: ApprovedOrderCount :one
SELECT count(*) FROM orders WHERE user_id=$1 AND status='approved';

-- name: MembershipTime :one
SELECT transaction_timestamp()::timestamptz;

-- name: PeriodCashbackTotals :one
WITH eligible AS (
 SELECT cashback,CASE WHEN sqlc.arg(date_basis)::text='ordered' THEN ordered_at ELSE approved_at END AS at
 FROM orders o WHERE o.user_id=sqlc.arg(user_id) AND status='approved' AND approved_at<=sqlc.arg(as_of)::timestamptz
)
SELECT coalesce(sum(cashback) FILTER(WHERE at>=sqlc.arg(previous_start)::timestamptz AND at<sqlc.arg(current_start)::timestamptz),0)::bigint AS previous_total,
 coalesce(sum(cashback) FILTER(WHERE at>=sqlc.arg(current_start)::timestamptz AND at<sqlc.arg(current_end)::timestamptz AND at<=sqlc.arg(as_of)::timestamptz),0)::bigint AS current_total,
 (SELECT count(*) FROM orders o WHERE o.user_id=sqlc.arg(user_id) AND status='approved' AND approved_at<=sqlc.arg(as_of)::timestamptz)::bigint AS approved_orders
FROM eligible WHERE at>=sqlc.arg(previous_start)::timestamptz AND at<sqlc.arg(current_end)::timestamptz;
