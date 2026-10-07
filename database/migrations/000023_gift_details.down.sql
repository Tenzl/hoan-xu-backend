-- Refuse to invent a channel for gifts created without one.
ALTER TABLE gift_catalog ALTER COLUMN channel SET NOT NULL;
ALTER TABLE gift_catalog DROP COLUMN image_url, DROP COLUMN description;
