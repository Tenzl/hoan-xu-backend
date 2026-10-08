ALTER TABLE orders ADD COLUMN source_report jsonb NOT NULL DEFAULT '{}';
ALTER TABLE import_batches ADD COLUMN committed_by uuid REFERENCES users(id);
