-- Run with the API stopped. All conversions and marker commit atomically.
ALTER TABLE wallet_accounts DROP CONSTRAINT IF EXISTS wallet_accounts_kind_check;
ALTER TABLE wallet_accounts ADD CONSTRAINT wallet_accounts_kind_check CHECK(kind IN ('available','held','gift_held','debt','system'));
CREATE TABLE IF NOT EXISTS wallet_unification (id boolean PRIMARY KEY DEFAULT true CHECK(id), converted_at timestamptz NOT NULL DEFAULT now());
ALTER TABLE checkins ADD COLUMN IF NOT EXISTS award_xu bigint;
ALTER TABLE gift_redemptions ADD COLUMN IF NOT EXISTS cost_xu bigint CHECK(cost_xu>0);
ALTER TABLE gift_redemptions ADD COLUMN IF NOT EXISTS cost_unit text NOT NULL DEFAULT 'legacy_coin' CHECK(cost_unit IN ('legacy_coin','xu'));
DO $$
DECLARE c record; g record; tid uuid; sys uuid; avail uuid; debt_id uuid; held_id uuid; amount bigint; repay bigint;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtext('hoanxu-unified-wallet'));
 IF EXISTS(SELECT 1 FROM wallet_unification) THEN RETURN; END IF;
 SELECT id INTO STRICT sys FROM wallet_accounts WHERE kind='system' FOR UPDATE;
 INSERT INTO wallet_accounts(user_id,kind) SELECT user_id,'gift_held' FROM coin_accounts ON CONFLICT DO NOTHING;
 FOR c IN SELECT user_id,balance FROM coin_accounts ORDER BY user_id FOR UPDATE LOOP
  amount:=c.balance*300;
  IF amount>0 THEN
   SELECT id INTO STRICT avail FROM wallet_accounts WHERE user_id=c.user_id AND kind='available' FOR UPDATE;
   SELECT id,least(balance,amount) INTO STRICT debt_id,repay FROM wallet_accounts WHERE user_id=c.user_id AND kind='debt' FOR UPDATE;
   INSERT INTO wallet_transactions(reference,description) VALUES('wallet_unification:'||c.user_id,'Chuyển Xu điểm danh vào ví (×300)') RETURNING id INTO tid;
   IF amount<>2*repay THEN INSERT INTO wallet_entries(transaction_id,account_id,amount) VALUES(tid,sys,-amount+2*repay); END IF;
   IF amount>repay THEN INSERT INTO wallet_entries(transaction_id,account_id,amount) VALUES(tid,avail,amount-repay); END IF;
   IF repay>0 THEN INSERT INTO wallet_entries(transaction_id,account_id,amount) VALUES(tid,debt_id,-repay); END IF;
   UPDATE wallet_accounts SET balance=balance-amount+2*repay WHERE id=sys;
   UPDATE wallet_accounts SET balance=balance+amount-repay WHERE id=avail;
   UPDATE wallet_accounts SET balance=balance-repay WHERE id=debt_id;
  END IF;
 END LOOP;
 FOR g IN SELECT id,user_id,cost FROM gift_redemptions WHERE status='pending' ORDER BY id FOR UPDATE LOOP
  amount:=g.cost*300;
  SELECT id INTO STRICT held_id FROM wallet_accounts WHERE user_id=g.user_id AND kind='gift_held' FOR UPDATE;
  INSERT INTO wallet_transactions(reference,description) VALUES('wallet_unification:gift:'||g.id,'Chuyển khoản giữ đổi quà vào ví') RETURNING id INTO tid;
  INSERT INTO wallet_entries(transaction_id,account_id,amount) VALUES(tid,sys,-amount),(tid,held_id,amount);
  UPDATE wallet_accounts SET balance=balance-amount WHERE id=sys;
  UPDATE wallet_accounts SET balance=balance+amount WHERE id=held_id;
 END LOOP;
 UPDATE gift_redemptions SET cost_xu=cost*300;
 UPDATE checkins SET award_xu=award::bigint*300;
 UPDATE coin_accounts SET balance=0;
 UPDATE gift_catalog SET cost=cost*300;
 UPDATE reward_policies SET settings='{"daily":300,"milestones":{"3":600,"7":1500,"14":3000,"30":9000},"unit":"xu"}' WHERE id='checkin';
 UPDATE app_settings SET settings=(settings-'coinExchangeEnabled')||'{"walletUnit":"xu","xuPerVnd":1}';
 -- Update only the original default answers; preserve administrator-authored FAQ.
 UPDATE app_settings SET settings=jsonb_set(settings,'{faq}',coalesce((SELECT jsonb_agg(CASE
 WHEN item->>'answer'='Xu dùng đổi voucher hoặc tiền khi quản trị bật quy đổi.' THEN jsonb_set(item,'{answer}',to_jsonb('1 Xu = 1đ. Hoàn tiền và thưởng điểm danh cùng vào ví, dùng rút tiền hoặc đổi voucher.'::text))
 WHEN item->>'answer'='Chỉ tiền từ hoa hồng đã đối soát/duyệt và tiền đổi xu được bật mới vào số dư khả dụng.' THEN jsonb_set(item,'{answer}',to_jsonb('Xu từ cashback đã duyệt và điểm danh vào ví khả dụng. Rút từ 50.000 Xu, theo bội số 1.000; khoản chờ duyệt và tạm giữ chưa thể rút.'::text))
 ELSE item END ORDER BY ord) FROM jsonb_array_elements(coalesce(settings->'faq','[]'::jsonb)) WITH ORDINALITY t(item,ord)),'[]'::jsonb));
 INSERT INTO wallet_unification DEFAULT VALUES;
 INSERT INTO audit_logs(action,resource,payload) VALUES('wallet_unified','wallet','{"legacyCoinMultiplier":300,"xuPerVnd":1}');
END $$;
ALTER TABLE coin_accounts DROP CONSTRAINT IF EXISTS coin_accounts_retired_balance;
ALTER TABLE coin_accounts ADD CONSTRAINT coin_accounts_retired_balance CHECK(balance=0);
CREATE OR REPLACE FUNCTION reject_legacy_coin_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'Legacy coin ledger retired; use unified wallet'; END $$;
DROP TRIGGER IF EXISTS retired_coin_ledger ON coin_transactions;
CREATE TRIGGER retired_coin_ledger BEFORE INSERT ON coin_transactions FOR EACH ROW EXECUTE FUNCTION reject_legacy_coin_write();
ALTER TABLE gift_redemptions ALTER COLUMN cost_unit SET DEFAULT 'xu';
