# Chính sách hạng theo kỳ và tỷ lệ hoàn tiền

Khách được xét bằng Xu vàng hoàn từ đơn đã duyệt trong kỳ, kể cả đơn nhập tay. Chính sách mặc định có bốn hạng:

| Mã | Tên Việt/Anh | Hoàn vàng tối thiểu | Thưởng đổi Xu |
|---|---|---:|---:|
| member | Thân thiết / Member | 0 | 3% |
| silver | Bạc / Silver | 500.000 | 6% |
| gold | Vàng / Gold | 1.500.000 | 10% |
| diamond | Kim cương / Diamond | 3.000.000 | 15% |

Mặc định kỳ 6 tháng bắt đầu 01/01 và 01/07 theo Asia/Ho_Chi_Minh, tính theo ngày duyệt hoàn. Hạng khởi điểm lấy kết quả kỳ trước; hạng hiện tại là mức cao hơn giữa hạng khởi điểm và hạng đạt trong kỳ này. Đủ ngưỡng lên ngay. Sang kỳ mới, chỉ dùng kỳ vừa kết thúc để giữ hoặc xuống hạng. Ví dụ Kim cương kỳ đầu, hoàn 600.000 trong kỳ hai thì đầu kỳ ba về Bạc. Không mua nhiều kỳ sẽ về Thân thiết.

Điểm danh, rút, chuyển đổi và Xu xanh không tham gia xét hạng. Tổng vàng trọn đời, đã sử dụng và số dư không đặt lại; bảng xếp hạng giữ cách tính hiện có.

## Cấu hình quản trị

Trong `/admin/settings`, chỉnh tên Việt/Anh, ngưỡng vàng, thưởng đổi Xu, khoảng tỷ lệ hoàn mua hàng, thuế, độ dài kỳ 1–12 tháng, ngày mốc và ngày dùng tính kỳ (đặt/duyệt). Bốn mã/thứ tự cố định; hạng đầu ngưỡng 0, các ngưỡng sau tăng dần đến 10^12; thưởng nguyên 0–100%; tên 1–40 ký tự. Các biên ngày 29–31 được kẹp cuối tháng từ mốc gốc, không lệch dần. Có xem trước kỳ và quyền lợi. Tỷ lệ gốc vàng/xanh dùng phần cài đặt riêng hiện có.

Khoảng mua hàng nguyên 0–100% và rộng ít nhất 5 điểm phần trăm. Khoảng bằng nhau của chính sách cũ được giữ khi sửa thông số khác; muốn đổi khoảng phải dùng khoảng hợp lệ. Thuế 0–100%, tối đa hai chữ số thập phân, không lộ trên API khách.

`GET /api/v1/admin/cashback-policies/current` và `POST /api/v1/admin/cashback-policies` dùng cùng phiên bản cho các trường `nameVi`, `nameEn`, `minGoldTotal`, `exchangeBonusPercent`, `periodMonths`, `anchorDate`, `dateBasis`, `taxPercent`, `tiers`. Lưu cần quyền settings, CSRF, xác thực gần đây, Idempotency-Key, audit và currentVersionId; xung đột trả 409. Lưu cấu hình tính lại hạng ngay, link và giao dịch cũ giữ snapshot. Membership trả kỳ trước/hiện tại, hạng khởi điểm/hiện tại/dự kiến, số còn thiếu để lên/giữ. Giao diện tải lại khi giao kỳ và định kỳ để cập nhật cấu hình.

## Chốt hệ số

Mỗi yêu cầu tạo link chụp phiên bản chính sách, hạng và thuế trước khi liên hệ Shopee. Dùng crypto/rand chọn đều một số nguyên phần trăm trong khoảng, bao gồm hai đầu. Trúng tối thiểu cộng 1 điểm phần trăm; trúng tối đa trừ 4; kết quả giữa giữ nguyên. Chỉ điều chỉnh một lần.

Hệ số = tỷ lệ sau điều chỉnh / 100 × (1 − thuế / 100), làm tròn **lên** đến hai chữ số thập phân bằng phép tính số nguyên. Ví dụ 66% và thuế 5% cho `0.627`, làm tròn lên `0.63`. API và phép tính dùng hệ số thập phân đúng hai chữ số. Khi truyền qua Shopee, `subId4` thay dấu chấm bằng chữ `p`: `0.63` → `0p63`, `0.00` → `0p00`, `1.00` → `1p00`. Chữ ký xác thực đúng chuỗi truyền đi; CSV phải giữ nguyên Sub_id4. Form Advanced của Shopee đã từ chối dấu chấm, nên không truyền `0.63` trực tiếp.

Link mới được lưu vào `affiliate_links` sau khi Shopee trả URL rút gọn hợp lệ. Token SubID3 và chữ ký SubID5 giữ thông tin sản phẩm, thời điểm tạo, phiên bản chính sách và tỷ lệ đã chọn; token không chứa số Xu cố định. Token v2 có hạn 6 × 24 giờ từ lúc tạo; token v1 đã phát hành giữ hạn 7 × 24 giờ. Thuế thay đổi hoặc lên hạng không thay đổi hệ số link đã tạo; đối soát dùng chính sách lưu trữ theo phiên bản token.

Hết hạn mà không có đơn đang xử lý/đã duyệt: lưu trạng thái cancel và thông báo trong ứng dụng một lần, không tự xóa bản ghi. Khách được xóa thật link chưa có đơn hoặc chỉ có đơn hủy/từ chối; có đơn đang xử lý hoặc đã duyệt thì khóa xóa. Link cũ chưa có metadata mới chỉ được xem. Xóa link không vô hiệu hóa SubID: đơn mua đúng hạn báo về muộn vẫn được ghi nhận, không tạo lại link đã xóa. Bản ghi đơn và ledger ví giữ nguyên.

