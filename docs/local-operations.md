# Vận hành local

## Mở và đóng ứng dụng

Chạy `scripts/dev.ps1` từ root của repo backend. Script kiểm tra port API, build Go, mở tiến trình ẩn và kiểm tra API sẵn sàng. Enter dừng tiến trình backend mà script đã tạo; PostgreSQL Windows service vẫn hoạt động. Log ở `private-data/logs/`. Frontend chạy riêng bằng `npm run dev` hoặc `scripts/dev.ps1` từ repo frontend.

Tự tạo admin bằng `scripts/create-admin.ps1`. Mật khẩu nhập qua prompt ẩn, không xuất ra log hoặc command argument. Đăng nhập `/login`, đổi mật khẩu tạm. Khi thao tác tiền, reset tài khoản hoặc sửa chính sách yêu cầu xác thực lại, nhập mật khẩu rồi thực hiện lại thao tác.

## Đối soát

Chính sách bốn hạng theo kỳ được quản lý tại `/admin/settings`: tên, ngưỡng hoàn vàng, thưởng đổi Xu, tỷ lệ mua hàng, thuế và lịch kỳ đều chỉnh được. Mặc định Thân thiết/Bạc/Vàng/Kim cương: 0/500.000/1.500.000/3.000.000 Xu, thưởng 3/6/10/15%. Lưu tạo phiên bản bất biến; hạng khách tính lại ngay, link/giao dịch đã chốt giữ snapshot. Xem [cashback-tiers.md](cashback-tiers.md) cho lên/giữ/xuống hạng và triển khai 29–30.

1. Xuất báo cáo hoa hồng từ publisher thực, chuẩn bị CSV UTF-8 theo file mẫu. Tiền là số nguyên VND; giữ channel/publisher/order/line riêng.
2. Upload tại Nhập báo cáo CSV, cấu hình mapping nếu tên cột khác. Kiểm tra valid/invalid/unmatched trước commit.
3. Dòng invalid cần sửa file. Dòng unmatched chỉ khớp tracking có bằng chứng; không gán ngẫu nhiên khách.
4. Commit và theo dõi batch. Worker lưu tiến độ, lease hết hạn được nhận lại sau restart. Đơn nguồn pending chưa được cộng tiền.
5. Nguồn approved mới có thể duyệt nội bộ. Duyệt và ghi ví là một transaction. Báo cáo đổi sau duyệt bị bỏ qua kèm lý do; không điều chỉnh đơn hoặc ví đã chốt.

## Chi trả và voucher

Khách mở Hồ sơ để lưu ngân hàng, số tài khoản và tên chủ tài khoản đúng như ngân hàng hiển thị. Ba trường phải đầy đủ, hoặc để trống cả ba để xóa cấu hình. Tên chủ tài khoản độc lập với tên hiển thị; số 0 đầu tài khoản được giữ nguyên. Dữ liệu mã hóa bằng khóa local; không đưa vào audit payload. Form rút tự điền hồ sơ nhưng lưu snapshot tại lúc gửi. Quản trị chuyển tiền theo snapshot của yêu cầu, không dựa vào hồ sơ đã đổi sau đó.

Nút VI/EN ở cuối sidebar chuyển ngôn ngữ và nhớ trên cùng trình duyệt; mobile mở bằng Thêm. Nội dung bài viết, tên người dùng và thông tin ngân hàng không được tự dịch. HTML mẫu gốc được lưu trong `docs/reference/` của repo frontend; `/demo` không được phục vụ trong ứng dụng thật.

Khách tạo yêu cầu rút chuyển khả dụng sang held. Người có quyền nhận xử lý trước khi chuyển khoản. Chuyển thật, upload bằng chứng PNG/JPEG/PDF và nhập mã giao dịch mới xác nhận paid. Chưa rõ kết quả ngân hàng thì giữ processing. Từ chối chỉ khi xác định chưa chi; hệ thống hoàn held một lần.

Chuẩn bị ngân sách trước khi bật đổi xu thành tiền. Chuẩn bị mã trước khi tăng tồn voucher. Khách đổi quà giữ xu/tồn; người xử lý nhập mã để hoàn thành hoặc lý do để từ chối. Khách xem mã trong tài khoản; chưa có gửi email tự động.

## Kiểm tra và khôi phục

Trang Lịch sử quản trị kiểm tra số dư khớp ledger. Điều chỉnh âm có thể tạo debt; khách có debt không rút được đến khi xử lý. Không sửa số dư trực tiếp bằng SQL.

Backup bằng `scripts/backup.ps1`; sao lưu riêng files/profile/khóa mã hóa. Restore với `scripts/restore.ps1 -Backup PATH -TargetDatabase DB_MOI`. Script từ chối DB ứng dụng hoặc DB đã có bảng. Kiểm tra migration, bảng, ledger và khóa giải mã trước khi đổi kết nối. Không ghi đè hoặc reset DB đang dùng.

## Cookie và phiên Shopee

Mở `/admin/cookies`, tại **Phiên và cookie Shopee** dán Cookie header hoặc JSON cookie đã xuất từ Affiliate, tối đa 64 KB. Chọn **Lưu và áp dụng cookie**; ô được xóa sau khi lưu thành công và trang hiển thị trạng thái phiên. Cookie hết hạn cần lấy cookie mới từ phiên đăng nhập hợp lệ; không vượt CAPTCHA/2FA.

Nút **Kiểm tra phiên hiện có** kiểm tra Chromium đang chạy, không gửi lại cookie từ ô nhập. Các check sản phẩm dùng chung browser/profile và cookie đang có, mỗi job mở tab riêng. Khi browser restart, ứng dụng nạp bản cookie đã dán từ file mã hóa nằm cạnh thư mục profile; giữ khóa mã hóa local để đọc được file. Không đưa file này vào Git. Tài khoản vẫn cần publisher/quyền Affiliate phù hợp; lưu cookie thành công không chứng minh schema hoặc tracking đã đạt nghiệm thu.

Nếu đăng nhập Shopee thủ công bằng CLI, dừng backend browser trước khi mở cùng profile. Không dùng profile Chrome cá nhân. Mọi cấu hình/bằng chứng live còn thiếu được ghi tại `docs/verification.md`.
