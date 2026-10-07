# Lợi nhuận đơn hàng và thực chi

Thông tin lợi nhuận chỉ trả qua API quản trị có quyền `orders`; tổng quan giữ quyền `audit` và chỉ tính khách hàng có email không trống. Trang khách không nhận các trường thuế/lợi nhuận. Đây là phép đọc dữ liệu, không tạo bút toán hoặc thay đổi số Xu hoàn.

- `taxAmount = floor(commission / 20)`: thuế cố định 5% hoa hồng gốc, làm tròn xuống từng đơn đến đồng.
- `projectedProfit = commission - cashback - taxAmount`: dùng Xu hoàn đã lưu của đơn, không tính lại tỷ lệ hoàn.
- `profitStatus`: `estimated` khi chờ duyệt, `projected` khi đã duyệt, `excluded` khi từ chối, `unavailable` khi không có hoa hồng thật. Đơn từ chối có thuế/lợi nhuận 0; lịch sử `legacy-server` và nhập tay `admin-legacy` có hoa hồng thay thế bằng tiền hoàn nên trả null cho thuế/lợi nhuận.

`GET /admin/orders`, `GET /admin/orders/{id}`, và API danh sách/chi tiết đơn của khách trong quản trị dùng `AdminOrder`. API `/orders`, `/orders/{id}` và đơn trong lịch sử mua hàng vẫn dùng `Order`.

Tổng quan là số liệu toàn bộ thời gian của cùng nhóm người dùng mới:

- Thuế tổng = tổng thuế của các đơn đã duyệt, không làm tròn trên tổng hoa hồng.
- Lợi nhuận dự kiến = tổng hoa hồng đã duyệt - tổng Xu hoàn các đơn đã duyệt - thuế tổng.
- `cashProfit` = tổng hoa hồng đã duyệt - tổng `withdrawals.amount` có trạng thái `paid` - thuế tổng.
- Khoản rút pending/processing/rejected không phải thực chi. Không trừ Xu hoàn lần nữa trong `cashProfit`; không phân bổ lần chuyển khoản gộp vào từng đơn. Xu khách chưa rút chưa được trừ trong số liệu này. Các khoản đổi quà/đổi Xu không được coi là chuyển khoản ngân hàng.
- Nếu có đơn đã duyệt thiếu hoa hồng thật trong nhóm tổng quan, `taxAmount`, `projectedProfit`, `cashProfit` là null; `profitUnavailableOrders` là số đơn thiếu dữ liệu. Các trường tổng quan cũ vẫn giữ tương thích. Không có đơn/rút tiền thì tổng là 0. Lợi nhuận âm được giữ nguyên.

Không có migration, không sửa dữ liệu lịch sử. API chi tiết quản trị đọc theo ID trả 422 cho UUID không hợp lệ và 404 khi không có đơn. API đơn của khách trong quản trị tiếp tục kiểm tra đồng thời quyền `users` và `orders` và chủ sở hữu đơn.
