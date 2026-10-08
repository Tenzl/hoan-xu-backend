ALTER TABLE gift_catalog ADD COLUMN image_position_y integer NOT NULL DEFAULT 50
  CHECK (image_position_y BETWEEN 0 AND 100);
