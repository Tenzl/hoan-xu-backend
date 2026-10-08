# Nhập báo cáo Shopee

CSV gốc được nhận diện theo `Order id`. Hệ thống giữ Channel, Order Status, Affiliate Item Status, Purchase Value và Item Total Commission trong payload xem trước; các trường nguồn của đơn được lưu trong `source_report`. Admin API trả `reportChannel`, `shopeeOrderStatus`, `affiliateItemStatus`, `reportedValue`, `reportedCommission`; API khách hàng không nhận metadata này.

Promotion ID trống được chuẩn hóa thành `0` khi tạo khóa dòng. Tiền thập phân được đọc bằng chuỗi và số nguyên, không dùng float. Số gốc được giữ để đối chiếu; tiền sử dụng cho tính toán làm tròn xuống đến đồng. Công thức hoàn Xu, thuế và snapshot chính sách hiện có được giữ nguyên.

Chọn hoặc thả file sẽ tạo bản xem trước, chưa tạo đơn và chưa ghi ví. Admin xác nhận nhập bằng endpoint commit hiện có, giữ phân quyền và xác thực lại. `committed_by` lưu người xác nhận để worker ghi sự kiện/audit theo batch và dòng.

Chỉ báo cáo Shopee gốc mới được tự duyệt: Order Status Completed/Complete và Affiliate Item Status Completed/Complete/Approved/Validated. Pending vẫn chờ; hủy/không hợp lệ bị từ chối; trạng thái không nhận diện là lỗi cần sửa trước commit. Tracking, thời hạn, khách hàng và chính sách vẫn phải qua kiểm tra. Đơn bị admin từ chối không tự mở lại; đơn đã duyệt giữ nguyên tài chính.

Worker gọi `orders.Service.EventTx` trong cùng transaction với cập nhật đơn và dòng nhập. Credit dùng reference `order_credit:<id>`; lỗi rollback cả đơn, ví, sự kiện và marker nhập. Nhập lặp không cộng lại. Batches cũ chưa có người xác nhận và payload không có trạng thái gốc giữ luồng duyệt thủ công; migration không hồi tố tài chính.

Migration 32 bổ sung metadata và người xác nhận; không sửa dữ liệu tài chính cũ. Đã áp dụng trên bản sao local `hoanxu_local_20261007_02`. Theo yêu cầu push và migration ngày 08/10/2026, Supabase đã được cập nhật đến migration 35, có sao lưu và kiểm tra dữ liệu cũ; xem `docs/supabase.md`.

Kiểm thử gồm fixture đã ẩn tracking theo cấu trúc CSV thực, giới hạn file/dòng, số thập phân, trạng thái, preview, cập nhật Pending sang Completed, nhập trùng, từ chối nội bộ, rollback/retry, actor của commit và metadata API quản trị. E2E chụp bản xem trước desktop và mobile 320px nền tối.

## Kết quả xác minh — 08/10/2026

- Go focused unit/integration cho CSV, đối soát, rollback/retry, quyết định nội bộ, tranh chấp duyệt, metadata và migration roundtrip đều qua; Go vet qua.
- Frontend typecheck, i18n và production build qua. 42 E2E quản trị liên quan qua; lượt kiểm tra cuối 4 E2E báo cáo qua sau hoàn thiện nút chọn file.
- Bộ Go đầy đủ có API integration qua, nhưng `TestWorkersReuseTabsAndRejectLateResponses/remote` lỗi xác thực fixture ở trạng thái `checking`, lặp lại khi chạy riêng. Package browser không được sửa trong yêu cầu này.
- Ảnh giao diện được lưu tại `frontend/docs/admin-redesign/imports-desktop.png` và `imports-mobile-dark.png`.
