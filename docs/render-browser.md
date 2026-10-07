# Go trên Render, Chromium trên EC2

Render chạy Go API, resolver, cache, singleflight, queue và chromedp. Chromium,
Xvfb, Openbox, noVNC và profile nằm riêng trên EC2. Source triển khai độc lập
nằm trong folder `chromium/` ở gốc repo backend. PostgreSQL giữ nguyên; không
cần migration cho thay đổi này.

## Chuẩn bị EC2 trước

Làm theo [README Chromium](../chromium/README.md): Docker Compose, EBS profile,
sandbox, user SSH giới hạn forwarding và pin host key. Deploy riêng folder
`chromium/` trong repo backend. Folder này không phụ thuộc Go/database và có
Docker build context riêng; `.dockerignore` của backend loại nó khỏi build API.
Folder `chromium/` bên cạnh repo trong workspace hiện tại là bản triển khai
local, chứa secrets riêng và không được push.

Giữ tối đa hai worker và một backend instance. Chromium chạy 24/7, có một tab dashboard
để đăng nhập; remote checker ưu tiên dùng lại tab Affiliate đang mở trong default
profile. Chỉ tạo thêm tab nếu chưa có tab phù hợp rảnh. Go không launch
hoặc kill Chrome trên EC2. Không tự động import/export cookie.

## Cấu hình Render

Giữ Docker runtime, `Dockerfile` và context `.` tại root repo backend. Image mới
chỉ có Go API/admin, CA certificates, SSH client và process supervisor; không còn
Chromium, Xvfb hay VNC. Health check tiếp tục `/readyz` cho API/database.

`render.yaml` dùng một instance `starter` làm điểm bắt đầu cho Go; kiểm tra RAM
thực tế trước khi giảm plan hiện tại. Giữ disk `/var/data` cho **file upload**;
profile Chrome đã chuyển sang EBS không có nghĩa file upload cũng được chuyển.
Giữ nguyên `DATABASE_URL`, `DATA_ENCRYPTION_KEY`, OAuth và `APP_ORIGIN` hiện có.
Không tái tạo encryption key. Migration vẫn chạy riêng bằng `/app/admin migrate`.

| Biến | Giá trị |
| --- | --- |
| `REMOTE_BROWSER_ENABLED` | `true` |
| `REMOTE_BROWSER_UPSTREAM` | `http://127.0.0.1:6080` |
| `REMOTE_BROWSER_ORIGIN` | Origin HTTPS backend Render, không có slash cuối |
| `REMOTE_BROWSER_BRIDGE_PASSWORD` | Secret giống EC2, 32+ ký tự URL-safe |
| `CHROME_SSH_TUNNEL_ENABLED` | `true` |
| `CHROME_SSH_HOST` | Elastic IP/hostname EC2 |
| `CHROME_SSH_USER` | `chrome-tunnel` |
| `CHROME_SSH_PORT` | `22` |
| `CHROME_SSH_PRIVATE_KEY` | Nội dung private key nhiều dòng, không có passphrase |
| `CHROME_SSH_KNOWN_HOSTS` | Nội dung known_hosts đã xác minh fingerprint |
| `PRIVATE_DIR` | `/var/data/files` |
| `COOKIE_SECURE` | `true` |

Chế độ Chrome `remote` và địa chỉ `http://127.0.0.1:9222` được lưu trong form
**Đăng nhập Shopee → Kết nối Shopee** cho origin frontend production. Trước khi
đưa API mới vào sử dụng, chạy migration 15 và chuyển cấu hình env cũ bằng
`/app/admin import-shopee-settings` một lần. Không đánh dấu đã xác minh từ env;
đăng nhập, kiểm tra GQL thật rồi bật tạo link trong quản trị. Xem
[luồng cấu hình đồng bộ](shopee-settings.md). SSH supervisor chạy theo
`CHROME_SSH_TUNNEL_ENABLED` độc lập với chế độ Chrome trong database.

File cấu hình production tổng là `env.prod` hiện có, nằm ngoài Git. Cập nhật
các biến trong bảng vào file này, giữ nguyên secrets database/encryption/OAuth
và các biến ứng dụng; không tạo thêm file env riêng cho Render. Khi import
Render Environment → Add from .env, dùng `env.prod` đã cập nhật. SSH private
key nằm trong giá trị quoted nhiều dòng của `CHROME_SSH_PRIVATE_KEY`; giữ
nguyên xuống dòng và xác nhận Render nhận đầy đủ giá trị trước khi deploy.
`CHROME_SSH_KNOWN_HOSTS` cũng cần quoted vì có khoảng trắng.