Khi nhập đơn: `cashback = ceil(commission × effectiveShareBps / 10000)`. Số Xu hoàn của đơn qua link mới làm tròn lên đơn vị nguyên; ví dụ 10.001 × 0.63 → 6.301 Xu. Đơn cũ (`commission_share`) giữ số đã chốt, không tính lại lịch sử. Commission lấy từ báo cáo thực tế của dòng sản phẩm, không nhân thêm Qty. Snapshot hệ số được lưu trên đơn khi ghi nhận, cùng tracking và thời gian hợp lệ. Đơn đã duyệt không còn được điều chỉnh. Báo cáo khác với đơn đã duyệt bị bỏ qua kèm lý do; không sửa đơn, nguồn trạng thái hoặc ví. Các đơn chưa duyệt giữ luồng xử lý hiện có; lịch sử điều chỉnh, nợ và ledger cũ được giữ.

Trước tạo link, backend liệt kê tối đa 101 kết quả random để trả đúng khoảng tỷ lệ hiệu lực. Không lấy hai đầu cấu hình làm hai đầu dự kiến vì quy tắc điều chỉnh làm thay đổi cực trị. Sau tạo, frontend dùng `sharePercent` đã chốt và hoa hồng checker để hiển thị một mức tiền dự kiến; hoa hồng báo cáo vẫn quyết định tiền thực tế. Chính sách cũ có khoảng không hợp lệ phải được quản trị sửa trước khi tạo link mới.

## Dữ liệu và kiểm chứng

Migration 13 thêm thuế phiên bản chính sách, mặc định 500 basis points. Migration 14 bổ sung metadata link mới và hỗ trợ thời hạn 144/168 giờ. Không cập nhật link cũ, đơn cũ hoặc số dư. Migration 14 đã được áp dụng lên schema Supabase `hoanxu` ngày 2026-10-07 sau kiểm thử và sao lưu (version 14, clean). Đối chiếu trước/sau và sau khi khởi động backend xác nhận 6.752 link lịch sử, 6.749 đơn, khách hàng và toàn bộ dữ liệu ví giữ nguyên; không có chênh lệch số dư hoặc giao dịch mất cân bằng.

Kiểm thử PostgreSQL trên database riêng có tên kết thúc `_test`; unit tests bao phủ endpoint, khoảng nguyên, thuế, làm tròn và không lộ cấu hình nội bộ. Playwright kiểm tra cấu hình quản trị, khoảng dự kiến, tỷ lệ chốt và vòng ví trên desktop/mobile.

## Triển khai migration 29–30

1. Dừng writer đơn/link/ví và các tác vụ đối soát; sao lưu và chụp orders, affiliate_links, wallet_accounts, wallet_transactions, wallet_entries, wallet_user_totals theo [dual-xu.md](dual-xu.md).
2. Áp dụng 29 (schema) rồi 30 (chính sách mặc định), sau các migration trước đó. 30 tạo phiên bản mới, không sửa lịch sử: Thân thiết/Bạc kế thừa Đồng; Vàng kế thừa Bạch kim; Kim cương kế thừa Kim cương cũ. Không backfill hạng cố định: xét trực tiếp lịch sử hai kỳ ở thời điểm yêu cầu.
3. Đối chiếu snapshot trước/sau phải giống nhau, kiểm tra ledger cân bằng riêng vàng/xanh và số dư khớp ledger. Chỉ chính sách/schema thay đổi. Kiểm tra cả database rỗng và tài khoản có lịch sử; triển khai backend/frontend tương ứng rồi mở ghi.
4. Mã tracking v2 giữ chỉ số cũ 0=bronze, 1=platinum, 2=diamond; thêm 3=member, 4=silver, 5=gold. Link và báo cáo lịch sử vẫn đọc được.

Down 30 từ chối khi chính sách đã có snapshot link/đơn hoặc giao dịch tài chính/cấu hình. Down 29 từ chối khi còn hạng mới. Khi guard từ chối, dùng migration tiến hoặc phục hồi đầy đủ backup trong bảo trì, không xóa ledger để ép rollback. Các migration này chỉ được kiểm thử trên schema PostgreSQL cô lập; chưa áp dụng vào database phục vụ người dùng.

## Kiểm thử chính sách theo kỳ — 07/10/2026

- Toàn bộ Go unit/integration PostgreSQL qua (75 file test); bộ bổ sung về migration, thay ngưỡng tức thời và lịch kỳ cũng qua. `go build` và `go vet` qua.
- Kiểm chứng mốc tiền, lên/giữ/xuống hạng, nhiều kỳ không mua, ngày 29–31/năm nhuận, ngày đặt/duyệt, bảo toàn ledger/ranges/tracking, chặn chỉnh đơn đã duyệt, idempotency và báo giá đổi Xu.
- Lượt Playwright toàn bộ có 339/344 ca qua; 5 lỗi fixture/locator được sửa và kiểm chứng lại trong các nhóm liên quan. Nhóm cuối có 26/26 ca desktop/mobile qua, gồm thêm 2 ca chặn điều chỉnh đơn; nhóm locale đã qua sau sửa fixture leaderboard. Không còn ca thất bại chưa xử lý.
- Typecheck, Next production build, kiểm tra bản dịch Việt/Anh và 10 kiểm thử script qua. OpenAPI backend/frontend và types đã đồng bộ cho chính sách/membership/đổi Xu.
- Không chạy migration hoặc ghi dữ liệu vào database đang phục vụ người dùng.
