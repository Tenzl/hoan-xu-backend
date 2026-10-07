DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM affiliate_links l JOIN cashback_tiers t ON t.policy_id=l.policy_id WHERE t.min_gold_total IS NOT NULL)
 OR EXISTS(SELECT 1 FROM orders o JOIN cashback_tiers t ON t.policy_id=o.policy_id WHERE t.min_gold_total IS NOT NULL)
 OR EXISTS(SELECT 1 FROM idempotency_records WHERE operation IN ('xu-exchange','cashback-policy')) THEN
  RAISE EXCEPTION 'Period policy may have recorded financial snapshots; use a forward migration';
 END IF;
END $$;
DELETE FROM cashback_tiers WHERE min_gold_total IS NOT NULL;
DELETE FROM cashback_policies p WHERE p.mode='tiered' AND NOT EXISTS(SELECT 1 FROM cashback_tiers t WHERE t.policy_id=p.id);
