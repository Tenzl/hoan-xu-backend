-- name: GetUser :one
SELECT id::text,name,email,role,blocked,tracking_code,created_at FROM users WHERE id=$1;

-- name: ListChannels :many
SELECT id,name,status FROM affiliate_channels ORDER BY id;

-- name: GetWallet :many
SELECT kind,balance FROM wallet_accounts WHERE user_id=$1;
