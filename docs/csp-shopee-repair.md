# Hoàn thiện CSP và Shopee checker — 06/10/2026

## Nguyên nhân và phạm vi

Lỗi CSS đã tái hiện: `style-src` development yêu cầu nonce, nhưng Next.js
DevTools/HMR chèn style không có nonce. Frontend tách policy development và
production; xem `frontend/docs/csp.md` trong checkout frontend đi kèm.

Log 504 cũ cho thấy capture hết 20 giây trước khi gửi request sản phẩm, ở đường
điều hướng SPA/tab sẵn sàng. Log đó chưa chứng minh chính xác lệnh CDP nào treo.
Đường cũ gồm `GetFrameTree`, kiểm tra số thẻ `<a>`, `history.pushState/popstate`
đã được bỏ. Fixture mới gây treo thực tế bằng cách giữ lại một lệnh trên CDP
WebSocket; đã xác nhận deadline và thay riêng worker hoạt động.

Thay đổi này không chạy migration, sửa database, số dư, đơn hoặc chính sách
cashback. Không cài Docker/Redis và không triển khai production.

## Luồng checker

Nhận job → thuê một trong hai worker → listener theo job → điều hướng document
bình thường → đối chiếu request/loader → `LoadingFinished` → đọc/kiểm tra body
ngoài callback → trả tab. Tab đăng nhập, Chrome và profile được giữ riêng.
Mỗi listener kết thúc theo context job, không tồn tại qua lần dùng tiếp theo.

Request phải đúng host, `/api/v3/offer/product`, item ID và document/loader của
lượt điều hướng hiện tại. JSON kiểm tra định danh item nếu schema có cung cấp.
HTTP/network failure, redirect login/CAPTCHA và target bị đóng được xử lý ngay.
Một API phụ trả 403 không phải bằng chứng phiên đăng nhập hết hạn.

| Ngân sách | Giới hạn |
|---|---|
| HTTP checker | 45 giây |
| Resolve URL | 8 giây |
| Chờ queue, tối đa 50 job | 10 giây |
| Shared job | 40 giây, giới hạn theo deadline request tạo job |
| Mỗi capture, kể cả thuê tab | 20 giây |
| Tạo worker / lệnh đọc trạng thái / đọc body | 3 giây |
| Khởi động điều hướng | 5 giây |
| Body | 2 MB; kiểm tra khi tải và sau khi đọc |

Không chờ ảnh/analytics hoàn tất. Lỗi target/CDP thay tab rồi thử lại tối đa một
lần nếu còn ít nhất 5 giây. Không retry khi cần login/xác minh, bị giới hạn hoặc
upstream báo lỗi. Chỉ browser thực sự mất kết nối mới mất trạng thái toàn browser.
Cache TTL 600 giây, tối đa 1.000 mục, chỉ lưu kết quả đã normalize/xác minh; lỗi
và schema chưa xác minh không được cache. Cache key có publisher, shop/item,
phiên bản adapter và phiên browser. Singleflight vẫn gộp request trùng.

## Mã lỗi và xử lý

| HTTP / code | Ý nghĩa | Cách xử lý |
|---|---|---|
| 503 `SHOPEE_LOGIN_REQUIRED` | Cần đăng nhập Affiliate | Admin mở Chrome, đăng nhập rồi kiểm tra phiên |
| 503 `SHOPEE_VERIFICATION_REQUIRED` | Shopee yêu cầu xác minh | Admin tự hoàn tất CAPTCHA/2FA; không tự vượt xác minh |
| 503 `BROWSER_UNAVAILABLE` | Target/CDP/browser chưa sẵn sàng | Thử lại; admin xem phase và kiểm tra Chrome/kết nối |
| 502 `SHOPEE_RESPONSE_NOT_OBSERVED` | Trang mở nhưng không phát request sản phẩm | Thử lại; admin kiểm tra phiên/đường sản phẩm/schema |
| 502 `SHOPEE_UPSTREAM_FAILED` | Request sản phẩm lỗi mạng hoặc HTTP | Chờ Shopee hồi phục; 403 riêng lẻ không reset toàn phiên |
| 429 `SHOPEE_RATE_LIMITED` | Shopee giới hạn truy cập | Chờ trước khi thử; không retry vòng lặp |
| 504 `SHOPEE_TIMEOUT` | Đã gửi request nhưng chưa trả xong | Người dùng có thể thử lại |
| 502 `SHOPEE_RESPONSE_INVALID` | Body/schema/item không hợp lệ | Kiểm tra adapter và fixture; không suy diễn số tiền thiếu thành 0 |

