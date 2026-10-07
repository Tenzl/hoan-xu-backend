# Tổng Xu vàng và đã sử dụng

`wallet_user_totals` lưu một dòng mỗi tài khoản, với hai cột `bigint` không âm:

- `gold_total` / API `goldTotal`: tổng `orders.cashback` của đơn hiện có trạng thái `approved`, kể cả đơn nhập tay/lịch sử. Không còn cho điều chỉnh đơn đã duyệt; tổng tiếp tục lấy giá trị đơn đã chốt. Không gồm điểm danh cũ, số dư ban đầu không có đơn hoặc Xu xanh thưởng thêm.
- `gold_used` / API `goldUsed`: tổng `withdrawals.amount` trạng thái `paid` cộng vàng thực tế bị trừ trong ledger chuyển đổi `xu_exchange:`. Không cộng khoản đang chờ, bị từ chối hoặc số xanh nhận.

Hai số này là thống kê, không thay thế số dư ledger. Lịch sử điều chỉnh giảm tiền hoàn trước đây có thể đã tạo `gold_used > gold_total` và khoản nợ vàng. Không ép số đã dùng về tổng, không đổi xanh để trả nợ. Bảng xếp hạng tiếp tục tính trực tiếp từ đơn đã duyệt theo kỳ.

Các API `/wallet`, `/me/dashboard`, `/admin/users` và `/admin/users/{id}` trả cả hai số. Ví, bảng khách mới/cũ và chi tiết khách hiển thị **Tổng Xu vàng** và **Đã sử dụng**. Tất cả alias số dư cũ vẫn là vàng.

`wallet.RefreshGoldTotals` tái tính từ nguồn trong cùng transaction, sau ghi đơn/ledger/trạng thái rút và trước commit. Hàm khóa tài khoản hệ thống vàng trước, xử lý khách theo ID đã sắp xếp; gọi lại không cộng dồn trùng. Duyệt đơn, đơn nhập tay (kể cả batch), nhập lịch sử, xác nhận rút và chuyển đổi sử dụng hàm này. Các dòng CSV chỉ tạo/cập nhật đơn pending/rejected không thay đổi tổng; báo cáo thay đổi đơn approved bị bỏ qua kèm lý do, không sửa nguồn hoặc ví.

## Áp dụng migration 27–28

1. Dừng các API/worker có thể ghi đơn, rút tiền, nhập lịch sử hoặc đổi Xu. Chụp backup và bản chụp orders, withdrawals, wallet_accounts, wallet_transactions, wallet_entries theo quy trình [dual-xu.md](dual-xu.md).
2. Áp dụng migration `000027_wallet_user_totals` tạo bảng rồi `000028_wallet_user_totals_backfill` điền lịch sử, qua `go run ./cmd/admin migrate` và cấu hình môi trường triển khai hiện có. Không khởi động writer cũ sau bước này vì writer cũ không duy trì thống kê.
3. Đối chiếu `gold_total` với tổng cashback approved; `gold_used` với rút paid cộng các entry vàng âm trên tài khoản available của giao dịch có tiền tố **chính xác** `xu_exchange:`. Dữ liệu rỗng phải trả 0. Các bản chụp nguồn và ledger trước/sau phải giống hệt.
4. Triển khai backend/frontend mới rồi mở ghi. Kiểm tra một đơn duyệt, một chuyển đổi và một khoản rút trong môi trường nghiệm thu; xác nhận UI cập nhật đúng và bảng xếp hạng không thay đổi khi đổi Xu.

Backfill chỉ ghi bảng thống kê, không sửa lịch sử hoặc số dư. Down của 28 không xóa số đã tính; down của 27 chỉ bỏ bảng dẫn xuất. Muốn khôi phục bảng phải chạy lại cả schema và backfill khi writer đã dừng. Down thống kê không thay đổi các guard bảo vệ Xu xanh của migration 24–25.

Kiểm thử chạy trên schema PostgreSQL cô lập; chưa áp dụng các migration này vào database đang phục vụ người dùng.
