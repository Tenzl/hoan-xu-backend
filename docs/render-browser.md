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
3. Nếu muốn giữ phiên Chrome và file upload qua restart, thêm disk 5 GB,
   mount `/var/data`. Disk là tùy chọn trả phí và chỉ gắn vào một instance.
   Cấu hình mẫu `render.yaml` giữ disk; có thể bỏ khối `disk` trước khi tạo
   service mới nếu chấp nhận đăng nhập lại và mất file upload sau restart.
4. Import các giá trị trong `env.prod` vào Render Environment. File này được git
   ignore và không nằm trong Docker image. Giữ nguyên `DATA_ENCRYPTION_KEY` qua
   các lần deploy. Không tạo lại khóa khi đã có dữ liệu mã hóa.
5. Health check `/readyz`. Public port dùng biến `PORT` của Render (mặc định 10000).
   Chỉ API bind `0.0.0.0`; không mở các port 5900, 6080 hoặc Chrome debugging.
6. Chạy `/app/admin migrate` bằng kết nối migration có đủ quyền trước khi đưa
   API mới vào sử dụng. Giữ migration 10 trong lịch sử; API không còn dùng
   bảng `browser_credentials` để nạp hoặc lưu phiên Shopee.
   Có thể chạy `go run ./cmd/admin migrate` tại máy có cấu hình database đó.
   Không chạy migration tự động mỗi lần container khởi động.

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

1. Đăng nhập tài khoản **admin** trên frontend, vào **Đăng nhập Shopee**.
2. Nhập lại mật khẩu quản trị và chọn **Mở Chrome trên server**.
3. Trong Chrome từ xa, đăng nhập Shopee Affiliate và hoàn tất xác minh mà
   Shopee yêu cầu. Đây chính là Chrome API dùng để kiểm tra sản phẩm.
4. Quay lại quản trị, chọn **Tôi đã đăng nhập — Kiểm tra phiên**.
5. Nhập **Affiliate ID (Shopee Publisher)** vào ô cấu hình và chọn **Lưu Affiliate ID**.
   Mã được lưu trong database; các link mới dùng mã đã lưu. Không cần biến
   `SHOPEE_PUBLISHER` trong Render Environment.
6. Đóng cửa sổ điều khiển khi xong. Chrome tiếp tục chạy và dùng phiên vừa đăng nhập.

Backend không đọc/ghi cookie Shopee từ database hoặc file; chức năng dán cookie
đã bị tắt. Chrome chỉ mở khi admin yêu cầu. Sau restart, mở Chrome từ quản trị;
nếu không có disk, đăng nhập lại. Nếu có disk, Chrome có thể dùng phiên trong
profile, nhưng Shopee vẫn có thể yêu cầu xác minh lại. Affiliate ID được giữ trong
database dù không có disk.

`SHOPEE_TRACKING_VERIFIED` chỉ bật sau khi tracking affiliate thật đã được kiểm
chứng. Lưu Affiliate ID hoặc đăng nhập thành công chưa tự bật tracking.

Container restart làm mất vé và phiên điều khiển. Disk khiến deploy cần dừng
instance cũ trước khi instance mới chạy; có gián đoạn ngắn và màn hình đang mở
sẽ bị ngắt.

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
