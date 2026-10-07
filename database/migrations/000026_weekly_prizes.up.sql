CREATE TABLE weekly_prize_campaigns (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 week_start timestamptz NOT NULL UNIQUE,
 week_end timestamptz NOT NULL,
 gift_id text NOT NULL REFERENCES gift_catalog(id),
 gift_snapshot jsonb NOT NULL,
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 description text NOT NULL CHECK(char_length(description)<=500),
 status text NOT NULL CHECK(status IN ('draft','active','settled')),
 reserved_count integer NOT NULL CHECK(reserved_count BETWEEN 0 AND 5),
 version integer NOT NULL DEFAULT 1 CHECK(version>0),
 created_by uuid NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 settled_at timestamptz,
 CHECK(week_end=week_start+interval '7 days'),
 CHECK((status='active' AND reserved_count=5) OR (status<>'active' AND reserved_count=0))
);
CREATE TABLE weekly_prize_awards (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 campaign_id uuid NOT NULL REFERENCES weekly_prize_campaigns(id),
 user_id uuid NOT NULL REFERENCES users(id),
 user_name text NOT NULL,
 rank integer NOT NULL CHECK(rank BETWEEN 1 AND 5),
 xu bigint NOT NULL CHECK(xu>0),
 orders bigint NOT NULL CHECK(orders>0),
 gift_snapshot jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered')),
 delivery_cipher text,
 delivered_by uuid REFERENCES users(id),
 delivered_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(campaign_id,rank), UNIQUE(campaign_id,user_id),
 CHECK((status='pending' AND delivery_cipher IS NULL AND delivered_at IS NULL) OR (status='delivered' AND delivery_cipher IS NOT NULL AND delivered_at IS NOT NULL))
);
CREATE INDEX weekly_prize_awards_user ON weekly_prize_awards(user_id,created_at DESC);
ALTER TABLE weekly_prize_campaigns ENABLE ROW LEVEL SECURITY;
ALTER TABLE weekly_prize_awards ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON weekly_prize_campaigns,weekly_prize_awards FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
 FOR role_name IN SELECT rolname FROM pg_roles WHERE rolname IN ('anon','authenticated','service_role') LOOP
  EXECUTE format('REVOKE ALL ON weekly_prize_campaigns,weekly_prize_awards FROM %I',role_name);
 END LOOP;
END $$;
CREATE FUNCTION protect_settled_prize_campaign() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status='settled' THEN RAISE EXCEPTION 'Settled campaign is immutable'; END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Prize campaigns cannot be deleted'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER weekly_prize_campaign_immutable BEFORE UPDATE OR DELETE ON weekly_prize_campaigns FOR EACH ROW EXECUTE FUNCTION protect_settled_prize_campaign();
-- Winners and catalog snapshots are immutable, including after delivery.
CREATE FUNCTION protect_weekly_prize_award() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Prize history is immutable'; END IF;
 IF (to_jsonb(NEW)-ARRAY['status','delivery_cipher','delivered_by','delivered_at']) IS DISTINCT FROM
    (to_jsonb(OLD)-ARRAY['status','delivery_cipher','delivered_by','delivered_at']) OR OLD.status='delivered' THEN
  RAISE EXCEPTION 'Prize history is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER weekly_prize_award_immutable BEFORE UPDATE OR DELETE ON weekly_prize_awards FOR EACH ROW EXECUTE FUNCTION protect_weekly_prize_award();
