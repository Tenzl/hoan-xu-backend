ALTER TABLE orders ADD COLUMN source_status text NOT NULL DEFAULT 'pending' CHECK(source_status IN ('pending','approved','rejected'));
