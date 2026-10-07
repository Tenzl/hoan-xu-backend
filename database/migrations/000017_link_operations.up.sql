-- Reservation contains only request identity; financial snapshot is stored on success.
CREATE TABLE link_operations (
 user_id uuid NOT NULL REFERENCES users(id),
 key text NOT NULL CHECK(length(key) BETWEEN 8 AND 128),
 payload_hash text NOT NULL,
 status text NOT NULL CHECK(status IN ('running','succeeded','failed','indeterminate')),
 response jsonb,
 error_status integer,
 error_code text,
 error_message text,
 link_id uuid REFERENCES affiliate_links(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,key)
);
