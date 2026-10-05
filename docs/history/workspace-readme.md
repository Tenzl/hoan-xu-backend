# Hoàn Xu

Ứng dụng local Windows: **PostgreSQL + Go + Next.js + Chromium**, không Docker/Redis.

Khách đăng nhập Google. Tài khoản nội bộ dùng username/mật khẩu do quản trị cấp. Không có đăng ký công khai. Hai file nguồn ở root được giữ nguyên; `/demo` là bản HTML mẫu độc lập, không gọi API tiền thật.

Giao diện khách, nội bộ, quản trị và demo có nút **VI / EN** ở thanh trên. Lựa chọn ngôn ngữ được nhớ trên trình duyệt, gồm nhãn, form, trạng thái và thông báo lỗi; tên người dùng, thông tin ngân hàng và nội dung do người dùng nhập giữ nguyên. `localStorage` chỉ lưu tùy chọn ngôn ngữ/giao diện trong ứng dụng thật.

Trong **Hồ sơ** (`/account`), khách lưu ngân hàng, số tài khoản và **họ tên đầy đủ đúng như hiển thị trên ngân hàng**, riêng với tên hiển thị. Số tài khoản được giữ dạng chuỗi để không mất số 0 đầu, gồm 6–20 chữ số. Các thông tin này được mã hóa trong PostgreSQL và tự điền ở form rút tiền. Có thể đổi thông tin trước khi gửi; mỗi yêu cầu giữ bản chụp riêng nên cập nhật hồ sơ không đổi yêu cầu cũ. Để xóa ngân hàng đã lưu, để trống cả ba trường rồi lưu hồ sơ.

Migration `000006_user_bank` thêm hồ sơ ngân hàng, không thay đổi số dư hoặc yêu cầu rút tiền hiện có. Khóa mã hóa phải được sao lưu cùng dữ liệu. Khi sửa bản dịch, chạy `npm run generate:i18n` trong frontend để cập nhật catalog API, demo English và CSP hashes, rồi build lại backend/frontend.

## Khởi động

Giao diện không có thanh tài khoản phía trên. Tài khoản, VI/EN, sáng/tối và icon chuông nằm ở cuối sidebar; trên mobile mở bằng **Thêm**. Menu cuộn độc lập, hỗ trợ bàn phím/Escape và giữ tùy chọn giao diện sau khi tải lại.

Màn **Lấy link hoàn tiền** có ô dán/xóa link, thông tin hoa hồng tự động, kết quả riêng để sao chép/mở mua/lưu và lịch sử link bên dưới. Hạng và khoảng chia lấy từ backend; hướng dẫn ghi nhận đơn nằm bên cạnh trên desktop và xếp dọc trên mobile. Kiểm tra sản phẩm không tự tạo link. Đổi link bỏ kết quả cũ, kể cả khi yêu cầu tạo link cũ còn đang chạy.

