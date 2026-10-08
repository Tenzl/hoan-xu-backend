# Link đã tạo và đơn hàng

Trang lấy link là /link. Bốn mục có URL riêng: /saved-links, /orders/pending, /orders/approved và /orders/rejected. /orders chuyển đến /orders/pending. Điều hướng dùng liên kết thật, hỗ trợ tải lại và Back/Forward. Các trang đơn không chứa link hoặc countdown. Thông báo cập nhật đơn là 10:00 hằng ngày, giờ Việt Nam.

Danh sách link gọi GET /affiliate-links, bao gồm mọi link chưa tới hạn tự xóa, kể cả link đã phát sinh đơn. Ba trạng thái gọi GET /orders?status=pending|approved|rejected. Cả hai tài nguyên giữ quyền sở hữu, phân trang/cursor hiện có. GET /me/purchases được đánh dấu deprecated và giữ để tương thích; frontend không dùng API gộp này.

## Lưu link và xóa thật

- Link ký được lưu 5 ngày, với autoDeleteAt = createdAt + 120 giờ. Thời hạn này chỉ quản lý danh sách, không quyết định điều kiện hoàn tiền. expiresAt giữ metadata gốc của token v1/v2/v3 để tương thích.
- Cùng khách hàng/sản phẩm trong 5 ngày: POST trả link hiện có, reused=true và HTTP 200; không lấy mẫu lại tỷ lệ. Link mới trả HTTP 201 và Location. Khóa PostgreSQL theo khách hàng/sản phẩm bảo vệ tạo/xóa đồng thời.
- DELETE /affiliate-links/{id} xóa thật bản ghi ký, trả 204. Link không còn hoặc thuộc khách khác trả 404; legacy không có tracking ký vẫn chỉ đọc. Hộp xác nhận giải thích rằng đơn và tiền hoàn được giữ nguyên.
- Worker chạy khi khởi động và mỗi phút, tối đa 200 link/lượt. Xóa link đủ 5 ngày và link ký đã bị ẩn trước đây, không phụ thuộc trạng thái đơn. API đọc loại link hết hạn lưu ngay cả trước khi worker chạy; tạo link mới cũng dọn bản ghi quá hạn của sản phẩm. Không gửi thông báo hủy hoàn tiền vì hết hạn lưu.

## Đối soát độc lập

CSV có tracking ký hợp lệ vẫn ghi nhận sau khi link bị xóa hoặc sau 5 ngày, áp dụng cho cả v1/v2/v3. Giữ kiểm tra Affiliate ID, chữ ký, khách hàng, sản phẩm, phiên bản chính sách và hệ số hoàn; Order Time không được trước thời điểm phát hành tracking. Nhập CSV và duyệt đơn đều không chặn theo expiresAt. Trạng thái sàn và quy trình duyệt hiện có quyết định cộng ví. Nhập lại không tạo đơn hoặc cộng ví trùng; đơn đã duyệt giữ nguyên lịch sử.

Migration 35 thêm index dọn link và đổi khóa ngoại orders.link_id sang ON DELETE SET NULL. Số 34 đã được dùng cho gift_image_position nên không ghi đè migration đó. Thao tác xóa giữ mã tracking trên đơn trước khi tách tham chiếu; không cascade đơn hoặc ví. Migration 33 và metadata tài chính cũ được giữ nguyên. Không tự xử lý lại CSV đã nhập; có thể nhập lại để đánh giá theo quy tắc mới.

## Xác minh local ngày 08/10/2026

Migration 35 đã được áp dụng lên database local hoanxu_local_20261007_02 (clean), sau sao lưu private-data/backups/link-retention-20261008-151515/before.dump. Snapshot trước/sau khởi động backend xác nhận 32.598 đơn và hash dữ liệu tài chính đơn, wallet_accounts, wallet_entries, wallet_transactions giữ nguyên. Không có link ký đến hạn tại thời điểm khởi động (9 link ký); link lịch sử được giữ. Backend dùng binary .tools/bin/hoanxu-api-link-retention.exe. Đây là cập nhật local.

- Backend: toàn bộ test qua runner `go run ./tests/run.go test -p 1` và `vet` đạt, gồm xóa thật, mốc 5 ngày, đơn giữ tracking, CSV muộn v1/v2/v3, chống nhập/cộng ví trùng và khóa tạo/xóa đồng thời.
- Frontend: production build, typecheck, kiểm tra bản dịch và 13 script tests đạt. E2E tổng thể đạt 450 ca, bỏ qua 6 ca theo cấu hình; hai ca lỗi selector hạng thành viên đã sửa và chạy lại đạt trên desktop/mobile, tổng cộng 452 ca đạt. Báo cáo nằm ở `frontend/tests/results/e2e-1791447539865-36224` và `frontend/tests/results/e2e-1791448200745-11476` tính từ workspace gốc.
- Bản frontend mới dùng `.next-link-retention` tại `http://localhost:3000`; `/saved-links` trả 200 và `/orders` chuyển 308 đến `/orders/pending`. Đã kiểm tra ảnh chụp trang đơn trên desktop/mobile.

## Xác minh local ngày 07/10/2026

- Test backend trên DB riêng `_test`, `vet`, build API/admin, frontend typecheck, kiểm tra bản dịch và build đã qua.
- E2E tạo link/Đơn hàng đã qua trên desktop/mobile; gồm 320px, VI/EN, sáng/tối, bàn phím, giảm chuyển động, khóa xóa và chuyển hết hạn sang Từ chối. Lượt toàn bộ ban đầu có lỗi fixture/locator cũ; các nhóm liên quan được cập nhật và chạy lại 66/66 thành công.
- API integration kiểm tra tên đã lưu, từ chối dữ liệu tên chưa xác minh/sai sản phẩm, tên link lịch sử từ đơn, phân trang/cursor, quyền sở hữu, đơn không còn link và báo cáo muộn đúng hạn.
- DB local `hoanxu_local_20261007_02` đang ở migration 21 sạch, bao gồm migration 20. Bản backup trước migration thuộc `private-data/backups/production-local-20261007-canonical/`; backup schema và snapshot trước/sau khởi động thuộc `private-data/backups/purchases-20261007-094642/`.
- Sau khởi động backend mới: 6.754 link, trong đó 6.752 link lịch sử; 6.749 đơn; 103 người dùng và 409 tài khoản ví. Hash toàn bộ link, đơn, người dùng, tài khoản ví, 13.500 wallet entries và 6.750 wallet transactions khớp trước/sau.
- Backend chạy bằng `scripts/dev-local.ps1`; frontend local dùng bản build `.next-purchases` tại `http://localhost:3000`. Không triển khai Render.
