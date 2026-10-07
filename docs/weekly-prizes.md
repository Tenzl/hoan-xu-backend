# Nhập đơn JSON và thưởng Top 5 tuần

`POST /api/v1/admin/users/{userId}/orders/batch` nhận `{ "orders": [...] }` gồm 1–100 đơn của một khách cũ. Các trường: `productName` (1–200 ký tự), `orderedAt` (RFC3339, có múi giờ, không tương lai), `cashback` (số nguyên 1..10^12), `note` (không bắt buộc, tối đa 500 ký tự). Có quyền users + orders, CSRF, mật khẩu xác thực gần đây và Idempotency-Key. Cả lô và ledger nằm cùng transaction. `approvedAt` là ngày lưu; ngày mua không chuyển điểm sang kỳ trước.

Mẫu và hướng dẫn sao chép/tải nằm trên trang đơn khách cũ. Giữ API thêm một đơn. Danh sách và chi tiết khách có `weekRank` và `monthRank` nullable, xếp chung trước khi lọc nhóm và phân trang.

## Chương trình tuần

Migration `000026_weekly_prizes` chỉ thêm bảng chương trình/phần thưởng và trigger bảo vệ lịch sử; không đổi các đơn/ledger/đổi quà hiện có. Rollback từ chối nếu đã có chương trình, để không mất lịch sử hoặc phần giữ kho. Backend owner truy cập bảng, RLS và quyền PUBLIC bị thu hồi.

Các API quản trị cần quyền gifts; ghi cần recent-auth + CSRF + Idempotency-Key:

- `GET/POST /admin/leaderboard-prizes`: xem/cấu hình tuần hiện tại hoặc kế tiếp. `version:0` tạo mới; cập nhật phải gửi version hiện tại.
- `GET /admin/leaderboard-prizes/{id}/preview`: top 5 và hash gắn version/các trường người thắng. Sau chốt trả lịch sử cố định.
- `POST /admin/leaderboard-prizes/{id}/settle`: `{hash}`; chỉ tuần bật đã kết thúc. Hash sai trả 409 yêu cầu xem trước lại.
- `GET /admin/leaderboard-prizes/{id}/awards`: quản trị theo dõi/trao cho cả hai nhóm khách.
- `POST /admin/leaderboard-awards/{id}/deliver`: `{deliveryNote}` mã voucher/ghi chú 1–2000 ký tự. Mã hóa bằng khóa dữ liệu hiện có; audit, notification và idempotency response không chứa plaintext. Đã trao không sửa lại.
- `GET /me/leaderboard-awards`: chỉ phần thưởng của tài khoản hiện tại, gồm thông tin giao quà riêng tư.
- `GET /leaderboard-prizes/current`: công khai chỉ chương trình bật trong tuần hiện tại; không có thì `data:null`. Tuần sau không quảng bá trước.

Tuần bắt đầu thứ Hai 00:00 GMT+7. Top: tổng Xu đơn approved, số đơn, ID; loại khách bị khóa; check-in không tính. Quà không có chi phí Xu, không tạo redemption hay ledger. Khi bật giữ năm đơn vị bằng cách giảm stock có thể đổi; sửa/đổi/tắt hoàn cũ giữ mới atomic. Chốt lưu tên khách, hạng, điểm, số đơn và snapshot quà; ít hơn năm trả phần dư. Giao quà và chốt không lặp thông báo. Không tự mở tuần tiếp theo; thưởng tháng chưa triển khai.

Migration đã được kiểm tra trên database test/local loopback. Theo yêu cầu push và cập nhật Supabase ngày 2026-10-08, migration production đã chạy đến phiên bản 31; xem `docs/supabase.md` để biết kết quả kiểm chứng và bản sao lưu. Frontend phát triển dùng `npm run dev`, không cần build sau mỗi sửa.
