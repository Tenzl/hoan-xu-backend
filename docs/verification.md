# Kiểm chứng và giới hạn

## Đã chạy

- Ngày kiểm chứng: 05/10/2026, Windows, PostgreSQL 18, database nghiệp vụ `hoanxu`; runtime `hoanxu_app` không có superuser/CREATEDB. Database nghiệp vụ không có khách hoặc số dư mẫu.
- Google OIDC fixture: ID token RSA ký/xác minh bằng verifier thật; state replay/sai, nonce, issuer, audience, hết hạn, chữ ký sai, sub rỗng, email chưa xác minh, trùng email nội bộ, định danh sub ổn định và account bị khóa.
- Go unit tests: Argon2id, URL/host validation, product IDs, điểm danh/mốc/timezone, đổi xu, CSV mapping/BOM/validation.
- PostgreSQL integration tests trên `hoanxu_test`: duyệt đồng thời, idempotency/payload conflict, rút/hoàn đúng một lần, ledger cân bằng, điểm danh đồng thời, ngân sách đổi xu, quà/tồn/refund, ownership/CSRF/blocked session và internal password.
- Regression trả debt đồng thời: test giữ system lock để tái hiện đọc số dư cũ; bản sửa khóa trước khi đọc debt.
- Chromium lifecycle smoke test với profile tạm và data URL, không truy cập tài khoản Shopee.
- SSRF: IP private/reserved/mapped IPv6, DNS có IP public/private lẫn nhau, chỉ dial IP đã kiểm tra để chặn rebinding, host giả và redirect/loop.
- HTTP với PostgreSQL: toàn bộ route đọc khách/admin, nhập đơn tay, password tạm và staff chỉ truy cập nhóm quyền được cấp.
- Race paid/rejected: chỉ một trạng thái chi/hoàn commit, held không bị chi hai lần, số dư khớp ledger.
- CSV worker: phục hồi lease giả lập sau process crash, không duyệt tiền từ báo cáo pending, retry commit không tạo đơn lặp.
- Cursor: nhiều bản ghi cùng thời gian, không trùng/mất dòng, cursor sai bị từ chối.
- TypeScript typecheck và Next.js build.
- Playwright: màn hình công khai/demo và toàn bộ khách/admin trên desktop/mobile. Fixture chỉ thuộc tests.
- Regression form: giữ nguyên khoảng trắng có ý nghĩa trong mật khẩu. Giao diện 375/768/1440px không tràn ngang; đã xem ảnh desktop/mobile. Script dev đã kiểm tra khởi động và dừng đúng tiến trình, frontend không nhận nhầm PORT của backend.
- Backup/restore được thử bằng script vào DB riêng `hoanxu_restore_verification` (migration 4), và bản cuối `hoanxu_restore_v5` (migration 5, FAQ 4 câu). Đủ 36 bảng, migration sạch; cấu hình DB nghiệp vụ giữ nguyên. FAQ được kiểm tra validation và giữ nội dung khi cập nhật chính sách.
- OpenAPI được lint hợp lệ; codegen TypeScript thành công. Lint có cảnh báo cố ý về localhost và redirect OAuth không có HTTP 2xx.
- Lượt cuối `scripts/test.ps1`: Go integration/unit + Chromium smoke, vet, TypeScript, Next.js build, 8/8 Playwright đều qua. Public UI test yêu cầu API trả thành công; không chấp nhận backend ngừng chạy làm bằng chứng. npm audit dependencies runtime: 0 vulnerability.

## Bổ sung hồ sơ ngân hàng và Việt/English — 05/10/2026

- Migration 6 đã áp dụng trên `hoanxu`, giữ dữ liệu/số dư hiện có; repository sqlc được sinh lại.
- PostgreSQL regression: bank profile mã hóa, giữ STK có số 0 đầu, tên chủ tài khoản độc lập tên hiển thị, dữ liệu riêng không vào audit, update không hợp lệ không cập nhật một phần, nội bộ không có bank profile, xóa cấu hình bằng ba trường trống và snapshot rút tiền giữ nguyên sau đổi hồ sơ.
- Toàn bộ Go tests với `TEST_DATABASE_URL` trên `hoanxu_test` và `go vet` qua. Truy vấn lịch sử rút tiền đã định danh rõ `w.bank_details` để tránh trùng cột hồ sơ mới.
- Accept-Language vi/en-US trả lỗi 401 cùng mã lỗi, message theo ngôn ngữ. Catalog UI và backend đồng bộ bằng `npm run generate:i18n`.
- 16/16 Playwright trên desktop/mobile: các màn khách/admin/nội bộ, bank profile → rút tiền, toggle không làm mất form đang nhập, lựa chọn giữ sau reload, UI English và không tràn ngang màn khách, demo English chạy dưới CSP và demo Việt giữ nguyên SHA-256 của HTML gốc.
- Nội dung do người dùng nhập giữ nguyên. Chưa có cấu hình Google live/Shopee live; kiểm thử hồ sơ sử dụng tài khoản giả trong database/schema kiểm thử và API fixture của UI.

