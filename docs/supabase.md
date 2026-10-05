# Supabase database configuration

Development `.env` and production `env.prod` use the same Supabase database through the shared session pooler on port 5432. The connection and encryption key are private and ignored by Git. Local development retains its local origin, port and cookie settings; production uses the Vercel origin and secure cookies.

The application tables live in the private `hoanxu` schema. Both `DATABASE_URL` and `MIGRATION_DATABASE_URL` include `sslmode=require&search_path=hoanxu%2Cextensions%2Cpublic`. Preserve this search path when updating either URL. The `extensions` schema provides Supabase's installed pgcrypto functions.

All nine existing application migrations were applied to this fresh database. Application tables have RLS enabled; the database owner used by the backend can access them, while Supabase's public API roles have no access to the schema, tables, sequences or functions. Do not add `hoanxu` to the Data API's exposed schemas. The frontend accesses data through the Go API.

The initial database contains application configuration and policy defaults, but no customer or admin accounts. Existing local application data was not imported. Use the existing admin creation workflow documented in the repository README when provisioning an admin.

`TEST_DATABASE_URL` still points to the separate local database ending in `_test`. Do not point automated integration tests at the shared development/production database.

The binaries load `.env` by default, or the path selected with `ENV_FILE`. Cloud services can instead supply the environment variables directly. Keep `DATA_ENCRYPTION_KEY` identical between environments because encrypted fields are stored in the shared database. Backend private cookies and browser profiles also depend on this key.

Connection guidance: [Supabase PostgreSQL connections](https://supabase.com/docs/guides/database/connecting-to-postgres). Schema exposure: [Supabase custom schemas](https://supabase.com/docs/guides/api/using-custom-schemas).
