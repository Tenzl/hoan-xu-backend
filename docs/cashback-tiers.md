# Ba hạng và tỷ lệ hoàn tiền

Khách tự lên hạng theo số bản ghi đơn đã được duyệt nội bộ: Đồng (`bronze`), Bạch kim (`platinum`), Kim cương (`diamond`). Chỉ trạng thái `approved` được tính. Đây là cùng đơn vị đếm `orders` của hệ thống; khóa đơn/dòng nguồn gồm kênh, publisher, mã đơn và mã dòng.

## Cấu hình

Mở `/admin/settings`, nhập ngưỡng đơn và khoảng phần trăm của ba hạng, xem trước rồi lưu. Đồng luôn bắt đầu từ 0; ngưỡng hai hạng sau tăng dần. Mỗi khoảng nằm trong 0–100%, có tối đa hai chữ số thập phân và được phép giao với khoảng hạng khác. Mặc định ngưỡng 0/30/100 và cả ba khoảng bằng tỷ lệ cũ khi migration, hiện 50–50%.

API `GET /api/v1/admin/cashback-policies/current` trả phiên bản hiện tại. `POST /api/v1/admin/cashback-policies` nhận `currentVersionId` và `tiers`, trả 201. Mỗi hạng có `tierCode`, `minApprovedOrders`, `minSharePercent`, `maxSharePercent`. Cần quyền settings, CSRF, xác thực mật khẩu trong 15 phút và Idempotency-Key. Phiên bản cũ trả 409 để người quản trị tải lại; cùng key/payload trả cùng kết quả. Cài đặt chung và FAQ không còn điều chỉnh mức chia.

## Chốt tỷ lệ

Tạo link chụp phiên bản, hạng và min/max tại lúc tạo. Mỗi đơn phát sinh từ link chọn ngẫu nhiên một tỷ lệ ở lần ghi nhận đầu, lưu cùng transaction tạo đơn. Dùng crypto/rand, phân phối đều trên các basis point và bao gồm hai đầu khoảng; min=max trả tỷ lệ cố định. Một basis point bằng 0,01%.

`cashback = commission × shareBps / 10000`, chia số nguyên và làm tròn xuống đồng. Commission lấy từ báo cáo thực nhận, không dùng số checker làm tiền đã duyệt. Link giữ snapshot dù người dùng lên hạng hoặc chính sách mới được lưu. Nhập lại CSV, xử lý đồng thời, retry duyệt và worker restart không chọn lại tỷ lệ đã commit. Điều chỉnh hoa hồng giữ shareBps và tạo bút toán bù. Dòng không khớp tracking chưa có tỷ lệ, tiếp tục cách ly.

## Dữ liệu và kiểm chứng

Migration 8 thêm cashback_tiers, snapshot trên affiliate_links và tỷ lệ trên orders. Link/đơn cũ giữ tỷ lệ cố định từ chính sách trước đây, tier null và số tiền hiện có. Không suy đoán hạng lịch sử hoặc reset ví. Rollback từ chối nếu đã có link/đơn theo hạng để tránh mất snapshot tài chính.

Kiểm thử PostgreSQL dùng database riêng: snapshot/link thật qua service, lên hạng, CSV trùng/concurrent, restart, retry credit, điều chỉnh tăng/giảm, ledger cân bằng, quyền và reauth, xung đột phiên bản, idempotency và giữ tiền qua migration. Unit tests kiểm tra ngưỡng, khoảng/tỷ lệ chính xác, hai đầu random và rounding. Playwright kiểm tra quản trị/khách VI/EN trên desktop/mobile. Fixtures không tạo tiền hoặc đơn trong database ứng dụng.
