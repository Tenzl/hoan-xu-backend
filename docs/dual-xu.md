# Xu vàng và Xu xanh

Xu vàng giữ các tài khoản `available`, `held`, `gift_held`, `debt`, `system` hiện có. Hoàn tiền đơn và đơn nhập tay tiếp tục cộng vàng, xử lý nợ theo quy tắc hiện có. Xu xanh dùng `green_available`, `green_gift_held`, `green_system`; điểm danh cộng xanh, không trả nợ vàng. `coin_accounts` chỉ tiếp tục giữ chuỗi điểm danh, không chứa số dư mới.

Quà mới giữ xanh. Cột `gift_redemptions.currency` lưu loại Xu để cấp voucher, từ chối và hoàn hết hàng dùng đúng tài khoản. Các yêu cầu tồn tại khi migration chạy được gắn vàng. Không tự đổi vàng lúc đổi quà. Khách chủ động đổi vàng → xanh, xác nhận tỷ lệ và số Xu nhận. Không có chiều ngược.

Tỷ lệ nằm trong `xu_exchange_policies`, mỗi lần lưu tạo một phiên bản bất biến. Khóa chính sách chung tuần tự hóa đổi Xu và cập nhật tỷ lệ. Hạng Thân thiết/Bạc/Vàng/Kim cương mặc định nhận thêm 3%/6%/10%/15% so với tỷ lệ gốc, do admin cấu hình trong chính sách hạng theo kỳ. Phép tính `floor(goldAmountXu * greenUnits * (100 + bonusPercent) / (goldUnits * 100))` dùng số nguyên lớn và chỉ làm tròn một lần cuối cùng. Backend lấy hạng từ tiền hoàn đã duyệt của kỳ trước/hiện tại theo [cashback-tiers.md](cashback-tiers.md); giao diện gửi hạng và phiên bản chính sách đã xem để phát hiện báo giá cũ. Ledger của chuyển đổi có bốn chân; vàng và xanh cân bằng riêng. Replay thành công được trả trước khi kiểm tra chính sách/hạng mới, trong `Store.Action`. Hộp thoại đổi Xu nằm trong `/gift`; `/wallet?exchange=1` chuyển sang `/gift?exchange=1`. Tổng và đã sử dụng vàng được mô tả trong [gold-totals.md](gold-totals.md).

## Áp dụng migration 24–25

1. Đưa ứng dụng vào bảo trì; dừng **mọi** API/worker/tác vụ nhập và đối soát có thể ghi ví. Không chạy phiên bản cũ trong lúc chuyển đổi hoặc khởi động lại phiên bản cũ sau khi mở ghi Xu xanh.
2. Chụp backup PostgreSQL bằng `pg_dump` và lưu bản chụp đối chiếu trước migration, khi tất cả writer đã dừng. Với môi trường Windows dùng các hàm `Get-TaskTool`, `Set-TaskPostgres -SchemaOwner` trong `scripts/common.ps1` để không đưa mật khẩu lên dòng lệnh.
3. Chụp các truy vấn ở dưới ra file; chạy `go run ./cmd/admin migrate` theo cơ chế triển khai hiện có (production cần ENV_FILE và cờ cho phép production).
4. Chụp lại trước khi khởi động writer. Các bản chụp **vàng và ledger phải giống hệt**, số dư mọi tài khoản xanh phải bằng 0, tỷ lệ ban đầu là 1:1, mọi yêu cầu quà tồn tại phải có `currency='gold'`. Kiểm tra ledger không lệch và khoản giữ/nợ không thay đổi. Nếu đối chiếu sai, giữ bảo trì và điều tra; không mở ghi ví.
5. Triển khai backend/frontend hỗ trợ hai ví rồi mới mở ghi. Kiểm tra điểm danh, chuyển đổi, rút vàng và đổi quà xanh bằng tài khoản kiểm thử.

```sql
-- Chụp vàng trước và sau; tài khoản xanh được loại khỏi lần chụp sau.
SELECT id,user_id,kind,balance FROM wallet_accounts
WHERE kind NOT LIKE 'green_%' ORDER BY id;
SELECT id,reference,description,created_at FROM wallet_transactions ORDER BY id;
SELECT id,transaction_id,account_id,amount FROM wallet_entries ORDER BY id;
SELECT id,user_id,gift_id,cost,status,created_at FROM gift_redemptions ORDER BY id;
-- Đối chiếu số dư với ledger: phải không có dòng.
SELECT a.id,a.kind,a.balance,coalesce(sum(e.amount),0) AS ledger_balance
FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id
GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0);
-- Sau migration: phải không có dòng.
SELECT e.transaction_id,CASE WHEN a.kind LIKE 'green_%' THEN 'green' ELSE 'gold' END AS currency
FROM wallet_entries e JOIN wallet_accounts a ON a.id=e.account_id
GROUP BY e.transaction_id,currency HAVING sum(e.amount)<>0;
SELECT * FROM wallet_accounts WHERE kind LIKE 'green_%' AND balance<>0;
SELECT * FROM gift_redemptions WHERE currency<>'gold';
SELECT gold_units,green_units FROM xu_exchange_policies;
```

Down migration 25 và 24 đều từ chối nếu có ledger xanh, số dư xanh, yêu cầu quà xanh hoặc lịch sử chỉnh tỷ lệ. Không bỏ guard để rollback. Khi đã phát sinh dữ liệu, sửa tiến về phía trước hoặc phục hồi toàn bộ backup trong bảo trì theo quy trình khôi phục, không xóa điểm hoặc biến điểm thành tiền rút.

Migration được kiểm thử trên schema PostgreSQL cô lập, gồm database chưa có đơn, giữ lịch sử vàng, hoàn quà cũ và chặn down sau phát sinh điểm. Việc kiểm thử không áp dụng migration lên database đang phục vụ người dùng.
