# Backend và Chrome trên Render

Một Render **Web Service**, runtime **Docker**, chạy API Go, Chromium có giao diện
(`CHROME_HEADLESS=false`), Xvfb, Openbox và noVNC/websockify. Frontend tiếp tục ở
Vercel; PostgreSQL tiếp tục dùng Supabase. Không cần worker hoặc laptop chạy nền.

## Cấu hình Web Service đang có

1. Dùng repository `Tenzl/hoan-xu-backend`, branch `main`, root directory để trống.
   Vào **Settings → Build → Source → Edit**, giữ repo/branch và chuyển runtime
   sang **Docker**; Dockerfile path `./Dockerfile`,
   Docker build context `.`. Bỏ lệnh build/start Go cũ và để Docker dùng entrypoint.
2. Dùng một instance. Cấu hình mẫu `render.yaml` dùng **1 CPU / 2 GB RAM**
   (`1c-2g`) cho Go và Chrome; đây là cấu hình trả phí, cần xem giá trên Render
   trước khi áp dụng. RAM 512 MB có thể không đủ cho Chrome và API chạy cùng nhau.
3. Thêm persistent disk 5 GB, mount `/var/data` để giữ profile Chrome, cookie mã hóa
   và file riêng. Disk chỉ gắn vào một instance; không bật autoscaling.
4. Import các giá trị trong `env.prod` vào Render Environment. File này được git
   ignore và không nằm trong Docker image. Giữ nguyên `DATA_ENCRYPTION_KEY` qua
   các lần deploy. Không tạo lại khóa khi đã có dữ liệu mã hóa.
5. Health check `/readyz`. Public port dùng biến `PORT` của Render (mặc định 10000).
   Chỉ API bind `0.0.0.0`; không mở các port 5900, 6080 hoặc Chrome debugging.
6. Nếu database chưa có schema, chạy `/app/admin migrate` bằng kết nối migration
   trong môi trường có đủ quyền, trước khi đưa API mới vào sử dụng. Không chạy
   migration tự động mỗi lần container khởi động. Database Supabase hiện tại
   đã được chuẩn bị; không cần tạo database mới.

Các biến dành cho container:

| Biến | Giá trị |
| --- | --- |
| `CHROME_PATH` | `/app/deploy/chromium.sh` |
| `CHROME_HEADLESS` | `false` |
| `DISPLAY` | `:99` |
| `CHROME_PROFILE` | `/var/data/chrome-profile` |
| `PRIVATE_DIR` | `/var/data/files` |
| `REMOTE_BROWSER_ENABLED` | `true` |
| `REMOTE_BROWSER_ORIGIN` | `https://hoan-xu-backend.onrender.com` |
| `APP_ORIGIN` | `https://hoan-xu.vercel.app` |
| `COOKIE_SECURE` | `true` |

`REMOTE_BROWSER_ORIGIN` phải trùng origin URL backend thật, không có dấu `/` cuối.
Frontend dùng `BACKEND_URL` cùng backend đó và cần build lại trên Vercel nếu đổi
giá trị. WebSocket màn hình mở trực tiếp tại backend, không đi qua rewrite Vercel.
Nếu dùng custom domain backend, đổi `REMOTE_BROWSER_ORIGIN` theo domain mới.

## Đăng nhập và xác minh Shopee

1. Đăng nhập tài khoản **admin** trên frontend, vào **Cài đặt cookie**.
2. Nhập lại mật khẩu quản trị và chọn **Mở Chrome trên server**.
3. Trong cửa sổ Chrome từ xa, đăng nhập Shopee Affiliate và hoàn tất kiểm tra
   Shopee yêu cầu. Đây chính là Chrome mà API dùng để kiểm tra sản phẩm.
4. Quay lại trang quản trị, chọn **Kiểm tra phiên hiện có**. Không cần xuất cookie
   từ Chrome cá nhân nếu đăng nhập trực tiếp trên server. Khi phiên hợp lệ,
   backend tự lưu cookie hiện tại dưới dạng mã hóa tại
   `/var/data/shopee-cookies.enc`; những lần kiểm tra định kỳ cũng cập nhật file
   này. Mỗi lần Chrome mới khởi động sau restart sẽ đọc và áp dụng cookie đã lưu.
5. Đóng phiên điều khiển khi xong. Profile vẫn lưu trên disk; Chrome vẫn chạy.

