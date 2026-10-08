# hoan-xu-backend

Go/PostgreSQL API for Hoàn Xu. The frontend is at https://github.com/Tenzl/hoan-xu-frontend.

## Run locally

Use the Go version declared in `go.mod`, PostgreSQL, and Chromium for Shopee product checks.

1. Copy `.env.example` to `.env.local`, set `APP_ENV=development`, and configure a loopback PostgreSQL database, `MIGRATION_DATABASE_URL` and `APP_ORIGIN`.
2. For a new empty database, generate `DATA_ENCRYPTION_KEY` with `go run ./cmd/admin key`. For a production clone, preserve the source encryption key to read historical data and verify tracking. Never commit it. Configure the same private 32-byte hex `PROXY_SIGNING_KEY` in frontend and backend `.env.local`.
3. Run `go run ./cmd/admin migrate` from this repository root. Migrations and SQL sources are included in `database/`.
4. Run `go run ./cmd/api`. The local API listens on `127.0.0.1:8080`; set the frontend's `BACKEND_URL` accordingly.

On Windows, run `scripts/setup-local.ps1`, `scripts/migrate.ps1` and `scripts/dev.ps1` from this repository. The dev script starts only the backend; run the frontend separately. Portable Go and compiled binaries stay in `.tools/`; cookies, browser profiles, backups, files and logs stay in `private-data/`. Both folders are ignored by Git. Go and PowerShell prefer `.env.local` when it exists; `ENV_FILE` selects another explicit profile. A production clone uses separate local files/browser directories and disables remote production browser access.

To create the first admin, set `ADMIN_PASSWORD` in your shell environment and run `go run ./cmd/admin create --username admin --name "Quản trị" --role admin`. Remove the environment variable afterwards. Change the temporary password at first login.

## Import historical customers

`go run ./cmd/admin import-legacy --file <export.md>` previews the five-column
Markdown export (`STT`, `Tên hiển thị`, `Mua lần đầu`, `Mua gần nhất`, `Tổng đơn`)
without opening a database connection. Dates use `dd/MM/yyyy` in Vietnam time.
The generated import below can only apply with `APP_ENV=test` on a loopback `_test` database. Existing business and historical data remain unchanged.

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

Shopee and Chrome settings are entered in **Đăng nhập Shopee → Kết nối Shopee** and saved in the private database configuration. One form saves the shared Affiliate ID plus origin-specific Chrome settings and customer-link activation atomically. Choose local Chrome with its executable/profile or remote Chrome at `http://127.0.0.1:9222`. SSH/noVNC remain deployment infrastructure. Open Chrome, sign in, check the existing session, then verify a product and all five signed SubIDs through native Shopee GQL before enabling customer links. The API never imports or exports cookies.

Both modes preload and reuse two worker tabs after session verification, keeping the administrator's login tab separate. Local mode runs Chrome on the developer's computer; it does not connect to EC2. Failed or timed-out worker tabs are closed and replaced when needed.

Configuration updates require an administrator, CSRF and recent password authentication. Server-owned verification proof is bound to the current configuration; editing the Affiliate ID, connection/profile or price scale invalidates it. Version conflicts return 409 without overwriting the draft. Old publisher/channel write APIs return 410. See [unified Shopee setup and migration](docs/shopee-settings.md).

`POST /api/v1/product-checks` and `POST /api/v1/affiliate-links` accept product
URLs, other affiliates' `s.shopee.vn` short links, `/opaanlp/{shop}/{item}` URLs
and `/an_redir?origin_link=...` wrappers. Resolution follows at most five HTTP
redirects within eight seconds and stops as soon as product IDs are found.
Incoming attribution and tokens are discarded; new records store a canonical
`https://shopee.vn/product/{shop}/{item}` URL. Shop links return
`422 NOT_PRODUCT_LINK`; network failures and timeouts keep separate error codes.

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