Bỏ các biến Chrome local cũ (`CHROME_PATH`, `CHROME_PROFILE`, `DISPLAY`,
`CHROME_HEADLESS`) khỏi file production và Render Environment để tránh nhầm
lẫn. File `.env`/`.env.local` cho development giữ cấu hình local riêng.
Frontend giữ `BACKEND_URL` nếu origin Go không đổi.

Nếu startup báo kết nối `/tmp/.s.PGSQL.5432`, kiểm tra `DATABASE_URL` trong
Environment của đúng Web Service: biến thiếu/rỗng khiến pgx dùng socket local.
File `env.prod` trên máy quản trị không được Git push hoặc Docker COPY vào
image, nên cần import vào Render và chọn Save and deploy sau khi kiểm tra các
giá trị. Upload file trong Secret Files không tự tạo Environment Variables.
Backend từ chối khởi động khi thiếu `DATABASE_URL` và báo lỗi cấu hình rõ ràng.

Entrypoint ghi SSH key vào thư mục tạm mode 700, file mode 600; không ghi key
vào disk upload và không chuyển biến chứa key cho process API. SSH dùng
`StrictHostKeyChecking=yes`, key riêng, keepalive và hai local forwards chỉ bind
loopback. Wrong host key bị từ chối, không tự cập nhật. Tunnel retry 2–30 giây;
API vẫn phục vụ khi tunnel/EC2 tạm mất kết nối.

CDP discovery lấy browser WebSocket ID mới mỗi lần reconnect và chỉ dùng origin
loopback đã cấu hình. Retry 5–30 giây; phiên phải được probe trước khi checker
hoạt động lại. Probe chạy độc quyền, chờ worker rảnh. Lỗi browser hiện trong
trang admin; `/readyz` không restart API chỉ vì Shopee/EC2 không sẵn sàng.

## Đăng nhập và nghiệm thu

1. Admin xác nhận mật khẩu, mở **Đăng nhập Shopee → Mở Chrome trên server**.
2. Đăng nhập Affiliate, hoàn tất xác minh bằng tay trên tab dashboard EC2.
3. Chọn **Tôi đã đăng nhập — Kiểm tra phiên**, lưu Affiliate ID và thử sản phẩm.
4. Restart Render: Chrome EC2 tiếp tục chạy; vé điều khiển cũ hết hiệu lực,
   admin mở màn hình lại. Restart Chromium: Go tự kết nối lại và probe phiên.
5. Kiểm tra từ mạng bên ngoài: 9222/6080/5900 không truy cập được; staff/customer
   không được mở màn hình. Profile tồn tại không bảo đảm Shopee luôn chấp nhận
   phiên. Tracking phải kiểm chứng riêng trước khi bật `SHOPEE_TRACKING_VERIFIED`.

Vé điều khiển một lần 60 giây, phiên màn hình 10 phút, HttpOnly/Secure/SameSite
Strict, kiểm tra Origin và thu hồi theo session admin vẫn giữ nguyên. Proxy
không gửi Cookie/Authorization của user sang EC2; nó dùng bridge password riêng.

## Đo thời gian lấy hoa hồng

Checker trả JSON khi response `/api/v3/offer/product` tải xong, không chờ
toàn bộ trang (ảnh, analytics...) phát sự kiện `load`. Remote mode không tạo tab
điều khiển trắng hoặc tải sẵn thêm tab khi chưa cần. Checker ưu tiên tab product
offer đang mở, sau đó dashboard, rồi các trang Affiliate khác trong default
profile. Các tab ngoài Affiliate hoặc thuộc context ẩn danh không được mượn.
Hai worker giữ lease độc quyền, nên không điều hướng cùng một tab đồng thời.
Probe chờ worker rảnh và dùng lại tab đó. Shopee vẫn tự gọi API và tạo security context.

Lượt thành công trả tab vào pool. Tab có sẵn được giữ lại khi lỗi, hủy, timeout
hoặc chuyển sang đăng nhập/xác minh. Tab do Go tạo bị lỗi được đóng và thay thế.
Go shutdown đóng các tab do Go tạo, ngắt CDP trước khi giải phóng tab mượn,
giữ Chromium và các tab có sẵn. Khi một tab bị đóng bằng tay, checker thay lease
đã chết khi thử lại. Mỗi lượt kiểm tra điều hướng tài liệu mới trong cùng tab để nhận
response mới; response cũ bắt đầu trước lượt kiểm tra không được chấp nhận.
Redirect đăng nhập/xác minh (kể cả trong SPA) và API 401/403 vẫn làm phiên hết
hiệu lực. Chrome local vẫn giữ tab đăng nhập/xác minh riêng và hai tab worker.

