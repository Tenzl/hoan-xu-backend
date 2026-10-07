# Quản lý link và đơn hàng

Trang `/link` chỉ tạo link: URL, sao chép và mở mua ở cùng một hàng; khung hạn hoàn Xu hiển thị thời gian còn lại và giờ Việt Nam. Trang này không có danh sách lịch sử hoặc thao tác xóa. Việc hết hạn khóa sao chép/mở mua.

Trang `/orders` mặc định mở **Đang lựa**. Các tab còn lại là **Đang xử lý**, **Hoàn thành**, **Từ chối** và **Tất cả**. Link còn hạn chưa có báo cáo nằm ở Đang lựa; link hết hạn chưa có đơn nằm ở Từ chối. Mỗi đơn được báo cáo xuất hiện một lần, kèm link liên quan nếu còn trong DB. Link lịch sử chỉ đọc được giữ trong Tất cả hoặc ghép cùng đơn.

`GET /me/purchases` dùng tài khoản đăng nhập, lọc `status=selecting|progress|completed|rejected|all`, hỗ trợ `page`, `perPage` và cursor. Liên kết đơn/link luôn giới hạn theo khách hàng; sử dụng tracking hoặc `link_id` cho dữ liệu lịch sử. Xóa link không xóa đơn, không tái tạo link khi nhập báo cáo muộn. Khóa xóa ở backend vẫn dựa trên các đơn đang xử lý/hoàn thành.

Migration **20** thêm `affiliate_links.product_name` dạng nullable và index cho đơn gắn link lịch sử. Link mới lấy tên từ checker đã xác minh cùng shop/item ở backend và cache hiện có. Link cũ dùng tên từ đơn liên quan; thiếu tên hiển thị “Tên sản phẩm chưa có”. Không backfill hoặc gọi Shopee cho toàn bộ link cũ. Hạn 144 giờ, chữ ký, hệ số và luồng đối soát không đổi.

## Xác minh local ngày 07/10/2026

- Test backend trên DB riêng `_test`, `vet`, build API/admin, frontend typecheck, kiểm tra bản dịch và build đã qua.
- E2E tạo link/Đơn hàng đã qua trên desktop/mobile; gồm 320px, VI/EN, sáng/tối, bàn phím, giảm chuyển động, khóa xóa và chuyển hết hạn sang Từ chối. Lượt toàn bộ ban đầu có lỗi fixture/locator cũ; các nhóm liên quan được cập nhật và chạy lại 66/66 thành công.
- API integration kiểm tra tên đã lưu, từ chối dữ liệu tên chưa xác minh/sai sản phẩm, tên link lịch sử từ đơn, phân trang/cursor, quyền sở hữu, đơn không còn link và báo cáo muộn đúng hạn.
- DB local `hoanxu_local_20261007_02` đang ở migration 21 sạch, bao gồm migration 20. Bản backup trước migration thuộc `private-data/backups/production-local-20261007-canonical/`; backup schema và snapshot trước/sau khởi động thuộc `private-data/backups/purchases-20261007-094642/`.
- Sau khởi động backend mới: 6.754 link, trong đó 6.752 link lịch sử; 6.749 đơn; 103 người dùng và 409 tài khoản ví. Hash toàn bộ link, đơn, người dùng, tài khoản ví, 13.500 wallet entries và 6.750 wallet transactions khớp trước/sau.
- Backend chạy bằng `scripts/dev-local.ps1`; frontend local dùng bản build `.next-purchases` tại `http://localhost:3000`. Không triển khai Render.