Session credentials stay in Chrome. The backend accepts only a GQL request matching the product and all five expected SubIDs, followed by its matching HTTPS `s.shopee.vn/<code>` response. An initial standard link is ignored. `POST /api/v1/affiliate-links` requires Idempotency-Key and stores the verified link plus its fixed snapshot; replay returns the same result. Creation never inserts an order or credits a wallet. Both creation screens disable copy/purchase at expiry, while the backend independently validates actual order placement time.

Signed links are saved for **120 hours (5 days)**. This is list retention, not a cashback deadline. New links return 201 and Location; requests for the same customer/product during retention return the existing link with reused=true and 200. Existing signed v1/v2/v3 metadata and financial snapshots remain verifiable; Order Time must be at or after tracking issuance, with no upper deadline imposed by saved-link retention.

GET /api/v1/affiliate-links lists all saved links independently of order status, excluding signed links whose autoDeleteAt has passed. The separate customer pages are /saved-links, /orders/pending, /orders/approved and /orders/rejected; /orders redirects to pending. Order pages use GET /api/v1/orders?status=pending|approved|rejected and contain orders only. The former mixed /me/purchases API is deprecated.

DELETE /api/v1/affiliate-links/{id} physically deletes the owned signed link (204), including links with pending/approved orders. Unknown or already deleted links return 404. The startup/minute worker physically deletes up to 200 overdue or previously hidden signed links per pass. Orders and wallet history are preserved; tracking is retained on orders before an existing link reference is detached by ON DELETE SET NULL. Legacy links remain read-only. Product and tracking locks serialize creation/deletion and order reconciliation. Apply migration **35** before starting this version (34 is the existing gift-image migration).

Shopee CSV attribution verifies signed SubIDs independently of any saved-link record, including purchases after deletion or after five days. Signature, publisher, customer, product and archived-rate checks remain; duplicate imports never credit twice.

Import the original Shopee CSV with full identifiers and a complete `Order Time` (Vietnam UTC+7). Required columns include `Order id`, `Conversion id`, `Order Status`, `Order Time`, `Shop id`, `Item id`, `Model id`, `Promotion id`, `Item Name`, `Purchase Value(₫)`, `Item Total Commission(₫)`, `Affiliate Item Status` and `Sub_id1–5`. Scientific-notation IDs, `########` and date-only timestamps are errors. Invalid signatures, unrelated campaigns/products and rows dated before tracking issuance are ignored with a preview reason. Pending real orders are recorded; cancellations are rejected with zero cashback; completed eligible rows await existing internal approval.

Source identity is order/conversion/shop/item/model/promotion, independent of CSV row number. Re-imports update the same order and never credit a second time. Reports that change approved orders are ignored with a reason; approved orders and their wallet entries are immutable. Period membership and admin configuration are documented in [docs/cashback-tiers.md](docs/cashback-tiers.md). New orders store their signed tracking, validity interval and effective rate directly, with `link_id=NULL`. Manual order entry requires the same full source identifiers, signed SubIDs and actual order time.

Historical links, orders and wallet balances are preserved. Legacy tracking can only update an existing historical order and cannot establish a new cashback order. Migration 12 adds stateless tracking metadata and stable numeric policy aliases; migration 13 adds the internal policy rate configuration; migration 14 adds saved metadata for new links and supports both token validity intervals. After testing in a separate `_test` database, migration 14 was applied to the configured shared Supabase `hoanxu` schema on 2026-10-07 (version 14, clean), and the new local backend was started. A private schema backup was taken first; before/after and post-restart record digests matched for all 6,752 historical links, 6,749 orders, 103 users and all wallet accounts, entries and transactions. Historical links retain null signed metadata and remain read-only. Wallet balance reconciliation and transaction balancing reported zero mismatches.

Live native GQL acceptance passed on 2026-10-07 with the exact five SubIDs, including the 49-character token, `0p63` factor and 32-character signature, followed by a matching short-link response. This validates link generation; actual purchase attribution still requires an original Shopee report containing all five SubIDs.

