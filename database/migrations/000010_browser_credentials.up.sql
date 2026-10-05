CREATE TABLE browser_credentials (
 provider text PRIMARY KEY CHECK(provider='shopee'),
 cookie_cipher text NOT NULL CHECK(octet_length(cookie_cipher) BETWEEN 1 AND 131072),
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- Only the backend's schema owner may access encrypted browser credentials.
ALTER TABLE browser_credentials ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON browser_credentials FROM PUBLIC;
DO $$
DECLARE api_role text;
BEGIN
 FOR api_role IN SELECT rolname FROM pg_roles WHERE rolname IN ('anon','authenticated','service_role') LOOP
  EXECUTE format('REVOKE ALL ON browser_credentials FROM %I', api_role);
 END LOOP;
END $$;
