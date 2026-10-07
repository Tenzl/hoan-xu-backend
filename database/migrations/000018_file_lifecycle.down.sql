DROP INDEX private_file_cleanup;
DROP INDEX import_batch_file;
ALTER TABLE import_batches DROP COLUMN file_id;
ALTER TABLE private_files DROP COLUMN lifecycle;
