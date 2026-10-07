# Cấu hình Shopee đồng bộ trong quản trị

Trong trang **Đăng nhập Shopee**, dùng một form **Kết nối Shopee** cho Affiliate ID,
Chrome và bật/tắt tạo link. Mở **Nâng cao** khi cần đổi đường dẫn/profile, địa chỉ
loopback hoặc hệ số đơn vị giá (mặc định 100000). Chrome luôn có cửa sổ đăng nhập.

Lưu cấu hình trước, mở Chrome đăng nhập, kiểm tra phiên và nhập một link sản phẩm
có hoa hồng để **Kiểm tra sản phẩm và tracking**. Tác vụ tối đa 90 giây kiểm tra
phiên, dữ liệu sản phẩm và native GQL đủ năm Sub_id. Kết quả đúng cấu hình hiện
hành mới cho phép bật **Cho phép khách tạo link**. Mã chẩn đoán `hxverify` không
thuộc khách Google, nên không dùng để ghi nhận hoàn Xu. Tạo link kiểm tra không
lưu affiliate_links/orders/wallet; bảng riêng shopee_verifications lưu tiến độ.

API GET/PUT `/admin/browser/settings` lưu Affiliate ID và cấu hình trong một
transaction, dùng version chống ghi đè giữa các tab. Chỉ admin, CSRF và xác thực
mật khẩu trong 15 phút được ghi. Kết quả xác minh là dữ liệu server, không nhận
checkbox xác nhận từ client. API ghi publisher/channel cũ trả 410. Đổi cấu hình
trong khi kiểm tra không chấp nhận kết quả cũ. Backend restart hủy tác vụ chưa
xong; yêu cầu kiểm tra lại. POST `/admin/browser/verifications` và GET theo ID
chỉ admin truy cập, chỉ một tác vụ đang chạy cho mỗi origin.

Affiliate ID dùng chung trong affiliate_channels.settings. runtimeConfigs được
phân biệt theo hash APP_ORIGIN; đổi APP_ORIGIN là cấu hình ứng dụng khác và cần
cấu hình/xác minh riêng. Bật/tắt và trạng thái kênh API dùng cùng dữ liệu runtime;
status cũ trong bảng chỉ phục vụ tương thích lịch sử. Đổi Affiliate ID dùng chung
làm kết quả xác minh ở các origin khác mất hiệu lực. Backend kiểm tra lại cấu hình
ở các thao tác Shopee để nhận thay đổi của origin khác. SSH/noVNC giữ env triển khai.

## Chuyển đổi

1. Dừng backend và sao lưu schema hoanxu, file env hiện dùng cùng binary cũ trong
   private-data/backups (không đưa vào Git).
2. Chạy migration 15 bằng admin migrate. Migration chỉ thêm bảng tác vụ kiểm tra,
   RLS và index; không sửa link, đơn, khách hoặc ledger cũ.
3. Chạy admin import-shopee-settings với ENV_FILE trỏ env cũ, một lần cho mỗi
   origin/database. Lệnh từ chối ghi đè cấu hình đã tồn tại. Trạng thái xác minh
   cũ không được tin cậy; cấu hình nhập vào chưa bật tạo link.
4. Mở Chrome và xác minh bằng trang quản trị. Khi cần kiểm tra vận hành từ máy
   backend, dừng browser do backend quản lý rồi dùng:
   `admin verify-shopee-settings --product-url https://shopee.vn/product/83496725/6939920023`.
   Thêm `--enable` chỉ khi cần bật tạo link sau khi kiểm tra thật thành công.
   Lệnh dùng cùng kiểm tra, chữ ký và proof như API; không ghi dữ liệu tài chính.
5. Bỏ SHOPEE_ENABLED/TRACKING_VERIFIED/SCHEMA_VERIFIED/PRICE_SCALE và
   BROWSER_MODE/CHROME_PATH/PROFILE/REMOTE_URL/HEADLESS khỏi env đã chuyển.
   Các biến này chỉ còn được đọc bởi lệnh nhập một lần, không dùng khi chạy API.
6. Khởi động backend mới, kiểm tra healthz/readyz, cấu hình còn sau restart và
   đối chiếu toàn bộ link, đơn, khách, số dư/entries/transactions trước/sau.

Rollback bằng binary và env đã sao lưu; giữ migration cộng thêm, không xóa dữ liệu.
Chỉ phục hồi cấu hình đã sao lưu khi cần; không restore toàn bộ schema lên dữ liệu
mới của khách. Với Render, cấu hình origin production phải được nhập trước khi
đưa bản API mới vào sử dụng. Chuyển cấu hình không tự deploy lên Render.

## Chuyển đổi local ngày 07/10/2026

Đã sao lưu schema, cấu hình và binary cũ tại
`private-data/backups/shopee-settings-20261007-015127`, áp dụng migration 15
(clean), nhập cấu hình local và production thành hai origin độc lập. GQL thật
đã xác minh thành công dữ liệu sản phẩm và đủ năm Sub_id với token v2 49 ký tự,
hệ số `0p63`; local được bật sau xác minh. Production được nhập ở trạng thái
chưa bật, cần kiểm tra Chrome từ xa trước khi dùng bản API mới trên Render.

Backend local mới đã khởi động, healthz/readyz thành công. Đối chiếu trước/sau
khớp 6.752 link, 6.749 đơn, 103 khách, 409 tài khoản ví, 13.500 ledger entries
và 6.750 giao dịch ví. Các biến Shopee/Chrome cũ đã bỏ khỏi ba file env riêng;
mọi cấu hình khác, gồm SSH/noVNC, được giữ nguyên. Không deploy Render tự động.
