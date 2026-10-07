# Chromium trên EC2

Nguồn canonical là `backend/chromium/`. Bản `chromium/` ở gốc workspace được xuất bằng `backend/scripts/export-chromium.ps1`; sửa nguồn rồi xuất lại. Script chỉ copy các file công khai, giữ secrets và profile tại đích.

Folder `chromium/` ở gốc repo backend này triển khai độc lập: chỉ Chromium, màn hình ảo và noVNC. Go API và
chromedp tiếp tục chạy trên Render; PostgreSQL giữ ở dịch vụ hiện tại.

```text
Render Go → 127.0.0.1:9222 → SSH → EC2 127.0.0.1:9222 → Chromium
Admin → Render /browser → 127.0.0.1:6080 → SSH → EC2 noVNC
```

## 1. Chuẩn bị EC2

Ubuntu Server 24.04 LTS, x86_64, Singapore; bắt đầu `t3.small`, EBS gp3 20 GB
mã hóa, Elastic IP. Bật Docker service lúc boot. Không cài Go/database trên EC2.
T3 dùng CPU credits: theo dõi credit balance/surplus, RAM và thời gian kiểm tra
trước khi tăng số tab hoặc đổi máy. Hai worker là điểm bắt đầu, không phải cam
kết công suất. Elastic IP và EBS có phí riêng.