1. Chạy `./scripts/setup-local.ps1` để kiểm tra dependency. Cần Node.js 22+, Go theo phiên bản trong `backend/go.mod`, PostgreSQL 18 (hoặc PostgreSQL hỗ trợ các migration), Chromium/Chrome. Go portable trong `.tools/go/bin` được script tự nhận nếu có.
2. Tạo `backend/.env` từ `.env.example`, điền kết nối database và khóa mã hóa 32 byte base64. Sinh khóa bằng `go run ./cmd/admin key` trong `backend`. **Giữ khóa này ổn định** để đọc thông tin ngân hàng và voucher đã lưu.
3. Tạo database `hoanxu`, tài khoản ứng dụng không phải superuser và database kiểm thử `hoanxu_test`. `MIGRATION_DATABASE_URL` dành cho chủ schema; `DATABASE_URL` dành cho runtime.
4. Trong `frontend`: chạy `npm ci`.
5. Chạy `./scripts/migrate.ps1`, rồi `./scripts/dev.ps1` ở root.
6. Mở [http://localhost:3000](http://localhost:3000). API bind riêng `127.0.0.1:8080`.

Các script chỉ dừng tiến trình ứng dụng mà chúng đã tạo; không dừng PostgreSQL Windows service. Có thể chạy riêng hai terminal:

```powershell
# Terminal 1 — backend
cd backend
go run ./cmd/api
```

```powershell
# Terminal 2 — frontend
cd frontend
npm run dev
```

Thông tin kết nối local đã cấu hình trên máy này nằm trong `backend/.env`, bị loại khỏi Git. Không sao chép secret sang README hoặc file mẫu.

## Admin đầu tiên

Từ root chạy `./scripts/create-admin.ps1 -Username admin -Name "Quản trị"`. Script nhận mật khẩu bằng prompt ẩn và tự tìm Go portable nếu chưa có Go trong PATH. Bạn tự chọn mật khẩu; không có tài khoản mặc định được seed.

Trong thư mục `backend`, đặt `ADMIN_PASSWORD` vào environment của terminal, rồi chạy:

```powershell
go run ./cmd/admin create --username admin --name "Quản trị" --role admin
Remove-Item Env:ADMIN_PASSWORD
```

Không truyền mật khẩu bằng command argument. Đăng nhập `/internal/login` và đổi mật khẩu tạm khi được yêu cầu. Mật khẩu có 12–128 ký tự.

Admin cấp thêm tài khoản trong **Tài khoản nội bộ**. Quyền staff: `orders`, `withdrawals`, `users`, `gifts`, `community`, `notifications`, `settings`, `audit`. Staff không được cấp tài khoản/nâng quyền nội bộ. Các thao tác tiền hoặc chính sách yêu cầu xác thực lại trong vòng 15 phút; sau xác thực, thực hiện lại thao tác đã yêu cầu.

CLI khôi phục mật khẩu: `go run ./cmd/admin reset --id USER_UUID` với `ADMIN_PASSWORD` trong environment. Reset thu hồi các phiên.

## Google

Tạo OAuth client cho web, origin `http://localhost:3000`, callback:

```text
http://localhost:3000/api/v1/auth/google/callback
```

Điền `GOOGLE_CLIENT_ID` và `GOOGLE_CLIENT_SECRET` trong `backend/.env`, restart backend. Nếu ứng dụng Google đang ở chế độ thử nghiệm, thêm email người thử nghiệm trong Google Console. Không cần Google password. Lần đầu tạo customer với 0 tiền và 0 xu. Google không đăng nhập hoặc ghép vào tài khoản nội bộ.

## Shopee: trạng thái xác minh

Trên Tổng quan và Lấy link hoàn tiền, dán link sản phẩm sẽ tự gọi checker sau 500 ms và hiển thị tên, giá, tỷ lệ và số tiền hoa hồng ngay dưới ô nhập (VI/EN). Đổi/xóa link hủy yêu cầu cũ và bỏ kết quả cũ; lỗi có nút thử lại. Kiểm tra không tự tạo link, đơn hay cộng ví. Chỉ hiển thị số tiền khi backend xác nhận schema; hoa hồng checker là dự kiến từ sàn, chưa phải tiền hoàn đã duyệt.

Mặc định **tắt tích hợp thật**. Resolver, browser worker, cache, parser, link builder và theo dõi phiên đã có. Schema sản phẩm và đơn vị giá/hoa hồng đã được đối chiếu với 10 trang Affiliate ngày 05/10/2026; bằng chứng đã lọc thông tin tài khoản nằm ở `backend/internal/affiliate/testdata`. Chưa xác minh tracking bằng báo cáo đơn hàng thật.

1. Điền `CHROME_PATH`, `CHROME_PROFILE` và `SHOPEE_PUBLISHER`.
2. Mở **Quản trị → Cài đặt cookie**, dán Cookie header (`name=value; name2=value2`) hoặc JSON array cookie của `shopee.vn`, rồi chọn **Lưu và áp dụng cookie**. Không cần restart backend để áp dụng. Chỉ admin hoặc staff có quyền `settings` được dùng ô này. Khi chạy trên máy có màn hình, có thể đặt `CHROME_HEADLESS=false` trong `backend/.env` rồi restart backend để hiện Chromium với một tab Affiliate luôn mở. Đăng nhập/xác minh thủ công trong tab đó, rồi bấm **Kiểm tra phiên hiện có** để dùng cookie vừa cập nhật trong cùng browser. Mặc định `CHROME_HEADLESS=true`.
3. Chromium do backend quản lý được giữ chạy, các lần check dùng tab mới trong cùng phiên. **Kiểm tra phiên hiện có** dùng cookie đang có trong browser, không nạp lại cookie đã dán và không tạo lại browser khi browser còn hoạt động. Cookie đã dán lưu mã hóa ở `private-data/shopee-cookies.enc` theo vị trí profile mặc định, chỉ nạp khi khởi tạo browser mới sau restart/crash. Không lưu cookie trong PostgreSQL, localStorage hoặc audit. Đây là Chromium với profile riêng của ứng dụng; không tự kết nối Chrome cá nhân. Cách đăng nhập bằng CLI vẫn dùng được: dừng backend browser, chạy `go run ./cmd/admin shopee-login`, đăng nhập thủ công rồi Enter. Nếu dùng CLI mà không lưu cookie qua quản trị, bật `SHOPEE_ENABLED=true` để backend mở profile khi khởi động.
4. Các mẫu hiện tại xác nhận `SHOPEE_PRICE_SCALE=100000`: giá chia cho 100000, còn tiền hoa hồng dạng `₫2.375` đã là VND. Đặt `SHOPEE_SCHEMA_VERIFIED=true` để hiển thị tên, giá và hoa hồng đã chuẩn hóa. Nếu Shopee thay đổi schema, đối chiếu lại field và đơn vị với 10–20 sản phẩm trước khi bật. Kiểm thử đối chiếu fixture API với giá/hoa hồng hiển thị trên trang Affiliate.
5. Tạo link thử và xác nhận `sub_id` xuất hiện trong báo cáo. Chỉ sau đó bật `SHOPEE_TRACKING_VERIFIED=true`, cấu hình mẫu `https://s.shopee.vn/an_redir` và mở trạng thái Shopee trong quản trị.

Không tự vượt CAPTCHA/2FA. Phiên hết hạn dừng xử lý; tài khoản, ví và cộng đồng vẫn chạy. Checker thiếu schema đã xác minh không hiển thị con số giả hoặc trả raw payload. Các sàn khác vẫn giữ mẫu tại `/demo`.

## Đối soát, ví và quà

Quản trị mở `/admin/settings` → **Chính sách chia hoa hồng theo hạng** để đặt ngưỡng đơn duyệt và % tối thiểu/tối đa cho Đồng, Bạch kim, Kim cương. Mặc định 0/30/100 đơn và cùng tỷ lệ cũ (hiện 50–50%). Đồng luôn có ngưỡng 0; hai ngưỡng sau tăng dần. Khoảng 0–100%, tối đa hai chữ số thập phân; các khoảng được phép giao nhau. Chọn **Xem trước chính sách**, rồi **Lưu chính sách mới**; cần xác thực lại mật khẩu nếu phiên xác thực đã quá 15 phút.

Hạng hiện tại tính từ số đơn đã duyệt. Link lưu hạng/khoảng/phiên bản lúc tạo; mỗi đơn random đều một tỷ lệ trong khoảng đó khi được ghi nhận lần đầu. Tiền hoàn = `floor(commission × shareBps / 10000)`. Nhập CSV lặp, worker restart hoặc duyệt lại không random lại. Điều chỉnh hoa hồng dùng tỷ lệ đã lưu. Link/đơn cũ giữ tỷ lệ cố định và nhãn chính sách cũ; sửa chính sách hoặc FAQ không thay tiền cũ. Khách thấy khoảng dự kiến trước khi có đơn, tỷ lệ đã chọn khi có đơn; tiền pending chưa thể rút.

- CSV UTF-8, dấu phẩy hoặc chấm phẩy, tối đa 10 MB/50.000 dòng. Có mapping cột JSON trước preview; mẫu ở `database/seeds/report-example.csv`.
- Cột chuẩn: `channel,publisher,order_id,line_id,tracking_code,date,product_name,value,commission,status`.
- `value` và `commission` là số nguyên VND không có dấu phân cách. Ngày `YYYY-MM-DD` theo giờ Việt Nam hoặc RFC3339. Trạng thái `pending`, `approved`, `rejected`.
- Preview → commit → worker PostgreSQL. Dòng không khớp tracking được cách ly; admin khớp bằng tracking có bằng chứng. Dữ liệu cấu trúc sai cần sửa file và nhập lại.
- Đơn nhập vẫn chờ quản trị duyệt; **nguồn sàn phải approved** mới được cộng ví. Đơn đã duyệt thay đổi được đưa vào adjustment để kiểm tra, không tự ghi đè ledger.
- Cashback chụp chính sách lúc tạo link, làm tròn xuống đồng; nguồn thực nhận từ CSV, không lấy hoa hồng checker làm tiền đã duyệt.
- Rút từ 50.000đ, bội số 1.000đ. Nhận xử lý trước khi chuyển khoản; tải PNG/JPEG/PDF bằng chứng rồi dùng ID file để xác nhận đã chi. Không xác nhận paid nếu chưa chuyển thực tế.
- Điểm danh +1 xu/ngày; thưởng mốc 3/7/14/30. Đổi xu sang tiền mặc định tắt. Tồn voucher mặc định 0. Admin chỉ bật/cập nhật sau khi chuẩn bị ngân sách/mã.
- Mã voucher chỉ người nhận xem. Không có dịch vụ gửi email tự động trong phiên bản này.

## Kiểm thử và codegen

Mở ứng dụng bằng `scripts/dev.ps1` trước, rồi chạy `scripts/test.ps1` trong terminal khác. Script chạy Go tests (database riêng), vet, TypeScript, build và Playwright. Các luồng ghi tiền/auth test dùng `TEST_DATABASE_URL` kết thúc `_test`; tests tạo schema riêng rồi chỉ xóa schema do chính nó tạo. UI công khai đọc API local đang chạy; UI khách/admin dùng fixture. Không reset database ứng dụng.

```powershell
cd backend
go test ./...
go vet ./...
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
```

Go integration tests cần `TEST_DATABASE_URL`; browser lifecycle smoke test cần `BROWSER_TEST_PATH` đến Chromium. Nếu thiếu chúng, Go ghi rõ test bị skip.

```powershell
cd frontend
npm run generate
npm run typecheck
npm run build
npx playwright install chromium
npm test
```

Playwright kiểm tra giao diện công khai với API local; kiểm tra đủ màn hình khách/admin bằng response fixture riêng của tests. Xác thực Google thật và Shopee thật cần cấu hình/tài khoản bên ngoài, không được giả lập thành dữ liệu production.

## Backup và khôi phục

`scripts/backup.ps1` tạo dump mới trong `private-data/backups`, không ghi đè. Back up thêm thư mục files và khóa mã hóa ở nơi riêng.

`scripts/restore.ps1 -Backup PATH -TargetDatabase hoanxu_restore_test` chỉ restore vào DB mới/rỗng, từ chối database đang dùng. Dùng connection schema-owner nếu cần quyền tạo DB. Không tự thay `DATABASE_URL` sau restore.

## Tài liệu

- `docs/implementation-plan.md`: phạm vi và checklist.
- `docs/verification.md`: kiểm chứng và phần còn cần dữ liệu live.
- `database/schema/erd.md`: quan hệ dữ liệu.
- `docs/inventory.md`: đối chiếu đủ màn hình mẫu/thật.
- `docs/local-operations.md`: quy trình đối soát, chi trả và khôi phục.
- `backend/contracts/openapi.yaml`: hợp đồng REST.

Phiên bản này dành cho local; hosting/HTTPS/hardening production là giai đoạn sau. Không thêm Docker hoặc Redis để chạy ứng dụng.
