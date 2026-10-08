# Đổi quà và giữ Xu

## Vị trí ảnh voucher — 08/10/2026

Migration `000034_gift_image_position` thêm `gift_catalog.image_position_y`, số nguyên 0–100, mặc định 50 (giữa), có CHECK tại database. API tạo/sửa/đọc quà nhận/trả `imagePositionY`; PATCH bỏ qua giữ vị trí cũ. Giá trị âm, trên 100 và số thập phân bị từ chối. Trường tùy chọn dùng `omitempty` giữ fingerprint của payload cũ khi không gửi vị trí ảnh.

Khách xem ảnh phủ kín khung 16:9 theo vị trí đã lưu; quản trị chỉnh bằng kéo ảnh hoặc thanh trượt. `TestGiftImagePositionPersistsAndValidates` trên database `_test` kiểm tra mặc định, hai biên, lưu vị trí, PATCH giá giữ vị trí và từ chối giá trị ngoài miền. Bộ TestGift và toàn bộ package API qua; vet qua. Hai ca browser fixture không ổn định trong bộ tổng đã chạy lại riêng và qua.

Đã sao lưu và áp dụng các migration local đang chờ tới 34 trong profile development. Digest danh mục/tồn kho, yêu cầu đổi quà, tài khoản ví và ledger trước/sau khớp; snapshot tại `private-data/backups/gift-image-position-20261008/`. Bản API mới build tại `.tools/bin/hoanxu-api-gift-image.exe`; API local hiện tại chưa khởi động lại vì công cụ từ chối thao tác dừng tiến trình. Không cập nhật production.

`POST /gift-redemptions` nhận `giftId`, thêm `expectedCostXu` tùy chọn để tương thích client cũ. Frontend mới luôn gửi giá đang hiển thị. Khi catalog đổi giá, trả 409 `GIFT_PRICE_CHANGED` trước khi ghi yêu cầu/giữ Xu/trừ kho. Giá sai định dạng hoặc ngoài giới hạn bị từ chối.

Idempotency-Key và payload được lưu cùng transaction. Cùng khách/cùng khóa chỉ giữ Xu một lần, cả khi gọi đồng thời. Replay thành công vẫn trả kết quả cũ sau khi catalog đổi giá. Khóa giá và tồn kho lấy từ `gift_catalog FOR UPDATE`; ledger chuyển Xu xanh khả dụng sang `green_gift_held` cho yêu cầu mới. Thiếu số dư hoàn tác toàn bộ yêu cầu và tồn kho.

Admin hoàn thành phải nhập mã voucher; mã được mã hóa trong DB, API khách chỉ giải mã các yêu cầu của khách. API admin không trả mã/cipher. Từ chối hoàn Xu và trả kho một lần. Mỗi kết quả hoàn thành/từ chối tạo một thông báo hướng đến lịch sử đổi quà trong cùng transaction.

Bản đổi quà trước cập nhật quản trị không thêm migration hoặc thay đổi dữ liệu link/đơn. Dữ liệu local ngày 07/10/2026 có 4 quà đang bật, cả 4 hết tồn kho và chưa có yêu cầu đổi quà. Cần admin bổ sung tồn kho thực tế trước khi cho khách đổi; cấp voucher vẫn là bước quản trị.

Full backend tests (bắt buộc DB riêng), vet và build đã qua. Snapshot trước/sau khởi động local nằm ở `private-data/backups/gifts-20261007-before.json` và `gifts-20261007-after.json`: hash toàn bộ link, đơn, khách, tài khoản và ledger ví khớp. Không đổi quà thực hoặc triển khai Render.


## Quản trị danh mục và hoàn Xu khi hết hàng

Migration `000022_gift_icons` bổ sung duy nhất `gift_catalog.icon`, mặc định `gift`. Không xóa quà, không thêm xóa mềm hoặc tặng miễn phí, không tự đổi tồn kho hay hoàn Xu. Chạy migration qua `go run ./cmd/admin migrate` trước khi chạy API mới; thử nghiệm chỉ áp dụng migration trong schema của database riêng `_test`.

`POST /admin/gifts` tạo quà với ID server sinh. `PATCH /admin/gifts/{id}` cập nhật từng trường tên/kênh/giá/tồn kho/active/icon; khi gửi `stock`, bắt buộc gửi `expectedStock`. Giá trị khác kho đang khóa trả 409 `GIFT_STOCK_CHANGED`. Những trường không gửi được giữ nguyên. Mọi ghi danh mục cần quyền `gifts`, phiên xác thực lại, Origin, CSRF và Idempotency-Key. GET quản trị trả `pendingCount` và `pendingXu`; GET yêu cầu hỗ trợ `status`, `giftId`.

