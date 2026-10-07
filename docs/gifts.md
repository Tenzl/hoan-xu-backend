# Đổi quà và giữ Xu

`POST /gift-redemptions` nhận `giftId`, thêm `expectedCostXu` tùy chọn để tương thích client cũ. Frontend mới luôn gửi giá đang hiển thị. Khi catalog đổi giá, trả 409 `GIFT_PRICE_CHANGED` trước khi ghi yêu cầu/giữ Xu/trừ kho. Giá sai định dạng hoặc ngoài giới hạn bị từ chối.

Idempotency-Key và payload được lưu cùng transaction. Cùng khách/cùng khóa chỉ giữ Xu một lần, cả khi gọi đồng thời. Replay thành công vẫn trả kết quả cũ sau khi catalog đổi giá. Khóa giá và tồn kho lấy từ `gift_catalog FOR UPDATE`; ledger chuyển Xu khả dụng sang `gift_held`. Thiếu số dư hoàn tác toàn bộ yêu cầu và tồn kho.

Admin hoàn thành phải nhập mã voucher; mã được mã hóa trong DB, API khách chỉ giải mã các yêu cầu của khách. API admin không trả mã/cipher. Từ chối hoàn Xu và trả kho một lần. Mỗi kết quả hoàn thành/từ chối tạo một thông báo hướng đến lịch sử đổi quà trong cùng transaction.

Không thêm migration hoặc thay đổi dữ liệu link/đơn. Dữ liệu local ngày 07/10/2026 có 4 quà đang bật, cả 4 hết tồn kho và chưa có yêu cầu đổi quà. Cần admin bổ sung tồn kho thực tế trước khi cho khách đổi; cấp voucher vẫn là bước quản trị.

Full backend tests (bắt buộc DB riêng), vet và build đã qua. Snapshot trước/sau khởi động local nằm ở `private-data/backups/gifts-20261007-before.json` và `gifts-20261007-after.json`: hash toàn bộ link, đơn, khách, tài khoản và ledger ví khớp. Không đổi quà thực hoặc triển khai Render.