Cài Docker Engine và Compose plugin theo [tài liệu Docker Ubuntu](https://docs.docker.com/engine/install/ubuntu/).
Copy riêng folder này đến `/opt/hoanxu/chromium` trên EC2.

Security Group: chỉ TCP 22 từ dải outbound hiển thị trong Dashboard của Render
và IP quản trị. Không thêm inbound 9222, 6080, 5900. Dải outbound Render có thể
được chia sẻ với service khác: SSH key riêng vẫn bắt buộc.

Không gắn IAM role có quyền ứng dụng cho browser host. Dùng IMDSv2 nếu cần
metadata. Đây là một browser có phiên tài khoản, vì vậy không dùng nó để mở các
URL tùy ý ngoài luồng vận hành.

## 2. Sandbox, profile và khởi động

```bash
cd /opt/hoanxu/chromium
sudo install -d -m 700 -o 10001 -g 10001 /var/lib/shopee-chrome
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

Điền chuỗi ngẫu nhiên vừa tạo vào `REMOTE_BROWSER_BRIDGE_PASSWORD` trong `.env`;
đặt đúng cùng giá trị trên Render. Giữ `.env` ngoài Git, không paste vào log.

Chromium chạy dưới UID 10001, không dùng `--no-sandbox` hoặc `--privileged`.
Compose cho phép syscall namespace qua `seccomp=unconfined` để Chrome dùng OS
sandbox. Đây là việc nới seccomp ở lớp container; không phải cấu hình sandbox
mặc định của Docker. Ubuntu 24.04 còn chặn user namespaces qua AppArmor, nên
load profile riêng có quyền `userns`:

```bash
sudo install -m 644 apparmor/hoanxu-chromium /etc/apparmor.d/hoanxu-chromium
sudo apparmor_parser -r /etc/apparmor.d/hoanxu-chromium
sudo systemctl enable --now docker
sudo docker compose up -d --build
sudo docker compose ps
curl --fail http://127.0.0.1:9222/json/version
sudo ss -lntp
```

Kiểm tra Chrome qua màn hình admin, vào `chrome://sandbox` để xác nhận sandbox
hoạt động trước khi đăng nhập thật. Nếu lỗi `No usable sandbox` hoặc userns,
kiểm tra AppArmor, `journalctl -k`, Docker/kernel; không sửa bằng cách thêm
`--no-sandbox`. Profile AppArmor mẫu chỉ cho phép userns, không cung cấp giới
hạn filesystem bổ sung. Không tắt AppArmor/userns restrictions toàn máy.

Chrome tự mở dashboard Affiliate trong một tab đăng nhập riêng. Go tạo tab
controller/checker trong cùng default browser context; không tạo incognito.
Profile nằm trên EBS; một supervisor giữ lock để tránh hai container dùng chung
profile. Không xóa `SingletonLock` khi còn process Chrome chạy.

Compose tự restart khi supervisor phát hiện process chính chết. HEALTHCHECK
đánh dấu `unhealthy` khi CDP/display không trả lời; Docker **không** tự restart
chỉ vì unhealthy. Theo dõi trạng thái này và dùng `sudo docker compose restart`
sau khi kiểm tra lỗi. Logs được xoay vòng.

## 3. User SSH chỉ dùng tunnel

Tạo key riêng trên máy quản trị; private key không có passphrase để supervisor
Render sử dụng không tương tác. Không dùng key đăng nhập quản trị EC2.

```bash
ssh-keygen -t ed25519 -f render-chromium -N '' -C render-chromium
```

Trên EC2, bằng user quản trị:

```bash
sudo useradd --create-home --shell /usr/sbin/nologin chrome-tunnel
sudo install -d -m 700 -o chrome-tunnel -g chrome-tunnel /home/chrome-tunnel/.ssh
sudo install -m 600 -o chrome-tunnel -g chrome-tunnel /dev/null /home/chrome-tunnel/.ssh/authorized_keys
```

Điền **public key** vào dòng mẫu `ssh/authorized_keys.example`, giữ nguyên
`restrict,port-forwarding,permitopen`, rồi ghi dòng đó vào `authorized_keys`.
Copy cấu hình và kiểm tra trước khi reload; giữ phiên SSH quản trị hiện tại:

```bash
sudo install -m 644 ssh/sshd_config.conf /etc/ssh/sshd_config.d/60-chrome-tunnel.conf
sudo sshd -t
sudo systemctl reload ssh
sudo sshd -T -C user=chrome-tunnel,host=localhost,addr=127.0.0.1 | \
  grep -E 'allowtcpforwarding|permitopen|maxsessions|passwordauthentication'
```

`MaxSessions 0` cấm shell/exec/subsystem, vẫn cho phép `ssh -N` forwarding.
User chỉ được forward TCP đến 9222 và 6080; không forward Unix socket, agent,
X11 hoặc remote port. Nếu AMI/PAM từ chối user đã khóa, kiểm tra auth log và
chính sách account; không bật password login để sửa tunnel.

Pin host key từ một kênh đã xác thực (SSH quản trị hoặc console EC2):

```bash
sudo cat /etc/ssh/ssh_host_ed25519_key.pub
sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

Tạo known_hosts: `EC2_ELASTIC_IP ssh-ed25519 PUBLIC_HOST_KEY`. Với SSH port khác
22, host phải có dạng `[EC2_ELASTIC_IP]:PORT`. Không tự tin cậy kết quả
`ssh-keyscan` chưa đối chiếu fingerprint, không dùng `StrictHostKeyChecking=no`.

## 4. Render và đăng nhập Shopee

Xem [hướng dẫn backend](../backend/docs/render-browser.md). Render cần:

| Biến | Giá trị |
| --- | --- |
| `BROWSER_MODE` | `remote` |
| `CHROME_REMOTE_URL` | `http://127.0.0.1:9222` |
| `REMOTE_BROWSER_ENABLED` | `true` |
| `REMOTE_BROWSER_UPSTREAM` | `http://127.0.0.1:6080` |
| `REMOTE_BROWSER_ORIGIN` | Origin HTTPS backend Render |
| `REMOTE_BROWSER_BRIDGE_PASSWORD` | Cùng secret với EC2 |
| `CHROME_SSH_TUNNEL_ENABLED` | `true` |
| `CHROME_SSH_HOST` | Elastic IP/hostname EC2 |
| `CHROME_SSH_USER` | `chrome-tunnel` |
| `CHROME_SSH_PORT` | `22` |
| `CHROME_SSH_PRIVATE_KEY` | Toàn bộ private key nhiều dòng |
| `CHROME_SSH_KNOWN_HOSTS` | Nội dung known_hosts đã xác minh |

Không copy `CHROME_PATH`, `CHROME_PROFILE`, `DISPLAY` từ deployment Render cũ.
Mở **Đăng nhập Shopee → Mở Chrome trên server**, xác nhận mật khẩu admin, đăng
nhập và xử lý xác minh bằng tay. Chọn **Tôi đã đăng nhập — Kiểm tra phiên** rồi
thử sản phẩm thật. Đóng màn hình không tắt Chrome. Restart Go không tắt Chrome;
restart EC2 giữ profile nhưng Shopee vẫn có thể yêu cầu đăng nhập lại.

Không copy profile Windows sang Linux hoặc dùng cookie dump để chuyển phiên.
Đăng nhập lại trên EC2 để xác minh IP/môi trường mới. Giữ
`SHOPEE_TRACKING_VERIFIED=false` cho đến khi kiểm chứng tracking riêng.

## 5. Backup, cập nhật và phục hồi

Backup là dữ liệu nhạy cảm. Dừng Chromium trước khi copy profile; mã hóa backup,
giới hạn truy cập và giữ quyền sở hữu UID 10001 khi restore.

```bash
cd /opt/hoanxu/chromium
sudo docker compose stop
sudo tar -C /var/lib -czf /secure-backups/shopee-chrome.tar.gz shopee-chrome
sudo chmod 600 /secure-backups/shopee-chrome.tar.gz
sudo docker compose start
```

`/secure-backups` cần được tạo trên storage quản trị được bảo vệ trước. Snapshot
EBS lấy khi browser dừng là lựa chọn khác. Restore khi container đã dừng; kiểm
tra nội dung archive trước khi extract, sửa owner rồi start. Không chạy hai bản
browser từ cùng một profile backup đồng thời.

Cập nhật: backup trước, `sudo docker compose build --pull --no-cache`,
`sudo docker compose up -d`, xác nhận health/sandbox, kiểm tra phiên và sản phẩm.
Giữ image/profile backup của phiên bản trước; rollback Chrome cũ có thể cần
khôi phục profile cùng phiên bản thay vì đọc profile đã được bản mới nâng cấp.

Mất tunnel: Go giữ API/database hoạt động, checker báo unavailable. SSH retry
2–30 giây; CDP retry 5–30 giây, rediscover endpoint rồi probe phiên trước khi
nhận job. Dùng `sudo docker compose logs --tail=100` và log Render để chẩn đoán,
không gửi secrets hoặc profile ra ngoài.

Nghiệm thu: 9222/6080/5900 không truy cập được từ Internet; key sai và host key
sai bị từ chối; admin mở màn hình được; restart Render không restart Chrome;
restart EC2 giữ profile và kết nối lại; thử hai sản phẩm khác nhau đồng thời.

Nguồn: [OpenSSH client](https://man.openbsd.org/ssh_config),
[OpenSSH server](https://man.openbsd.org/sshd_config),
[Docker seccomp](https://docs.docker.com/engine/security/seccomp/),
[Ubuntu AppArmor user namespaces](https://ubuntu.com/blog/ubuntu-23-10-restricted-unprivileged-user-namespaces),
[chromedp RemoteAllocator](https://github.com/chromedp/chromedp).
