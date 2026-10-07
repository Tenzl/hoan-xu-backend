ALTER TABLE gift_catalog ADD COLUMN icon text NOT NULL DEFAULT 'gift'
 CHECK (icon IN ('gift','ticket','shopping-bag','box','coffee','headphones','star','heart'));
