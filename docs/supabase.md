# Supabase database configuration

Development `.env` and production `env.prod` use the same Supabase database through the shared session pooler on port 5432. The connection and encryption key are private and ignored by Git. Local development retains its local origin, port and cookie settings; production uses the Vercel origin and secure cookies.

The local runtime profile `.env.local`, selected by `scripts/dev-local.ps1`, uses the loopback database `hoanxu_local_20261007_02`. Its database URLs stay separate from the shared Supabase database; browser settings, localhost origin and cookie settings retain their local values. `TEST_DATABASE_URL` remains a separate local test database. Select `env.prod` explicitly when administering Supabase.

The application tables live in the private `hoanxu` schema. Both `DATABASE_URL` and `MIGRATION_DATABASE_URL` include `sslmode=require&search_path=hoanxu%2Cextensions%2Cpublic`. Preserve this search path when updating either URL. The `extensions` schema provides Supabase's installed pgcrypto functions.

Migration 10 remains in the applied migration history. Its private `browser_credentials` table is retained, but the API no longer reads or writes Shopee cookies there. Shopee uses manual sign-in in the managed Chrome profile. The Affiliate ID entered in admin is saved in `affiliate_channels.settings`. Apply all migrations with the Go migration runner before deploying against another database. Application tables have RLS enabled; the database owner used by the backend can access them, while Supabase's public API roles have no access to the schema, tables, sequences or functions. Do not add `hoanxu` to the Data API's exposed schemas. The frontend accesses data through the Go API.

The initial database contains application configuration and policy defaults, but no customer or admin accounts. Existing local application data was not imported. Use the existing admin creation workflow documented in the repository README when provisioning an admin.

`TEST_DATABASE_URL` still points to the separate local database ending in `_test`. Do not point automated integration tests at the shared development/production database.

The binaries load `.env` by default, or the path selected with `ENV_FILE`. Cloud services can instead supply the environment variables directly. Keep `DATA_ENCRYPTION_KEY` identical between environments because encrypted fields are stored in the shared database. The historical `browser_credentials` table retains RLS and revoked public API grants; it is never returned by the application API.

Connection guidance: [Supabase PostgreSQL connections](https://supabase.com/docs/guides/database/connecting-to-postgres). Schema exposure: [Supabase custom schemas](https://supabase.com/docs/guides/api/using-custom-schemas).

On 2026-10-08, migrations 22–31 were applied to Supabase in one transaction after taking a private custom-format schema backup. Application writes were locked during the backfills. Verification compared all existing records in 41 business tables, excluding only columns intentionally changed by these migrations, and confirmed that the original orders, wallet entries and gold balances were preserved. The migration state is version 31 with `dirty=false`; all 47 application tables have RLS enabled. The Go migration runner subsequently reported no pending migrations. Backup files and execution logs remain under the Git-ignored `private-data/backups/supabase-prepush-20261008/` directory.
