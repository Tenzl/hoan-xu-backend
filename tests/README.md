# Backend tests

All test sources, fixtures and runners live here:

- `unit/<package>/`: affiliate parsing, authentication, browser, cashback, CSV imports, leaderboards and rewards.
- `integration/api/`: HTTP routes, database migrations, permissions, orders and wallet integration tests.
- `fixtures/affiliate/`: sanitized Shopee response/page captures.
- `deployment/`: Docker smoke test for headed Chrome, authenticated noVNC, disk permissions and session revocation. Run `pwsh ./tests/deployment/docker-smoke.ps1` after building `hoanxu-backend:browser`; it creates and removes isolated test containers and a test volume.
- `results/`: temporary Go overlay files and optional coverage output, ignored by Git.

Run from the backend repository root:

```sh
go run ./tests/run.go
go run ./tests/run.go vet
go run ./tests/run.go test -run TestPeriodBoundsVietnam -v
```

On Windows, `./tests/run.ps1` loads the backend `.env`, requires the dedicated `TEST_DATABASE_URL` and runs tests/vet. CI uses the same Go runner. Database tests require a separate database ending in `_test`; Chromium tests require `BROWSER_TEST_PATH`.

The runner uses Go's `-overlay` flag to compile the stored test sources inside their original `internal` packages without copying files back into production source directories. This preserves tests of private functions and the original package working directories. `tests/go.mod` keeps these stored sources out of ordinary production builds. Use the runner rather than plain `go test ./...`, which does not discover the relocated tests.

Add a test under `unit/<package>/` or `integration/<package>/` with its existing package declaration and a unique `_test.go` filename. The runner discovers it automatically. Read runtime fixtures from `../../tests/fixtures/` because Go tests run in their original internal package directory.
