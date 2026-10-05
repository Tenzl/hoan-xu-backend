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

Successful session checks also save the current Shopee cookies encrypted beside the profile, including cookies obtained by manual login in remote Chrome. A new browser reads them once at startup after a restart; checks reuse the current browser session instead of reloading cookies each time.

`CHROME_HEADLESS=false` opens Chromium for manual verification on a desktop. `true` runs it hidden. Shopee may still require manual access verification; successful desktop checks do not guarantee cloud/headless operation. No CAPTCHA or two-factor verification is automated.

Product units were compared with ten Affiliate pages; sanitized evidence is in `tests/fixtures/affiliate/`. Set `SHOPEE_SCHEMA_VERIFIED=true` and `SHOPEE_PRICE_SCALE=100000` to enable the verified product parser. Affiliate tracking must be independently confirmed before enabling `SHOPEE_TRACKING_VERIFIED`.

## Validation and contract

```sh
go run ./tests/run.go
go run ./tests/run.go vet
go build ./cmd/api ./cmd/admin
```

Database integration tests require `TEST_DATABASE_URL` pointing to a database whose name ends in `_test`. Chromium integration tests require `BROWSER_TEST_PATH`.

`tests/run.ps1` loads `.env` and runs Go tests/vet. All test sources and fixtures live in `tests/`, grouped into unit and API integration tests. The Go runner preserves their original package context using an overlay; plain `go test ./...` does not discover the relocated tests. See `tests/README.md` for filtered runs and fixture paths. Use `scripts/backup.ps1` and `scripts/restore.ps1` for PostgreSQL backups/restores. Operational documentation is in `docs/`; `docs/history/` retains the original combined-workspace instructions as historical references.

The frontend contract is `contracts/openapi.yaml`; `node scripts/refine-contract.mjs` updates it using the existing contract rules. After updating it, copy it into the frontend repository's `contracts/` folder and run `npm run generate` there. API translations are `internal/api/en.json`; synchronize them from the frontend's reviewed translation catalog using an explicit `BACKEND_REPO_DIR`.

## Deployment status

The current development and production environment files share a Supabase database through its session pooler. Application tables are in the private `hoanxu` schema. See `docs/supabase.md` for the required search path, encryption key and separate test database configuration.

`Dockerfile` and `render.yaml` package this API with headed Chromium, Xvfb and a private noVNC display in one Render Web Service. Production `env.prod` contains the Docker browser paths and remote display origin; import its values into Render Environment. Use a persistent disk at `/var/data`, one instance, and `/readyz` for the health check. An administrator can open the display from **Cài đặt cookie** after confirming their password. See [Render setup and browser verification](docs/render-browser.md) for the deployment steps and limitations. Local development still defaults to `127.0.0.1:8080` and does not enable the remote display.

## Unified Xu wallet

The wallet now combines approved cashback and check-in rewards (1 Xu = 1 VND). See [migration, API and local checklist](docs/unified-wallet.md). When `.env` targets another environment, use `.env.local` and `./scripts/dev-local.ps1`; `ENV_FILE` selects an explicit profile for Go commands and PowerShell helpers. Stop the API and back up the chosen database before migration 9.