Khởi động/reconnect/kiểm tra phiên cần probe thành công trước khi trạng thái
browser thành authenticated. Remote không tạo worker thứ hai chỉ để preload.
API/database readiness độc lập với việc này.
Cache 10 phút và singleflight giữ nguyên, cache bị đổi khi phiên thay đổi.

Render Logs có hai bản ghi chỉ chứa thời gian và trạng thái, không chứa URL,
cookie, dữ liệu sản phẩm hoặc tài khoản:

- `shopee_check_completed`: `resolve_ms`, `check_wait_ms`, `total_ms`,
  `cache_hit`, `shared`, `success`. `check_wait_ms` bao gồm chờ queue, browser
  và xử lý kết quả; so sánh với thời gian capture để xác định chờ worker.
- `shopee_browser_capture`: `lease_ms`, `navigation_ms`,
  `product_request_after_ms`, `product_headers_after_ms`, `product_ready_after_ms`,
  `body_ms`, `total_ms`, `success`. Các trường `*_after_ms` tính từ đầu lượt capture;
  `-1` nghĩa là chưa quan sát được mốc tương ứng. `lease_ms` gồm chờ và gắn tab;
  `navigation_ms` là thời gian gửi lệnh điều hướng.

Nếu request sản phẩm bắt đầu muộn nhưng phần chờ response thấp, thời gian
nằm ở mở tab/khởi chạy trang, chưa đủ bằng chứng thiếu RAM. Đo trên Render khi
check sản phẩm mới và khi hai worker cùng chạy, đối chiếu RAM/CPU EC2 trước
khi đổi instance. Đo qua tunnel máy quản trị còn bao gồm độ trễ từ máy đó,
không dùng để kết luận độ trễ Render → EC2.

## Development, kiểm thử và rollback

Development chọn Chrome local, đường dẫn và profile trong form **Kết nối Shopee**;
Chrome luôn có cửa sổ. Chạy `scripts/dev.ps1` hoặc `go run ./cmd/api` như
trước. Local không kết nối EC2: mở Chrome từ admin, đăng nhập rồi kiểm tra phiên
để tải sẵn hai tab worker. Các sản phẩm được điều hướng trong tab đã tải sẵn;
kiểm tra lại cùng sản phẩm vẫn tải tài liệu mới để lấy response mới.
Image production mới không có Chrome local. Khi cần rollback deployment gộp,
dùng image/backend revision cũ và giữ nguyên disk/profile cũ trước khi chuyển đổi.

Thử remote từ máy developer bằng tunnel tự quản lý:

```bash
ssh -N -T -i render-chromium -o StrictHostKeyChecking=yes \
  -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 \
  -L 127.0.0.1:9222:127.0.0.1:9222 \
  -L 127.0.0.1:6080:127.0.0.1:6080 chrome-tunnel@EC2_ELASTIC_IP
```

Chọn Chrome từ xa và địa chỉ `http://127.0.0.1:9222` trong form quản trị;
`go run` không tự launch SSH.
Muốn mở màn hình thì bật `REMOTE_BROWSER_ENABLED`, đặt origin backend localhost
và bridge secret. `CHROME_SSH_TUNNEL_ENABLED=false` chỉ dùng với Docker test hoặc
tunnel được quản lý bên ngoài; không trỏ CDP ra địa chỉ public.

```powershell
$env:BROWSER_TEST_PATH=(Get-ChildItem "$env:LOCALAPPDATA/ms-playwright/chromium-*/chrome-win64/chrome.exe" -File | Sort-Object LastWriteTime -Descending | Select-Object -First 1).FullName
go run ./tests/run.go
go run ./tests/run.go vet
go build ./cmd/api ./cmd/admin
```

Test lifecycle dùng server giả lập, hai worker, CDP qua proxy loopback và browser
restart; không đăng nhập Shopee hoặc truy cập database production.
Docker smoke test tại `tests/deployment/docker-smoke.ps1` dựng PostgreSQL,
Chromium và SSH fixture riêng, kiểm tra tunnel thật và viewer. Chạy trên Docker
daemon sẵn sàng; AppArmor production cần kiểm chứng trên Ubuntu EC2 riêng.

Nguồn: [chromedp](https://github.com/chromedp/chromedp),
[Render outbound IPs](https://render.com/docs/outbound-ip-addresses),
[Render disk](https://render.com/docs/disks),
[OpenSSH](https://man.openbsd.org/ssh_config).
