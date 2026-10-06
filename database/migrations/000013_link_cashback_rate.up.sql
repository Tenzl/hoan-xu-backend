ALTER TABLE cashback_policies ADD COLUMN tax_bps integer NOT NULL DEFAULT 500
 CHECK (tax_bps BETWEEN 0 AND 10000);
-- Links remain stateless. Their signed token pins this archived policy version.
-- No existing order, share rate or wallet balance is recalculated.
