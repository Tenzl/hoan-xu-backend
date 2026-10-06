-- Rollback preserves financial rows but discards new attribution metadata. Back up first.
-- Ignored preview rows become unmatched; no report row is deleted.
ALTER TABLE import_rows DROP CONSTRAINT import_rows_status_check;
UPDATE import_rows SET status='unmatched' WHERE status='ignored';
ALTER TABLE import_rows ADD CONSTRAINT import_rows_status_check CHECK(status IN ('valid','invalid','unmatched','applied','duplicate','adjustment'));
ALTER TABLE orders DROP CONSTRAINT signed_link_metadata;
ALTER TABLE orders DROP COLUMN cashback_mode, DROP COLUMN link_expires_at, DROP COLUMN link_created_at, DROP COLUMN tracking_sub_ids, DROP COLUMN tracking_code;
ALTER TABLE cashback_policies DROP CONSTRAINT tracking_version_range;
ALTER TABLE cashback_policies DROP COLUMN tracking_version;