`QUEUE_FULL`/`QUEUE_TIMEOUT` giữ mã cũ. Endpoint `/product-checks` và adapter
`/shopee/check` giữ request `{url}` và response thành công hiện hành.
Frontend giữ `ApiError.code`, hiện thông báo VI/EN và retry theo thao tác người
dùng. Debounce 500 ms, abort và chống response link cũ được giữ. Khoảng chia
vẫn lấy từ cấu hình hạng, ví dụ 65–75%; lỗi checker không tạo Xu hay số dư giả.

Admin có quyền `settings` xem `lastFailure: {code, phase, at}` hoặc `null` trong
browser status. Đây là lỗi gần nhất, vẫn giữ sau một lượt thành công để hỗ trợ
chẩn đoán; không đồng nghĩa browser hiện tại bị hỏng. Dữ liệu này không xuất
hiện trong API công khai. Log `shopee_browser_capture` có attempt, phase, code,
thời gian lease/navigation/response/body và tổng thời gian; log thay tab có lý
do. Không log URL đầy đủ, body, cookie, token hoặc thông tin tài khoản.

## Kiểm thử và nghiệm thu

Chạy từ thư mục backend bằng Go đã cài hoặc `.tools/go/bin/go.exe`:

```powershell
$env:BROWSER_TEST_PATH='C:/Program Files/Google/Chrome/Application/chrome.exe'
go run ./tests/run.go test -timeout 180s
go run ./tests/run.go vet
```

Fixture dùng profile tạm: CDP treo ở Navigate/Location/GetResponseBody, target
bị đóng, Chrome restart/mất kết nối, hai job đồng thời, hủy job, ảnh chậm,
response cũ/sai item, cùng item kiểm tra lại, không request, request treo, lỗi
mạng, 401/403/429/500, body sai/quá lớn, login/CAPTCHA. Normalizer giữ bộ 10
response thật đã được lược bỏ thông tin riêng trong repository.

Nghiệm thu thật đã đạt ngày 06/10/2026: 5 item
`25876970260`, `44378342379`, `25091975938`, `29429787328`, `41011259591`, mỗi
item hai lần, một cặp đồng thời, các lượt còn lại cách nhau tối thiểu 7 giây.
Cả 10 capture khớp tên/giá/hoa hồng đang hiển thị trên Shopee Affiliate và chỉ
khởi động Chrome một lần. Capture mất khoảng **2,8–8,7 giây**. So sánh tên
chuẩn hóa khoảng trắng để không coi khoảng trắng kép trong dữ liệu là sai tên.
Đây là xác minh checker, không phải bằng chứng attribution/đối soát affiliate.

Test thật là opt-in, không chạy trong CI. Trước khi chạy, bảo đảm profile Hoàn
Xu chưa bị một controller local khác giữ; không dùng profile Chrome cá nhân:

```powershell
$env:BROWSER_LIVE_PATH='đường dẫn Chromium trong .env.local'
$env:BROWSER_LIVE_PROFILE=(Resolve-Path private-data/chrome-profile).Path
go run ./tests/run.go test -run TestLiveShopeeAcceptance -v -timeout 180s
```

Thiếu login/xác minh hoặc upstream lỗi thì test báo `LIVE_ACCEPTANCE_BLOCKED`,
không chấp nhận fixture thay nghiệm thu thật. Test không tạo link, đơn hay giao
dịch. Sau kiểm thử, dùng `scripts/dev-local.ps1` cho backend và `npm run dev`
cho frontend. Local manual mode mở Chrome từ trang quản trị; sau backend
restart, admin mở Chrome và kiểm tra phiên lại, không cần lấy cookie mới nếu
profile vẫn có phiên hợp lệ. Không đóng Windows PostgreSQL service.