## Bổ sung cookie Shopee trong quản trị — 05/10/2026

- Parser Cookie header/JSON: giữ dấu `=` trong giá trị, giới hạn kích thước, domain Shopee chính xác, từ chối domain giả/control characters/trùng cookie; lỗi không phản chiếu input.
- Lưu cookie mã hóa bằng khóa local, thay file nguyên tử; kiểm thử đọc lại và file hỏng. Không lưu giá trị trong audit hoặc PostgreSQL.
- Chromium thực với HTTP fixture local: hai check đồng thời dùng chung browser; probe không ghi đè cookie đã thay đổi trong browser; dán lần hai dùng lại browser; restart khôi phục cookie đã lưu; response 401 đánh dấu cần đăng nhập lại. Không dùng tài khoản hoặc website Shopee thật trong test này.
- PostgreSQL/API regression: guest/customer bị chặn, kiểm tra CSRF, cookie sai trả 422, browser thiếu trả 503, response không lộ cookie. Route dùng cùng guard quyền `settings` cho admin/staff. Toàn bộ Go tests trên DB riêng `hoanxu_test`, Chromium fixture và `go vet` đã qua; TypeScript và Next.js build đã qua.
- Playwright: 26 trường hợp desktop/mobile đã kiểm chứng qua lượt chạy toàn bộ và chạy lại các trường hợp sau sửa locator. Bốn trường hợp cookie kiểm tra lưu/xóa ô nhập, không lưu localStorage, probe không gửi lại cookie, giữ input khi validation lỗi, VI/EN và không tràn ngang. Không thay đổi giao diện quản trị để đáp ứng locator cũ; test đọc tên tài khoản hiện có thay cho link đã bỏ.

## Ba hạng và random tỷ lệ — 05/10/2026

- Migration 8 đã áp dụng trên `hoanxu` sau backup `hoanxu-20261005-113317.dump`; dữ liệu nghiệp vụ vẫn có 2 users, 0 orders và không nạp tiền/demo. Ba hạng mặc định ngưỡng 0/30/100, khoảng 50–50%.
- Kiểm thử migration trên schema riêng có link, đơn đã duyệt và ledger: tỷ lệ cố định, cashback và số dư giữ nguyên, không gán hạng lịch sử.
- PostgreSQL kiểm chứng link qua service chụp snapshot; pending/rejected không lên hạng; lên hạng và đổi chính sách không ảnh hưởng link cũ; 8 batch trùng xử lý bởi 2 worker chỉ tạo một đơn/một tỷ lệ; worker restart và thay commission giữ draw.
- Duyệt đồng thời/idempotency chỉ credit một lần; điều chỉnh tăng/giảm giữ shareBps, ledger cân bằng. Nhập tay dùng cùng hàm random và chống trùng. Hai admin cập nhật đồng thời chỉ tạo một phiên bản; stale edit trả 409.
- API kiểm tra guest/customer/staff, quyền settings, CSRF, reauth, idempotency và tỷ lệ quá hai chữ số thập phân trả 422. Sửa cài đặt/FAQ không tạo chính sách.
- Toàn bộ Go unit/integration với `hoanxu_test` và Chromium fixture, `go vet`, TypeScript và Next.js build đều qua. Playwright toàn bộ 56/56 desktop/mobile qua; các kiểm tra bổ sung sau chỉnh bảng mobile, cuộn ngang bằng bàn phím và reload conflict đều qua. Đã xem ảnh form quản trị và bảng đơn mobile; dữ liệu trong ảnh thuộc fixture.

## Cần kiểm chứng bên ngoài

Google OAuth đã được cấu hình local; kiểm thử tự động dùng OIDC fixture, chưa xác nhận luồng Google live trong lượt bổ sung cookie. Chưa có cookie/account/publisher hoặc báo cáo Shopee thật để chứng minh schema/scale/cap và tracking báo cáo. Flags schema/tracking vẫn chặn trước khi có bằng chứng. Các fixture normalization là tổng hợp theo MD, không phải response Shopee thực.

Phần này không phải xác nhận đủ điều kiện production. Bản hiện tại được bàn giao local; production deployment và hardening được tách sang giai đoạn sau.
