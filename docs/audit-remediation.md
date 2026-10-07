# Vận hành sau đánh giá — 07/10/2026

Thực hiện local; không migrate production trong đợt này.

## Database

Go và PowerShell ưu tiên `.env.local`; `ENV_FILE` chọn profile tường minh. Development chỉ nhận database loopback. Test yêu cầu thêm tên `_test`. Migrate production yêu cầu `ENV_FILE` và `--allow-production`.

`admin clone-local --pg-bin <thư mục PostgreSQL> --destination <backup mới>` đọc nguồn từ MIGRATION_DATABASE_URL/DATABASE_URL và đích từ LOCAL_DATABASE_URL. Dump trong snapshot nhất quán, chỉ đọc nguồn; đích phải là database mới ở loopback, tên hoanxu_local hoặc hoanxu_local_*. Backup, manifests và profile được ghi trong private-data. Giữ khóa mã hóa nguồn; reset sessions/jobs đang chạy và cấu hình origin/browser cho local. Nếu có private_files, cần copy các file tương ứng riêng.

Sau migrate chạy `admin check-local-history --baseline <backup đã kiểm chứng>`. Lệnh đọc local, so digest 22 bảng lịch sử/tài chính, kiểm ledger và file thiếu; không tính lại số dư. Metadata mới của orders/links được loại khỏi phép so lịch sử. `verify-local` chỉ dành cho bản restore trước migration, có cập nhật credential local; không dùng trên runtime đang chạy.

Bản clone: hoanxu_local_20261007_02, PostgreSQL nguồn 17 → local 18. 40 bảng khớp trước migration, local lên version 21 sạch. Sau migration: 22 bảng lịch sử khớp, ledger sai lệch 0, file thiếu 0. Metadata NOT NULL mới của PG18 và nhóm ngoặc CHECK tương đương được chuẩn hóa khi so schema; manifest gốc được giữ.

## Tracking và import

SubID3 giữ sản phẩm, policy version/hạng, tỷ lệ chính, thời điểm và nonce; SubID4 giữ hệ số hiệu lực (`0p63` = `0.63`). SubID5 ký publisher và SubID1–4. Không có số Xu cố định trong token. Tiền hoàn dùng hoa hồng gốc của đơn và hệ số đã chốt, làm tròn lên; không random lại khi import.

Registry giữ publisher hiện tại và lịch sử. Import xác minh publisher/signature, sản phẩm và thời điểm đơn. Source identity chống tạo đơn/credit lặp. Preview insert lô 1.000 dòng, apply dùng một kết quả attribution mỗi dòng, index partial phục vụ valid rows.

Import giữ quyết định từ chối nội bộ. Action `reopened` yêu cầu quyền review và lý do 3–500 ký tự, chỉ về pending và có audit, không ghi có ví. Đơn Shopee hủy không được mở lại. Seed tự sinh chỉ apply vào môi trường test/database _test; dữ liệu cũ được giữ.

## Link và file

Tạo link yêu cầu Idempotency-Key 8–128 ký tự. Cùng user/key/body trả cùng kết quả; đổi body trả 409. Caller mất kết nối không hủy công việc; service lifetime hoặc deadline 55 giây có thể hủy. Link và operation response commit cùng transaction. Lỗi Shopee không lưu link snapshot. Timeout trả LINK_CREATION_UNCERTAIN: kiểm tra danh sách link trước yêu cầu mới. Running quá 2 phút thành indeterminate, không tự random lại.

CSV giới hạn 10 MB, ghi file trực tiếp để tránh giữ thêm bản raw trong RAM. Preview lỗi dọn staged file. Cleanup theo lô: staged CSV quá 24 giờ; attached CSV của batch preview/completed/failed quá 30 ngày. Queued/processing, evidence và file legacy được giữ; batch metadata và tài chính không bị xóa. File deleting được retry ở lần worker sau.

## Ledger và số đo

Trigger queue account thay đổi có revision. Mỗi phút đối chiếu tối đa 100 account; full chạy startup và mỗi giờ. Chỉ báo lỗi, không sửa balance tự động. Xóa queue theo revision tránh bỏ thay đổi đồng thời. Giữ khóa system account.

Benchmark local pool tối đa 12 connection, 10 credit/user: 10 user/100 operation đạt 47,89 op/s, p95 676,19 ms; 100 user/1.000 operation đạt 324,85 op/s, p95 654,20 ms. Số dư và ledger khớp. Số đo bao gồm chờ pool trên máy local, không đại diện công suất production. Kết quả riêng: tests/results/wallet-contention.json.

EXPLAIN trên fixture 50.000 dòng, 49.000 dòng đã applied: lookup valid đầu tiên dùng partial index chạm 3 shared blocks, thực thi 0,036 ms; bỏ partial index trong transaction thử nghiệm phải lọc 49.000 dòng, chạm 653 blocks, khoảng 5,761 ms. Đây là một mẫu query local, không phải số đo toàn pipeline import hay RAM. Plans lưu ở tests/results/import-valid-index.json. Transaction bỏ index được rollback và schema test được dọn.

## Frontend, proxy và test

Proxy ký timestamp/IP/method/path bằng PROXY_SIGNING_KEY; backend chỉ tin IP đã xác minh. Request unsigned dùng socket IP, session hợp lệ dùng user ID cho rate limit. Principal chỉ lấy danh tính/quyền; bank profile đọc riêng tại /me.

Frontend invalidate theo nghiệp vụ; giữ product preview khi đổi ngôn ngữ/thao tác tài chính. Pagination dùng metadata từng response. Operation keys giới hạn theo session, không giữ raw password/bank payload. Đồng hồ link dùng chung, cập nhật focus/visibility và kiểm Date.now lúc thao tác. Màn hình admin/browser lazy load, domain types sinh từ contract.

CI backend bắt buộc PostgreSQL/Chromium fixture; frontend chạy desktop/mobile E2E và CSP hai chế độ. Mỗi browser run dùng workspace copy, cổng và server riêng; HMR không sửa source gốc. DB tests có schema riêng trong _test; migration roundtrip so balance/digest ledger. Docker/SSH deployment và Shopee live acceptance là opt-in riêng.

backend/chromium là nguồn canonical. Chạy `scripts/export-chromium.ps1 -Destination <đường dẫn chromium gốc workspace>` từ backend để xuất 11 file công khai và kiểm hash. Không copy secrets/profile hoặc xóa file vận hành ở đích.

Khi transport CDP ngắt, Manager giữ ID tab cho tới khi xác nhận tab đã đóng. Reconnect dọn các ID do Manager sở hữu và giữ ID cleanup lỗi để retry; tab đăng nhập native được giữ. Kiểm thử cho phép CloseTarget hoàn thành bất đồng bộ trong tối đa 5 giây nhưng vẫn bắt rò rỉ tồn tại.
