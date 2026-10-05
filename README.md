# hoan-xu-backend

Go/PostgreSQL API for Hoàn Xu. The frontend is at https://github.com/Tenzl/hoan-xu-frontend.

## Run locally

Use the Go version declared in `go.mod`, PostgreSQL, and Chromium for Shopee product checks.

1. Copy `.env.example` to `.env` and configure `DATABASE_URL`, `MIGRATION_DATABASE_URL` and `APP_ORIGIN`.
2. Run `go run ./cmd/admin key`, save the generated value as `DATA_ENCRYPTION_KEY`, and keep this key stable across deployments. Never commit it.
3. Run `go run ./cmd/admin migrate` from this repository root. Migrations and SQL sources are included in `database/`.
4. Run `go run ./cmd/api`. The local API listens on `127.0.0.1:8080`; set the frontend's `BACKEND_URL` accordingly.

On Windows, run `scripts/setup-local.ps1`, `scripts/migrate.ps1` and `scripts/dev.ps1` from this repository. The dev script starts only the backend; run the frontend separately. Portable Go and compiled binaries stay in `.tools/`; cookies, browser profiles, backups, files and logs stay in `private-data/`. Both folders are ignored by Git. Default paths are `CHROME_PROFILE=private-data/chrome-profile` and `PRIVATE_DIR=private-data/files`.

To create the first admin, set `ADMIN_PASSWORD` in your shell environment and run `go run ./cmd/admin create --username admin --name "Quản trị" --role admin`. Remove the environment variable afterwards. Change the temporary password at first login.

## Shopee

Configure `CHROME_PATH` and a private persistent `CHROME_PROFILE`. Pasted cookies are encrypted beside the profile, outside the source tree. Use **Cài đặt cookie** in the admin panel to update or check the session.

`CHROME_HEADLESS=false` opens Chromium for manual verification on a desktop. `true` runs it hidden. Shopee may still require manual access verification; successful desktop checks do not guarantee cloud/headless operation. No CAPTCHA or two-factor verification is automated.

Product units were compared with ten Affiliate pages; sanitized evidence is in `internal/affiliate/testdata/`. Set `SHOPEE_SCHEMA_VERIFIED=true` and `SHOPEE_PRICE_SCALE=100000` to enable the verified product parser. Affiliate tracking must be independently confirmed before enabling `SHOPEE_TRACKING_VERIFIED`.

## Validation and contract

```sh
go test ./...
go vet ./...
go build ./cmd/api ./cmd/admin
```

Database integration tests require `TEST_DATABASE_URL` pointing to a database whose name ends in `_test`. Chromium integration tests require `BROWSER_TEST_PATH`.

`scripts/test.ps1` loads `.env` and runs Go tests/vet. Use `scripts/backup.ps1` and `scripts/restore.ps1` for PostgreSQL backups/restores. Operational documentation is in `docs/`; `docs/history/` retains the original combined-workspace instructions as historical references.

The frontend contract is `contracts/openapi.yaml`; `node scripts/refine-contract.mjs` updates it using the existing contract rules. After updating it, copy it into the frontend repository's `contracts/` folder and run `npm run generate` there. API translations are `internal/api/en.json`; synchronize them from the frontend's reviewed translation catalog using an explicit `BACKEND_REPO_DIR`.

## Deployment status

This repository contains the application source and migrations. Render Docker/Xvfb configuration is not yet implemented. Cloud deployment also needs public port binding, HTTPS/cookie settings, a persistent location for private files and the Chromium profile, and an authenticated way to complete browser verification.
