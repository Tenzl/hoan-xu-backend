-- name: CurrentCashbackPolicy :one
SELECT id::text,created_at,tax_bps FROM cashback_policies WHERE mode='tiered' ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: CashbackTiers :many
SELECT tier_code,min_approved_orders,min_share_bps,max_share_bps FROM cashback_tiers WHERE policy_id=$1 ORDER BY min_approved_orders;

-- name: ApprovedOrderCount :one
SELECT count(*) FROM orders WHERE user_id=$1 AND status='approved';