`CHROME_HEADLESS=false` opens Chromium for manual verification on a desktop. `true` runs it hidden. Shopee may still require manual access verification; successful desktop checks do not guarantee cloud/headless operation. No CAPTCHA or two-factor verification is automated.

Product units were compared with ten Affiliate pages; sanitized evidence is in `tests/fixtures/affiliate/`. The default price scale is 100000, editable in the form's advanced settings. Verification normalizes actual product data and exercises native short-link GQL with a five-day token, `0p63`, and HMAC. It creates only an administrative verification record, with no customer link/order/wallet writes. Commission attribution is still established by original Shopee reports.

## Validation and contract

```sh
go run ./tests/run.go
go run ./tests/run.go vet
go build ./cmd/api ./cmd/admin
```

Database integration tests require `TEST_DATABASE_URL` pointing to a database whose name ends in `_test`. Chromium integration tests require `BROWSER_TEST_PATH`.

`tests/run.ps1` loads the selected local profile and runs Go tests/vet. All test sources and fixtures live in `tests/`, grouped into unit and API integration tests. The Go runner preserves their original package context using an overlay; plain `go test ./...` does not discover the relocated tests. See `tests/README.md` for filtered runs and fixture paths. Use `scripts/backup.ps1` and `scripts/restore.ps1` for PostgreSQL backups/restores. Operational documentation is in `docs/`; `docs/history/` retains the original combined-workspace instructions as historical references.

The frontend contract is `contracts/openapi.yaml`; `node scripts/validate-contract.mjs` validates it without rewriting it; refine-contract.mjs is a compatibility entry point to that validator. After updating it, copy it into the frontend repository's `contracts/` folder and run `npm run generate` there. API translations are `internal/api/en.json`; synchronize them from the frontend's reviewed translation catalog using an explicit `BACKEND_REPO_DIR`.

## Deployment status

Cloudflare keepalive workers can call public `GET /ping` (for example,
`https://hoan-xu-backend.onrender.com/ping`). It returns HTTP 200 with
`data.status`, `data.service` and `data.time` in the standard JSON envelope,
with `Cache-Control: no-store`. No authentication or database/browser request
is needed. Use `/readyz` when database readiness must also be checked.

Local development uses a verified PostgreSQL clone in the private `hoanxu` schema. Production configuration remains separate. See [local clone and remediation operations](docs/audit-remediation.md) and `docs/supabase.md` for production search-path and encryption-key requirements.

`Dockerfile` and `render.yaml` package Go and an SSH tunnel supervisor on Render. The independent folder [`chromium/`](chromium/README.md) at this repository's root packages Chromium, Xvfb and noVNC for EC2 and is excluded from the API Docker build context. Update the existing ignored `env.prod` with the dedicated SSH key, verified known_hosts and shared bridge password according to [deployment instructions](docs/render-browser.md); replace old combined-container browser values in that same production file, preserving application secrets. Import that file into Render rather than creating a separate Render env file. Keep one Render instance, `/readyz`, and the `/var/data` disk for private uploads. CDP and noVNC are loopback only, reached through SSH; the admin display remains protected by recent administrator authentication. Local development keeps its existing headed browser flow. No database migration or frontend endpoint change is required.

## Gold and green Xu wallets

Cashback and historical balances are gold Xu (withdrawable). Daily check-in and explicit gold-to-green conversions earn green Xu (rewards only). See [rules and maintenance migration checklist](docs/dual-xu.md). The earlier [wallet unification](docs/unified-wallet.md) remains historical documentation. When `.env` targets another environment, use `.env.local` and `./scripts/dev-local.ps1`; `ENV_FILE` selects an explicit profile for Go commands and PowerShell helpers. Stop the API and back up the chosen database before migration 9.

CSP/checker repair, stable error codes, safe diagnostics and real Shopee acceptance evidence: [docs/csp-shopee-repair.md](docs/csp-shopee-repair.md). The repair does not run database migrations.
