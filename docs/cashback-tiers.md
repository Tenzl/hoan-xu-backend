# Ba hạng và tỷ lệ hoàn tiền

Khách lên hạng theo số bản ghi đơn đã được duyệt nội bộ (`approved`): Đồng (`bronze`), Bạch kim (`platinum`), Kim cương (`diamond`). Đơn đang chờ hoặc bị từ chối không nâng hạng.

## Cấu hình quản trị

Mở `/admin/settings`, nhập ngưỡng đơn, khoảng tỷ lệ nguyên của ba hạng và phần trăm thuế nội bộ. Đồng bắt đầu từ 0; ngưỡng hai hạng sau tăng dần. Mỗi khoảng nằm trong 0–100%, tối đa cao hơn tối thiểu ít nhất 5 điểm phần trăm. Thuế mặc định 5%, cho phép 0–100% với tối đa hai chữ số thập phân.

`GET /api/v1/admin/cashback-policies/current` trả phiên bản và `taxPercent`. `POST /api/v1/admin/cashback-policies` nhận `currentVersionId`, `taxPercent`, `tiers`; trả 201. Giữ quyền settings, CSRF, xác thực mật khẩu gần đây, Idempotency-Key và kiểm tra xung đột phiên bản. Thuế không xuất hiện trên giao diện hoặc response API khách hàng.

## Chốt hệ số

Mỗi yêu cầu tạo link chụp phiên bản chính sách, hạng và thuế trước khi liên hệ Shopee. Dùng crypto/rand chọn đều một số nguyên phần trăm trong khoảng, bao gồm hai đầu. Trúng tối thiểu cộng 1 điểm phần trăm; trúng tối đa trừ 4; kết quả giữa giữ nguyên. Chỉ điều chỉnh một lần.

Hệ số = tỷ lệ sau điều chỉnh / 100 × (1 − thuế / 100), làm tròn **lên** đến hai chữ số thập phân bằng phép tính số nguyên. Ví dụ 66% và thuế 5% cho `0.627`, làm tròn lên `0.63`. API và phép tính dùng hệ số thập phân đúng hai chữ số. Khi truyền qua Shopee, `subId4` thay dấu chấm bằng chữ `p`: `0.63` → `0p63`, `0.00` → `0p00`, `1.00` → `1p00`. Chữ ký xác thực đúng chuỗi truyền đi; CSV phải giữ nguyên Sub_id4. Form Advanced của Shopee đã từ chối dấu chấm, nên không truyền `0.63` trực tiếp.

Link mới được lưu vào `affiliate_links` sau khi Shopee trả URL rút gọn hợp lệ. Token SubID3 và chữ ký SubID5 giữ thông tin sản phẩm, thời điểm tạo, phiên bản chính sách và tỷ lệ đã chọn; token không chứa số Xu cố định. Token v2 có hạn 6 × 24 giờ từ lúc tạo; token v1 đã phát hành giữ hạn 7 × 24 giờ. Thuế thay đổi hoặc lên hạng không thay đổi hệ số link đã tạo; đối soát dùng chính sách lưu trữ theo phiên bản token.

Hết hạn mà không có đơn đang xử lý/đã duyệt: lưu trạng thái cancel và thông báo trong ứng dụng một lần, không tự xóa bản ghi. Khách được xóa thật link chưa có đơn hoặc chỉ có đơn hủy/từ chối; có đơn đang xử lý hoặc đã duyệt thì khóa xóa. Link cũ chưa có metadata mới chỉ được xem. Xóa link không vô hiệu hóa SubID: đơn mua đúng hạn báo về muộn vẫn được ghi nhận, không tạo lại link đã xóa. Bản ghi đơn và ledger ví giữ nguyên.

Khi nhập đơn: `cashback = ceil(commission × effectiveShareBps / 10000)`. Số Xu hoàn của đơn qua link mới làm tròn lên đơn vị nguyên; ví dụ 10.001 × 0.63 → 6.301 Xu. Đơn cũ (`commission_share`) giữ cách làm tròn xuống khi có điều chỉnh, không tính lại lịch sử. Commission lấy từ báo cáo thực tế của dòng sản phẩm, không nhân thêm Qty. Snapshot hệ số được lưu trên đơn khi ghi nhận, cùng tracking và thời gian hợp lệ. Import lặp, duyệt lại và điều chỉnh hoa hồng giữ nguyên tỷ lệ; thay đổi tiền qua bút toán ví hiện có.

Trước tạo link, backend liệt kê tối đa 101 kết quả random để trả đúng khoảng tỷ lệ hiệu lực. Không lấy hai đầu cấu hình làm hai đầu dự kiến vì quy tắc điều chỉnh làm thay đổi cực trị. Sau tạo, frontend dùng `sharePercent` đã chốt và hoa hồng checker để hiển thị một mức tiền dự kiến; hoa hồng báo cáo vẫn quyết định tiền thực tế. Chính sách cũ có khoảng không hợp lệ phải được quản trị sửa trước khi tạo link mới.

## Dữ liệu và kiểm chứng

Migration 13 thêm thuế phiên bản chính sách, mặc định 500 basis points. Migration 14 bổ sung metadata link mới và hỗ trợ thời hạn 144/168 giờ. Không cập nhật link cũ, đơn cũ hoặc số dư. Migration 14 đã được áp dụng lên schema Supabase `hoanxu` ngày 2026-10-07 sau kiểm thử và sao lưu (version 14, clean). Đối chiếu trước/sau và sau khi khởi động backend xác nhận 6.752 link lịch sử, 6.749 đơn, khách hàng và toàn bộ dữ liệu ví giữ nguyên; không có chênh lệch số dư hoặc giao dịch mất cân bằng.

Kiểm thử PostgreSQL trên database riêng có tên kết thúc `_test`; unit tests bao phủ endpoint, khoảng nguyên, thuế, làm tròn và không lộ cấu hình nội bộ. Playwright kiểm tra cấu hình quản trị, khoảng dự kiến, tỷ lệ chốt và vòng ví trên desktop/mobile.
