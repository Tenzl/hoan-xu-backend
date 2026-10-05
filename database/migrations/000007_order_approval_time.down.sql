DROP INDEX orders_leaderboard_period;
ALTER TABLE orders DROP CONSTRAINT approved_orders_have_time;
ALTER TABLE orders DROP COLUMN approved_at;
