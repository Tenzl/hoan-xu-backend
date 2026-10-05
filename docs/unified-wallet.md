# Ví Xu chung và xem trước cashback

## Đơn vị và giao dịch

1 Xu = 1 VND. Cashback đã duyệt và điểm danh vào cùng ví. Điểm danh nhận 300 Xu/ngày; bonus chuỗi 3/7/14/30 là 600/1.500/3.000/9.000 Xu. Các khoản credit trả khoản thiếu trước khi tăng khả dụng. Leaderboard chỉ lấy cashback của đơn đã duyệt.

Nhập hoặc tạo link không tạo đơn hay credit ví. Checker chỉ cung cấp hoa hồng dự kiến; frontend tính khoảng min/max bằng basis point và số nguyên. Sau khi tạo link, preview dùng snapshot của link. Tỷ lệ random chỉ chọn khi ghi nhận đơn lần đầu.

Rút tiền giữ nguyên tối thiểu 50.000 Xu và bội số 1.000. `held` dành cho rút tiền; `giftHeld` là tài khoản `gift_held` dành cho voucher. Tổng tạm giữ trên UI là tổng hai khoản. Quà chốt chi khi cấp mã; từ chối hoàn khoản giữ đúng một lần. Mọi thao tác tài chính khóa system account trước, ledger bất biến và có unique reference/idempotency.

## Chuyển đổi và lịch sử

Migration 9 phải chạy lúc API dừng. Chuyển số dư coin cũ ×300; pending gift giữ cost×300 vào `gift_held`, không vào khả dụng. Catalog ×300. Lưu `cost_xu`, `cost_unit`, `award_xu`; giữ cost/award cũ và ledger coin bất biến để tra cứu. `wallet_unification`, audit và reference duy nhất bảo vệ chuyển đổi lần hai. Coin balance cũ bị khóa tại 0, không cho insert ledger cũ. API `POST /coin-exchanges` trả 410 `COINS_ALREADY_UNIFIED`.

Không rollback bằng down migration sau khi đã chi tiền. Khôi phục bản trước chuyển đổi vào database mới, kiểm tra ledger và cấu hình đích có chủ ý. Không reset database hiện dùng.

## API

- Dashboard: `available`, `pending`, `held`, `giftHeld`, `debt`, `totalOrders`, `pendingOrders`, `approvedOrders`, `rejectedOrders`, `membership`, `unit`.
- Wallet: `available`, `held`, `giftHeld`, `debt`, `unit`.
- Check-in GET: `available`, `streak`, `best`, `lastDay`, `today`, `checkedIn`, `days`, `unit`. POST: `awardXu`, `available`, `streak`, `day`.
- Gift API/updates: `costXu`; historical redemption also returns `costUnit` and `legacyCost` when applicable.
- Wallet history: available delta `amount`, reserve deltas `heldAmount`/`giftHeldAmount`, `unit`. Legacy coin history returns `unit=legacy_coin`, original `amount` and `equivalentXu`.

## Local profiles

`.env` may contain another environment. Use `.env.local` for local PostgreSQL; both are ignored by Git. Never copy connection strings or secrets into documentation.

```powershell
# Backend (from backend folder)
./scripts/dev-local.ps1

# Migration/backup against explicit local profile
$env:ENV_FILE = '.env.local'
./scripts/backup.ps1
./scripts/migrate.ps1

# Test: database ending _test, isolated schemas
./tests/run.ps1
```

Frontend runs separately with `npm run dev` in frontend folder. No Docker/Redis. API defaults to localhost:8080; frontend localhost:3000. Stop the running API before opening another on the same port.

## Acceptance checklist

- [x] Concurrent check-in credits once; milestones and Vietnam day.
- [x] Migration repeat preserves balances/debt/pending reservations and balanced ledger.
- [x] Withdrawal/redeem race cannot overspend; refund/completion once.
- [x] Missing/zero commission, debounce, abort and old responses.
- [x] Preview never credits real balances/orders; snapshot after link creation.
- [x] Inline withdrawal, bank details, uppercase holder, errors, Escape/focus return.
- [x] VI/EN, dark/light, 375/768/1440, reduced motion.
- [x] PostgreSQL local migrated to v9; no ledger/gift-hold discrepancies.
- [x] Backup restored into hoanxu_wallet_restore_20261005_test (pre-cutover v8); ledger reconciled.
