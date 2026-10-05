-- Financial conversion cannot be reversed after spending. Restore a pre-cutover backup into a new database.
DO $$ BEGIN RAISE EXCEPTION 'Unified wallet is irreversible: restore a pre-cutover backup to a new database'; END $$;
