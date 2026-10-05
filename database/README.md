# PostgreSQL

Migrations SQL nằm trong `migrations/`; cấu hình mặc định được tạo bằng migration 2. `seeds/report-example.csv` là file định dạng ví dụ, không tự nhập vào dữ liệu thật.

`queries/` là nguồn sqlc; generated code ở `backend/internal/platform/db`. Các repository query JSON còn lại chiếu rõ từng field, không expose `SELECT *` cho API.

Schema owner chạy migrations bằng `MIGRATION_DATABASE_URL`; runtime dùng `DATABASE_URL` với quyền dữ liệu trên schema ứng dụng. Tài khoản runtime không cần superuser/CREATEDB. Trên máy đã cấu hình role `hoanxu_app`; thông tin kết nối nằm trong `.env` bị loại khỏi Git.

Tiền BIGINT VND, xu số nguyên, tỷ lệ NUMERIC, timestamp UTC. Ngày nghiệp vụ theo Asia/Ho_Chi_Minh. Unique refs/idempotency và row locks bảo vệ tiền; trigger chặn sửa ledger/audit và kiểm tra bút toán cân bằng khi commit.

Database test phải kết thúc `_test`; tests tạo/xóa schema ngẫu nhiên riêng, không dùng DROP DATABASE hoặc reset schema public.
