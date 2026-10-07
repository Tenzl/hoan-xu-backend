-- Preserve historical publishers and explicit internal review independently of source status.
CREATE TABLE tracking_publishers (
 publisher text PRIMARY KEY CHECK(length(publisher) BETWEEN 1 AND 32),
 created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO tracking_publishers(publisher)
SELECT DISTINCT publisher FROM (
 SELECT settings->>'publisher' AS publisher FROM affiliate_channels WHERE id='shopee'
 UNION SELECT publisher FROM orders WHERE cashback_mode='signed_link'
 UNION SELECT payload->'configuration'->>'publisher' FROM audit_logs WHERE action='shopee_settings_updated'
 UNION SELECT payload->>'publisher' FROM audit_logs WHERE action='shopee_publisher_updated'
) history WHERE length(publisher) BETWEEN 1 AND 32 ON CONFLICT DO NOTHING;
ALTER TABLE orders ADD COLUMN internally_rejected boolean NOT NULL DEFAULT false;
UPDATE orders o SET internally_rejected=true WHERE o.status='rejected' AND EXISTS
 (SELECT 1 FROM order_events e WHERE e.order_id=o.id AND e.action='rejected');
CREATE INDEX import_rows_valid ON import_rows(batch_id,row_number) WHERE status='valid';
