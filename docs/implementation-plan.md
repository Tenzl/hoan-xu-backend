# Ví Xu: kế hoạch triển khai và bàn giao

Đã triển khai thiết kế cột ví 340px trên màn /link, donut số dư với vòng dự kiến riêng, tổng đơn, rút tiền tại chỗ, quyền lợi hạng và hướng dẫn giữ nguyên. Dưới 1200px chuyển một cột; dưới 600px các khối ví xếp dọc.

Ví chung 1 Xu=1đ; điểm danh +300/ngày, bonus ×300; gift dùng ví và tạm giữ riêng. PostgreSQL migration 9 chuyển dữ liệu cũ một lần, giữ lịch sử. Không random hoặc cộng ví lúc check/tạo link. OpenAPI/types và VI/EN đã cập nhật.

Chi tiết vận hành, thay đổi API, migration và checklist: [unified-wallet.md](unified-wallet.md).

Giữ local Windows, PostgreSQL, không Docker/Redis. ENV_FILE chọn profile .env.local riêng khi .env dành cho môi trường khác. Supabase không nằm trong nghiệm thu local này.
