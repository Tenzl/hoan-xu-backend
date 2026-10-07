-- Legacy files are exempt from automatic retention until explicitly classified.
ALTER TABLE private_files ADD COLUMN lifecycle text NOT NULL DEFAULT 'legacy'
 CHECK(lifecycle IN ('legacy','staged','attached','deleting'));
ALTER TABLE import_batches ADD COLUMN file_id uuid REFERENCES private_files(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX import_batch_file ON import_batches(file_id) WHERE file_id IS NOT NULL;
CREATE INDEX private_file_cleanup ON private_files(created_at,id) WHERE purpose='csv' AND lifecycle<>'legacy';