`SHOPEE_ENABLED=false` ban đầu không ngăn admin mở Chrome để đăng nhập. Chỉ bật
kiểm tra sản phẩm sau khi xác minh phiên hoạt động. `SHOPEE_TRACKING_VERIFIED` và
`SHOPEE_PUBLISHER` chỉ bật/điền khi tracking affiliate thật đã được kiểm chứng.
Không bật tracking chỉ vì đăng nhập thành công.

Shopee vẫn có thể yêu cầu xác minh lại hoặc từ chối IP của Render. Chạy có giao
diện không bảo đảm hết kiểm tra; giao diện từ xa giúp xử lý thủ công khi sàn cho
phép. Container restart làm mất vé/phiên điều khiển, nhưng dữ liệu trên disk còn.
Disk khiến deploy cần dừng instance cũ trước khi instance mới chạy; có gián đoạn
ngắn và kết nối màn hình đang mở sẽ bị ngắt.

## Bảo vệ màn hình và vận hành

- Vé một lần hết hạn sau 60 giây, nằm trong fragment URL, không trong access log.
- Phiên màn hình tối đa 10 phút, dùng cookie HttpOnly/Secure/SameSite Strict.
- Tài khoản phải là admin và đã xác nhận mật khẩu gần đây. Staff có quyền
  settings cũng không được điều khiển Chrome. Mỗi lần mở được ghi audit, không
  ghi cookie, mật khẩu hoặc vé truy cập.
- Mọi tài nguyên màn hình kiểm tra phiên admin gốc; WebSocket kiểm tra Origin và
  kiểm tra lại phiên mỗi 15 giây. Đăng xuất/khóa tài khoản thu hồi quyền điều khiển.
- VNC và websockify chỉ nghe loopback. Proxy không chuyển cookie đăng nhập hoặc
  Authorization của người dùng sang websockify. Proxy dùng một mật khẩu bridge
  ngẫu nhiên riêng, tạo lại khi container khởi động, để trang web bên trong Chrome
  cũng không thể tự kết nối WebSocket loopback. Không có trang VNC công khai.
- Entry point sửa quyền thư mục disk rồi chạy **toàn bộ tiến trình bằng UID
  10001**. Wrapper Chromium tắt OS sandbox vì môi trường container không cấp
  user namespace cần thiết; Chrome vẫn chạy dưới user không có quyền root.
- Khi một tiến trình chính dừng, container dừng để Render khởi động lại.

## Kiểm tra trước khi deploy

```powershell
docker build -t hoanxu-backend:browser .
docker run --rm --name hoanxu-browser --env-file env.prod -p 10000:10000 -v hoanxu-private:/var/data hoanxu-backend:browser
```

Để mở màn hình khi thử Docker trên máy mình, override
`REMOTE_BROWSER_ORIGIN=http://localhost:10000`. Nếu dùng frontend local, override
`APP_ORIGIN=http://localhost:3000` và `COOKIE_SECURE=false`, rồi trỏ proxy frontend
về `http://localhost:10000`. Không đổi các giá trị này trên production.

Đường dẫn `/browser/screen` và `/browser/view/websockify` chưa xác thực phải trả
401. `/readyz` trả 200; Chrome xuất hiện khi admin mở màn hình. Sau restart,
đăng nhập admin và mở lại màn hình để kiểm tra profile Shopee đã giữ trên disk.

Test tự động với database/container riêng, không dùng Supabase:

```powershell
./tests/deployment/docker-smoke.ps1
# Thêm kiểm tra canvas noVNC trên desktop/mobile bằng Playwright của frontend:
./tests/deployment/docker-smoke.ps1 -ViewerTest ../frontend/tests/deployment/remote-viewer.mjs
```

Test kiểm tra profile còn trên volume sau restart và mở lại được Chrome. Không
thực hiện đăng nhập Shopee thật; việc sàn chấp nhận phiên/IP phải kiểm tra sau
deploy trên Render.

Nguồn: [Docker trên Render](https://render.com/docs/docker),
[Web Service](https://render.com/docs/web-services),
[Persistent disk](https://render.com/docs/disks),
[WebSocket](https://render.com/docs/websocket),
[Blueprint](https://render.com/docs/blueprint-spec),
[noVNC](https://novnc.com/info.html).
