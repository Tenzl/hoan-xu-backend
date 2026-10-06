-- Restores the column only; previous bookmark flags cannot be recovered.
ALTER TABLE affiliate_links ADD COLUMN saved boolean NOT NULL DEFAULT false;
