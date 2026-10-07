INSERT INTO wallet_accounts(kind) VALUES('green_system');
INSERT INTO wallet_accounts(user_id,kind) SELECT u.id,k.kind FROM users u CROSS JOIN (VALUES('green_available'),('green_gift_held')) k(kind) ON CONFLICT DO NOTHING;
INSERT INTO xu_exchange_policies(gold_units,green_units) VALUES(1,1);