`POST /admin/gifts/{id}/out-of-stock` khóa tài khoản hệ thống trước danh mục và yêu cầu. Trong một transaction, đặt kho 0, hoàn mọi yêu cầu pending của quà đó từ tài khoản giữ sang khả dụng theo cost_xu và currency đã lưu (green_gift_held → green_available cho yêu cầu mới; gift_held → available cho yêu cầu cũ), ghi rejected với lý do “Hàng đã hết”, tạo thông báo riêng cho từng khách và audit. Không giới hạn theo trang, không cộng lại kho và không xử lý yêu cầu completed/rejected. Kết quả gồm giftId, stock, refundedCount, refundedXu, refundedGoldXu và refundedGreenXu. Cùng khóa trả kết quả cũ kể cả sau khi nhập kho lại. Khóa mới chỉ xử lý yêu cầu vẫn pending.

Action `refund_out_of_stock` trên `/admin/gift-redemptions/{id}/events` hoàn một yêu cầu, không cộng kho. Action rejected thông thường vẫn cộng lại kho. Kho tự về 0 khi giữ món cuối không kích hoạt hoàn. Admin chủ động đặt lại số lượng khi có hàng.

Thông báo lấy tên khách từ database, dùng “bạn” nếu tên trống. Cấp mã có tên khách/tên quà; hoàn vì hết hàng có tên khách/tên quà/số Xu. Voucher vẫn mã hóa và chỉ chủ sở hữu xem được mã trong lịch sử; không đưa mã vào thông báo/audit.

Kiểm thử riêng bao gồm phân quyền, CSRF, xác thực lại, tồn kho thay đổi, cập nhật từng trường, hoàn hàng loạt vượt một trang, chống hoàn trùng, replay sau nhập kho, lỗi thông báo hoàn tác toàn transaction, và tranh chấp cấp mã với hết hàng.


Xác minh local 07/10/2026: đã áp dụng migration 22 và khởi động API mới, `/readyz` thành công, 4 quà hiện có đều trả icon. Snapshot `private-data/backups/gift-inventory-20261007-before.json` và `gift-inventory-20261007-after.json` khớp hash tồn kho (không tính cột icon mới), yêu cầu đổi quà, tài khoản ví và ledger; đối chiếu lại sau startup vẫn khớp. Không tạo yêu cầu đổi quà hay hoàn Xu trên database local đang sử dụng. Full API tests, kiểm thử riêng TestGift, vet và build đã qua; bốn ca fixture Chrome lỗi trong lần chạy tổng đã qua khi chạy lại riêng. Không triển khai production.


## Ảnh và mô tả quà

Migration 000023_gift_details cho phép channel NULL, bổ sung image_url và description mặc định rỗng, không chỉnh giá/kho/yêu cầu/ví. API tạo quà không yêu cầu kênh, giữ kênh cũ khi PATCH bỏ qua trường này. imageUrl nhận chuỗi rỗng hoặc URL HTTPS hợp lệ tối đa 2.048 ký tự, không nhận credentials/whitespace; backend không fetch URL. description là văn bản thuần tối đa 2.000 ký tự, PATCH bỏ qua thì giữ nguyên, gửi rỗng để xóa. Các trường mới không có giá trị được bỏ khỏi payload idempotency để giữ tương thích với thao tác cũ. Down migration từ chối nếu tồn tại quà không kênh, tránh tự gán kênh giả.


Đã áp dụng migration 23 và khởi động API local ngày 07/10/2026. Snapshot `private-data/backups/gift-details-20261007-before.json` và `gift-details-20261007-after.json` khớp dữ liệu danh mục cũ, tồn kho, yêu cầu, tài khoản ví và ledger. Bốn quà cũ trả imageUrl/description rỗng. Kiểm thử TestGift, vet và build qua; không tạo quà hoặc thao tác tiền trong database sử dụng để kiểm thử.


## Loại Xu thanh toán

Sau migration 24–25, quà mới chỉ dùng Xu xanh. Khách có Xu vàng cần chủ động chuyển ở Ví, hệ thống không tự đổi. Yêu cầu cũ có currency=gold và tiếp tục tiêu/hoàn Xu vàng; yêu cầu mới có currency=green và tiêu/hoàn Xu xanh. Thông báo hoàn ghi loại Xu. Xem [quy trình migration hai ví](dual-xu.md).
