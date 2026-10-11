# Tracking v4 compact

Chỉ phát hành và đối soát định dạng `v4-compact-1`. Không đọc tracking v1/v2/v3
hoặc layout v4 đầy đủ cũ, không chuyển đổi link và không viết lại lịch sử đơn.

## Dữ liệu được mã hóa

| Trường | Số byte |
|---|---:|
| UUID khách hàng | 16 |
| Thời điểm phát hành, Unix giây uint32 big-endian | 4 |
| Hệ số hoàn cuối cùng, số nguyên 0–100 | 1 |
| Phiên bản chính sách, canonical unsigned varint | 1–5 |
| Shop ID, canonical unsigned varint | 1–10 |
| Item ID, canonical unsigned varint | 1–10 |
| Độ dài Affiliate ID theo chữ số | 1 |
| Giá trị Affiliate ID dạng số big-endian | 1–14 |

Payload dài 26–61 byte. Affiliate ID gồm 1–32 chữ số; độ dài giữ nguyên các số 0
ở đầu. Số byte của giá trị được xác định từ độ dài chữ số, không từ giá trị.
Decoder từ chối varint dư byte, dữ liệu thừa, giá trị ngoài biên và biểu diễn
không canonical. Timestamp hỗ trợ đến 2106-02-07 06:28:15 UTC.

Hệ số 0.63 được lưu là 63. Tiền hoàn thực tế vẫn lấy hoa hồng trong báo cáo:
`ceil(commission × coefficient / 100)`, giữ nguyên quy tắc làm tròn lên.
Không mang min/max, tỷ lệ trước thuế, thuế, hạng, UUID chính sách, thời điểm
tự xóa, tên sản phẩm, URL, giá hoặc hoa hồng dự kiến.

Phiên bản chính sách tra ra UUID phục vụ foreign key và kiểm toán. Đối soát và
duyệt đơn không đọc thuế hoặc tỷ lệ chính sách để tính lại hệ số đã mã hóa.

## Envelope và năm Sub ID

Dùng AES-256-GCM với `cipher.NewGCMWithRandomNonce` của Go. Thư viện tạo nonce
12 byte và tag xác thực 16 byte, thêm 28 byte. Envelope gồm version 4 (1 byte),
key ID (1 byte), nonce, ciphertext và tag; tổng 56–91 byte.

AAD là `hoanxu:shopee-tracking:v4-compact-1:` nối hai byte header. Toàn envelope
đổi sang Base62 không padding, chia đều liên tiếp vào năm phần (phần đầu nhận
ký tự dư), rồi thêm prefix vị trí `HX41` đến `HX45`. CSV phải đủ cả năm,
đúng vị trí, độ dài và nguyên văn. Sửa, tráo hoặc ghép phần khác link bị từ chối.

Ví dụ Affiliate ID 123456789, shop 83496725, item 6939920023, policy version 1:
payload 36 byte, envelope 66 byte, Base62 88 ký tự; năm Sub ID dài
**22/22/22/21/21**, tổng **108** ký tự tính cả prefix. Độ dài thay đổi theo các
ID; trường hợp biên mỗi Sub ID tối đa 29 ký tự. Guard 50 ký tự trong browser
adapter là giới hạn do ứng dụng đặt; chưa có tài liệu Shopee chính thức xác nhận.

`trackingCode` là SHA-256 toàn envelope, dạng 64 ký tự hex. Nó không phải
Sub ID riêng lẻ và không chứa dữ liệu nghiệp vụ giải mã. API khách giữ nguyên
cấu trúc, không trả payload giải mã hoặc thuế.

## Khóa và vận hành

- `TRACKING_KEYS`: JSON ánh xạ ID canonical 1..255 sang khóa Base64 32 byte.
- `TRACKING_ACTIVE_KEY_ID`: ID duy nhất dùng phát hành link mới.
- Dùng khóa ngẫu nhiên riêng cho tracking, không dùng DATA_ENCRYPTION_KEY.
  Cấp phát khóa qua kho bí mật/env riêng; không commit hoặc ghi khóa/token
  đầy đủ vào log.
- Giữ khóa cũ để giải mã báo cáo đến muộn, không tái sử dụng ID cho khóa khác.
  Go yêu cầu không quá 2^32 lần mã hóa trên một khóa với nonce ngẫu nhiên;
  xoay khóa trước ngưỡng đó.
- Thiếu cả hai biến: tracking bị tắt, các phần khác của API vẫn khởi động.
  Cấu hình một phần hoặc khóa sai định dạng: từ chối khởi động.
- Thiếu khóa giải mã trả lỗi cấu hình `TRACKING_KEY_UNAVAILABLE`, không đánh
  dấu khách/đơn không hợp lệ. Preview rollback toàn bộ; worker giữ dòng để có
  thể xử lý lại sau khi phục hồi khóa. Nếu batch đã failed, nhập lại CSV sau
  khi sửa khóa; chống trùng đơn và ledger vẫn áp dụng.

## Tạo link và đối soát

Frontend tổng quan và trang link hoàn tiền đều nhận một sản phẩm mỗi lần.
Backend không có API tạo nhiều link. Native Shopee GQL vẫn gọi
`batchGetCustomLink` tại `/api/v3/gql?q=batchCustomLink` với đúng **một**
phần tử `linkParams` và `sourceCaller=CUSTOM_LINK_CALLER`; tên operation
là tên API Shopee. Browser adapter từ chối đầu vào có số sản phẩm khác 1.

Đối soát xác thực AES-GCM, khách chưa khóa, Affiliate ID hiện tại hoặc đã lưu
trong registry, shop/item và Order Time không trước thời điểm phát hành.
DB lưu đơn, chống nhập trùng, kiểm tra khách và ghi ví. Không cần bản ghi link.

Quy tắc xóa link sau 120 giờ chỉ là giữ danh sách gọn: thời điểm đó được suy
ra từ thời điểm phát hành, không mang trong mã và không giới hạn báo cáo đến
muộn hoặc đơn đặt sau khi xóa link. Xóa link không xóa đơn hoặc ledger.

## Xác minh trước khi bật

Fingerprint xác minh gắn với version 4 và FormatID; proof cũ không bật được
tạo link mới. Sau khi cấp khóa, kiểm tra lại sản phẩm/tracking trong quản trị.

Kiểm thử browser bằng fixture chỉ xác nhận hợp đồng request/response. Trước
khi bật chính thức, phải xác nhận Shopee thật nhận cả năm Sub ID compact và
báo cáo CSV thật trả nguyên vẹn cả năm. Không coi việc nhận URL ngắn là bằng
chứng vòng đối soát CSV thành công. Lần thay đổi này không tự bật, deploy,
migrate hoặc sửa dữ liệu đang phục vụ khách.

## Kiểm chứng local — 12/10/2026

Đã cấu hình khóa tracking riêng trong env local bị Git bỏ qua, build backend và
chạy `admin verify-shopee-settings` bằng Chrome đăng nhập thật. Kiểm tra phiên,
sản phẩm và GQL tracking v4 compact thành công; request khớp đủ năm Sub ID và
Shopee trả URL ngắn hợp lệ. Không tạo link khách, đơn hoặc giao dịch ví.

Đây là xác nhận tạo link thật, chưa phải vòng CSV thật. Chưa bật tạo link khách
hoặc triển khai lên production; bước CSV ở trên vẫn cần hoàn tất trước khi bật.
Render blueprint đã khai báo hai biến khóa để cấu hình riêng khi triển khai.

Tài liệu chuẩn: [Go NewGCMWithRandomNonce](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce).

