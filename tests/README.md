# Backend tests

All test sources, fixtures and runners live here:

- `unit/<package>/`: affiliate parsing, authentication, browser, cashback, CSV imports, leaderboards and rewards.
- `integration/api/`: HTTP routes, database migrations, permissions, orders and wallet integration tests.
- `fixtures/affiliate/`: sanitized Shopee response/page captures.
- `deployment/`: independent Chromium, real SSH tunnel, key/host pinning, authenticated noVNC, profile permissions, API restart and session revocation. Run `pwsh ./tests/deployment/docker-smoke.ps1` with Docker running; it builds the backend, independent `chromium/` folder and disposable SSH fixture images, then creates/removes isolated containers and volumes. AppArmor production settings must also be verified on Ubuntu EC2.
- `results/`: temporary Go overlay files and optional coverage output, ignored by Git.

Run from the backend repository root:

```sh
go run ./tests/run.go
go run ./tests/run.go vet
go run ./tests/run.go test -run TestPeriodBoundsVietnam -v
node --experimental-vm-modules --test tests/unit/remotebrowser/viewer.test.cjs
```

On Windows, `./tests/run.ps1` loads the backend `.env`, requires the dedicated `TEST_DATABASE_URL` and runs tests/vet. CI uses the same Go runner. Database tests require a separate database ending in `_test`; Chromium tests require `BROWSER_TEST_PATH`.

For an explicitly authorized deployed browser, set `BROWSER_REMOTE_TEST_URL` to
the loopback HTTP endpoint of a verified SSH tunnel and run
`go run ./tests/run.go -run TestRemoteEC2Sandbox -v`. This opt-in test opens its
own tab, checks `chrome://sandbox/`, and closes the controller without stopping
Chromium or interacting with the administrator's login tab. Leave the variable
unset during ordinary tests; real Shopee session acceptance remains manual.

The runner uses Go's `-overlay` flag to compile the stored test sources inside their original `internal` packages without copying files back into production source directories. It tests/vets `./cmd/...` and `./internal/...`, excluding private operational scripts. This preserves tests of private functions and the original package working directories. `tests/go.mod` keeps these stored sources out of ordinary production builds. Use the runner rather than plain `go test ./...`, which does not discover the relocated tests.

Add a test under `unit/<package>/` or `integration/<package>/` with its existing package declaration and a unique `_test.go` filename. The runner discovers it automatically. Read runtime fixtures from `../../tests/fixtures/` because Go tests run in their original internal package directory.
