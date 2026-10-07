ALTER TABLE gift_catalog ALTER COLUMN channel DROP NOT NULL;
ALTER TABLE gift_catalog ADD COLUMN image_url text NOT NULL DEFAULT '' CHECK (char_length(image_url)<=2048);
ALTER TABLE gift_catalog ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description)<=2000);
