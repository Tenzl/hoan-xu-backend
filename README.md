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

## Import historical customers

`go run ./cmd/admin import-legacy --file <export.md>` previews the five-column
Markdown export (`STT`, `Tên hiển thị`, `Mua lần đầu`, `Mua gần nhất`, `Tổng đơn`)
without opening a database connection. Dates use `dd/MM/yyyy` in Vietnam time.
After backing up the database with `scripts/backup.ps1`, append `--apply` to import
into the environment selected by `.env` or `ENV_FILE`.

The batch `legacy-server-2026-10` creates separate customers with empty emails and
no login identities, preserving each customer's count and first/last purchase day.
Other timestamps and cashback amounts (5,000–40,000 VND per order) are generated
deterministically from the normalized export. All orders are approved at their
historical purchase time, credited to the unified wallet, and counted in historical
leaderboards. Both link URLs are `link`; missing product/value fields use the
documented placeholder values. A separate fixed 100% policy makes commission
equal to the generated cashback without changing the active tiered policy.

The import uses the same immutable, balanced ledger as normal order credits, with
one `order_credit:<id>` transaction per order. Bulk balance updates apply only to
freshly created wallets with zero debt. Customers, links, orders, events, credits
and an audit completion marker commit together. Concurrent retries serialize;
reapplying the same batch is a no-op, and changed source content is rejected.
Keep the source export and backup in ignored `private-data/`, outside Git.

## Shopee

Local development uses `BROWSER_MODE=local`, `CHROME_PATH` and a private `CHROME_PROFILE`; Chrome starts when an administrator opens it. Production uses `BROWSER_MODE=remote` and `CHROME_REMOTE_URL=http://127.0.0.1:9222` over an SSH tunnel to Chromium on EC2. Remote Chrome runs continuously and Go probes its session on startup/reconnect. Open **Đăng nhập Shopee → Mở Chrome trên server**, sign in, then choose **Tôi đã đăng nhập — Kiểm tra phiên**. The API does not import, export or restore Shopee cookies from files or the database.

Both modes preload and reuse two worker tabs after session verification, keeping the administrator's login tab separate. Local mode runs Chrome on the developer's computer; it does not connect to EC2. Failed or timed-out worker tabs are closed and replaced when needed.

Enter the Shopee **Affiliate ID (Shopee Publisher)** on the same page and save it. The ID is stored in `affiliate_channels.settings` and used when creating links; no `SHOPEE_PUBLISHER` environment variable is required. Saving an ID or signing in does not automatically enable verified tracking. Production Chrome keeps its profile on EC2 EBS, independently of Render restarts; Shopee can still request login/verification. Historical migration 10 and its private table remain, but the API no longer uses their cookie data.

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

Cloudflare keepalive workers can call public `GET /ping` (for example,
`https://hoan-xu-backend.onrender.com/ping`). It returns HTTP 200 with
`data.status`, `data.service` and `data.time` in the standard JSON envelope,
with `Cache-Control: no-store`. No authentication or database/browser request
is needed. Use `/readyz` when database readiness must also be checked.

The current development and production environment files share a Supabase database through its session pooler. Application tables are in the private `hoanxu` schema. See `docs/supabase.md` for the required search path, encryption key and separate test database configuration.

`Dockerfile` and `render.yaml` package Go and an SSH tunnel supervisor on Render. The independent folder [`chromium/`](chromium/README.md) at this repository's root packages Chromium, Xvfb and noVNC for EC2 and is excluded from the API Docker build context. Update the existing ignored `env.prod` with the dedicated SSH key, verified known_hosts and shared bridge password according to [deployment instructions](docs/render-browser.md); replace old combined-container browser values in that same production file, preserving application secrets. Import that file into Render rather than creating a separate Render env file. Keep one Render instance, `/readyz`, and the `/var/data` disk for private uploads. CDP and noVNC are loopback only, reached through SSH; the admin display remains protected by recent administrator authentication. Local development keeps its existing headed browser flow. No database migration or frontend endpoint change is required.

## Unified Xu wallet

The wallet now combines approved cashback and check-in rewards (1 Xu = 1 VND). See [migration, API and local checklist](docs/unified-wallet.md). When `.env` targets another environment, use `.env.local` and `./scripts/dev-local.ps1`; `ENV_FILE` selects an explicit profile for Go commands and PowerShell helpers. Stop the API and back up the chosen database before migration 9.

CSP/checker repair, stable error codes, safe diagnostics and real Shopee acceptance evidence: [docs/csp-shopee-repair.md](docs/csp-shopee-repair.md). The repair does not run database migrations.
