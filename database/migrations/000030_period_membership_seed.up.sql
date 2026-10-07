WITH previous AS (
 SELECT id,share_percent,tax_bps FROM cashback_policies WHERE mode='tiered' ORDER BY created_at DESC,id DESC LIMIT 1
), new_policy AS (
 INSERT INTO cashback_policies(share_percent,mode,tax_bps,created_at)
 SELECT share_percent,'tiered',tax_bps,clock_timestamp() FROM previous RETURNING id
)
INSERT INTO cashback_tiers(policy_id,tier_code,name_vi,name_en,min_gold_total,exchange_bonus_percent,min_share_bps,max_share_bps)
SELECT n.id,t.code,t.vi,t.en,t.threshold,t.bonus,old.min_share_bps,old.max_share_bps
FROM previous p CROSS JOIN new_policy n CROSS JOIN (VALUES
 ('member','Thân thiết','Member',0::bigint,3,'bronze'),
 ('silver','Bạc','Silver',500000::bigint,6,'bronze'),
 ('gold','Vàng','Gold',1500000::bigint,10,'platinum'),
 ('diamond','Kim cương','Diamond',3000000::bigint,15,'diamond')
) t(code,vi,en,threshold,bonus,old_code)
JOIN cashback_tiers old ON old.policy_id=p.id AND old.tier_code=t.old_code;
