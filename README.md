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

Enter the Shopee **Affiliate ID (Shopee Publisher)** on the same page and save it. The ID is stored in `affiliate_channels.settings`; no `SHOPEE_PUBLISHER` environment variable is required. Link creation still requires this configuration, an available channel and verified tracking. The account signed into Chrome must be the corresponding Affiliate account. Saving an ID or signing in does not automatically enable verified tracking. Production Chrome keeps its profile on EC2 EBS, independently of Render restarts; Shopee can still request login/verification. Historical migration 10 and its private table remain, but the API no longer uses their cookie data.

`POST /api/v1/affiliate-links` generates an official Shopee short link through
`POST /api/v3/gql?q=productOfferLinks` (`batchGetProductOfferLink`) in a logged-in
worker tab. It opens the product offer page and uses its native **Get Link →
Advanced → Add to Link** flow so the Affiliate app supplies its own session and
request headers. A bare fetch/XHR returned HTTP 200 with an upstream error instead
of a link during live acceptance. The backend fills:

- `subId1`: the customer's permanent `users.tracking_code`, created automatically
  on the first Google account registration and retained on subsequent logins.
- `subId2`: the fixed campaign label `hoanxu`.
- `subId3`: a versioned signed-attribution token (36 bytes big endian, Base62 padded to 49 characters) carrying creation time, random nonce, shop/item IDs, archived policy version, tier and chosen rate.
- `subId4`: the Shopee-safe chosen payout factor: `0p63` encodes `0.63`, `0p00` encodes zero and `1p00` encodes one. The API and cashback arithmetic use the decimal factor. Signatures bind the exact carrier string; decimal-point SubIDs are not accepted.
- `subId5`: a 32-character hex HMAC binding the Affiliate ID and SubIDs 1–4. The signing key is domain-derived from `DATA_ENCRYPTION_KEY`.

Session credentials stay in Chrome. The backend accepts only a GQL request matching the product and all five expected SubIDs, followed by its matching HTTPS `s.shopee.vn/<code>` response. An initial standard link is ignored. `POST /api/v1/affiliate-links` returns HTTP 200 with the short URL, tracking token, effective factor/range, policy/tier, `createdAt` and `expiresAt`; it never inserts a link or order. The result exists only in the current UI session. Both creation screens disable copy/purchase at expiry, while the backend independently validates actual order placement time.

New v2 links are saved in `affiliate_links` only after Shopee returns the verified short URL; creation returns HTTP 200 with the database `id`, status and deletion permission. Their cashback deadline is exactly **144 hours (6 days)**: `createdAt <= Order Time < expiresAt`. Already-issued v1 tokens retain their original 168-hour deadline. A report imported later remains eligible if the order was placed in the signed interval, even if its link was physically deleted or marked cancelled. The Shopee URL itself may still open after the cashback deadline. Only the factor is fixed, not an amount: cashback uses the original report's per-item commission and does not multiply Qty again.

`GET /api/v1/affiliate-links` lists each customer's links; `GET /api/v1/affiliate-links/{id}` reads the current status. Report orders determine `progress` (any pending order), `completed` (approved orders, no pending), or `cancelled` (only rejected orders). Without orders, a link is active until its deadline. A minute worker also runs on startup, marks overdue links without pending/approved orders cancelled and sends a single in-app notification. Cancelled records remain until their owner deletes them. A timely late report revives the displayed order state; a deleted link is never recreated by import.

`DELETE /api/v1/affiliate-links/{id}` physically removes an eligible link (204); pending or approved orders lock deletion (409). Legacy links are read-only. Ownership and CSRF are enforced. Creation does not create an order or credit a wallet. Signed orders keep `link_id=NULL`, so deleting their link never deletes the order or its financial history. Imports, order reviews, cancellations and deletion share a per-customer/token transaction lock.

Import the original Shopee CSV with full identifiers and a complete `Order Time` (Vietnam UTC+7). Required columns include `Order id`, `Conversion id`, `Order Status`, `Order Time`, `Shop id`, `Item id`, `Model id`, `Promotion id`, `Item Name`, `Purchase Value(₫)`, `Item Total Commission(₫)`, `Affiliate Item Status` and `Sub_id1–5`. Scientific-notation IDs, `########` and date-only timestamps are errors. Invalid signatures, unrelated campaigns/products and out-of-window rows are ignored with a preview reason. Pending real orders are recorded; cancellations are rejected with zero cashback; completed eligible rows await existing internal approval.

Source identity is order/conversion/shop/item/model/promotion, independent of CSV row number. Re-imports update the same order and never credit a second time. Approved financial changes continue through the existing explicit adjustment/ledger workflow. New orders store their signed tracking, validity interval and effective rate directly, with `link_id=NULL`. Manual order entry requires the same full source identifiers, signed SubIDs and actual order time.

Historical links, orders and wallet balances are preserved. Legacy tracking can only update an existing historical order and cannot establish a new cashback order. Migration 12 adds stateless tracking metadata and stable numeric policy aliases; migration 13 adds the internal policy rate configuration; migration 14 adds saved metadata for new links and supports both token validity intervals. After testing in a separate `_test` database, migration 14 was applied to the configured shared Supabase `hoanxu` schema on 2026-10-07 (version 14, clean), and the new local backend was started. A private schema backup was taken first; before/after and post-restart record digests matched for all 6,752 historical links, 6,749 orders, 103 users and all wallet accounts, entries and transactions. Historical links retain null signed metadata and remain read-only. Wallet balance reconciliation and transaction balancing reported zero mismatches.

Live native GQL acceptance passed on 2026-10-07 with the exact five SubIDs, including the 49-character token, `0p63` factor and 32-character signature, followed by a matching short-link response. This validates link generation; actual purchase attribution still requires an original Shopee report containing all five SubIDs.

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
