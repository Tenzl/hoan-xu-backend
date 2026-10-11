# hoan-xu-backend

Go/PostgreSQL API for Hoàn Xu. The frontend is at https://github.com/Tenzl/hoan-xu-frontend.

## Run locally

Use the Go version declared in `go.mod`, PostgreSQL, and Chromium for Shopee product checks.

1. Copy `.env.example` to `.env.local`, set `APP_ENV=development`, and configure a loopback PostgreSQL database, `MIGRATION_DATABASE_URL` and `APP_ORIGIN`.
2. For a new empty database, generate `DATA_ENCRYPTION_KEY` with `go run ./cmd/admin key`. For a production clone, preserve the source encryption key to read historical data. Configure separate TRACKING_KEYS and TRACKING_ACTIVE_KEY_ID before generating tracking v4 links; see docs/tracking-v4.md. Never commit it. Configure the same private 32-byte hex `PROXY_SIGNING_KEY` in frontend and backend `.env.local`.
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

`POST /api/v1/affiliate-links` accepts one product at a time on both customer
screens. It calls the logged-in Shopee app's native `batchGetCustomLink` at
`/api/v3/gql?q=batchCustomLink` with exactly one `linkParams` entry. The backend
matches the product, all five encrypted tracking parts and the native short-link
response before saving. There is no multi-link creation endpoint or plus button.

Tracking v4 compact encrypts customer UUID, Affiliate ID, shop/item, issuance
time, policy version and the final payout coefficient using AES-256-GCM and a
separate keyring. Five Base62 parts have position prefixes HX41–HX45. A typical
fixture totals 108 characters (22/22/22/21/21); maximum supported field values
need at most 29 per Sub ID. The adapter's 50-character guard is an application
limit, not a verified official Shopee limit. `trackingCode` is the SHA-256 of
the entire envelope. No legacy tracking format is accepted. See
[wire format, keys and acceptance checklist](docs/tracking-v4.md).

Creation requires Idempotency-Key, a configured tracking key and verification
bound to the current tracking format. Replays reuse the result; same-customer,
same-product requests reuse an active saved link. Creation does not insert an
order or credit a wallet. Public API structure is unchanged and does not expose
the decrypted payload or tax.

Saved links are physically deleted after 120 hours or by their owner; order and
wallet history survive deletion. This retention period is derived metadata,
not part of the encrypted payload or a purchase deadline. CSV attribution works
without the saved link, including late reports and purchases after deletion.

Original Shopee CSV must preserve all five tracking parts and complete source
identifiers, product IDs and Order Time (Vietnam UTC+7). Validation checks the
AEAD, customer, publisher, product and issuance time. Cashback uses only the
verified final coefficient and the report commission, rounded up to whole Xu;
it does not recalculate from current or archived policy rates/tax. Policy version
resolves the audit reference only. Source identity and ledger uniqueness prevent
double credits; approved orders remain immutable. Existing historical orders
and balances are not rewritten. Generic legacy tracking cannot update orders.

This format requires new real Shopee acceptance: confirm link generation and
an original CSV returning all five exact Sub IDs before official activation.
Earlier acceptance of plaintext/HMAC formats does not verify v4 compact.

`CHROME_HEADLESS=false` opens Chromium for manual verification on a desktop. `true` runs it hidden. Shopee may still require manual access verification; successful desktop checks do not guarantee cloud/headless operation. No CAPTCHA or two-factor verification is automated.

Product units were compared with ten Affiliate pages; sanitized evidence is in `tests/fixtures/affiliate/`. The default price scale is 100000, editable in the form's advanced settings. Verification normalizes actual product data and exercises native short-link GQL with five encrypted v4 compact tracking parts. It creates only an administrative verification record, with no customer link/order/wallet writes. Commission attribution is still established by original Shopee reports.

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
