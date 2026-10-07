CREATE TABLE shopee_verifications (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 origin_key text NOT NULL,
 configuration_version text NOT NULL,
 fingerprint text NOT NULL,
 product_url text NOT NULL,
 status text NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 stage text NOT NULL DEFAULT 'queued',
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '90 seconds',
 finished_at timestamptz,
 error_code text,
 error_message text
);
CREATE UNIQUE INDEX shopee_verifications_active ON shopee_verifications(origin_key) WHERE status IN ('queued','running');
CREATE INDEX shopee_verifications_recent ON shopee_verifications(origin_key,created_at DESC);
ALTER TABLE shopee_verifications ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON shopee_verifications FROM PUBLIC;
