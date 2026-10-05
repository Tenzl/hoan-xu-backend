# Kế hoạch đã chốt

Local Windows, PostgreSQL, Go/chi/pgx/sqlc, Next.js/TypeScript, Chromium; không Docker/Redis. Khách Google, nội bộ do admin cấp bằng mật khẩu. Không có đăng ký công khai. Giữ nguyên HTML/MD gốc và demo độc lập.

## Checklist triển khai

- [x] Thiết kế lại màn Lấy link hoàn tiền: bố cục form/hướng dẫn, nút dán/xóa, xem trước hoa hồng, trạng thái tạo/copy/lưu, chống hiển thị link cũ khi đổi sản phẩm; VI/EN, sáng/tối và responsive.
- [x] Dán link tự kiểm tra hoa hồng và hiển thị dưới ô nhập; debounce, hủy yêu cầu cũ, thử lại, giữ trạng thái chưa xác minh và nhãn dự kiến, hỗ trợ VI/EN và mobile. Kiểm thử dùng fixture; không thay thế xác minh Shopee live.
- [x] Ba folder database/backend/frontend; scripts PowerShell local.
- [x] SQL migrations, defaults, generated sqlc và ERD.
- [x] Google OIDC + state/nonce/PKCE; session PostgreSQL, CSRF, role/permission.
- [x] Tài khoản nội bộ, password tạm, reset/revoke session, reauth.
- [x] Resolver chống SSRF; Chromium profile riêng, queue, cache memory, singleflight.
- [x] Ô cookie Shopee trong quản trị, Cookie header/JSON với allowlist domain; lưu mã hóa ngoài DB, áp dụng không restart; check/probe dùng lại Chromium và phiên hiện có, cache tách theo phiên cookie.
- [x] Checker và parser chỉ bật số tiền sau khi schema/scale được xác minh.
- [x] Link tracking, snapshot chính sách; flag chặn trước kiểm chứng.
- [x] Ba hạng Đồng/Bạch kim/Kim cương; ngưỡng và khoảng chia cấu hình theo phiên bản, snapshot lúc tạo link, random một lần/đơn, giữ tỷ lệ khi retry/điều chỉnh; quản trị xem trước và VI/EN.
- [x] CSV preview/mapping/commit, worker lease, cách ly/khớp tracking, retry.
- [x] Đơn nguồn/internal tách trạng thái; duyệt, từ chối và điều chỉnh.
- [x] Ledger bất biến/cân bằng, debt, rút/giữ/chi/hoàn và bằng chứng private.
- [x] Điểm danh, xu, đổi tiền feature flag, voucher/tồn kho/refund.
- [x] Deal, useful unique, kiểm duyệt, thông báo/read receipts.
- [x] Màn hình khách/admin, mobile, sáng/tối; mascot và CSS mẫu.
- [x] Hồ sơ khách: ngân hàng, STK dạng chuỗi và tên chủ tài khoản đầy đủ; mã hóa, tự điền rút tiền, snapshot yêu cầu không thay đổi khi sửa hồ sơ.
- [x] Việt/English cho giao diện khách/nội bộ/admin/demo, toggle VI/EN nhớ lựa chọn; API Accept-Language cho thông báo lỗi.
- [x] FAQ/email quản trị cấu hình; backup/restore vào DB mới và script tự tạo admin.
- [x] OpenAPI/generated types, kiểm thử PostgreSQL và browser UI.
- [ ] Google login thật: cần Client ID/Secret và cấu hình OAuth.
- [ ] Shopee live: cần account/publisher, response thật 10–20 sản phẩm, CSV xác nhận tracking.
- [ ] Đổi tiền/voucher thật: cần chủ website bật ngân sách và nhập tồn mã.

## Quy tắc giữ mẫu

Lazada/TikTok/Tiki chưa được tích hợp thật: vẫn hiện trong giao diện/cấu hình và `/demo`. Không thay lỗi API bằng fixture. Dữ liệu mẫu không được nhập vào ví thật. HTML mẫu sao chép byte-identical; CSP cho phép script/style mẫu bằng hash, không mở inline scripts mặc định cho ứng dụng thật.

## Acceptance

Ứng dụng chạy local trực tiếp, không yêu cầu container. Auth khách/nội bộ tách quyền. DB bền vững và transaction không lặp. Tất cả màn hình có dữ liệu thật, trạng thái trống/lỗi hoặc mẫu được xác định. Phụ thuộc ngoài chưa có cấu hình không được tuyên bố đã kiểm chứng.
