DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM weekly_prize_campaigns) THEN
  RAISE EXCEPTION 'Cannot remove weekly prize history or reserved inventory';
 END IF;
END $$;
DROP TABLE weekly_prize_awards;
DROP FUNCTION protect_weekly_prize_award();
DROP TABLE weekly_prize_campaigns;
DROP FUNCTION protect_settled_prize_campaign();
