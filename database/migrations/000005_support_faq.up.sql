UPDATE app_settings SET settings=jsonb_set(settings,'{faq}','[
 {"question":"Đơn bao lâu được ghi nhận?","answer":"Đơn hiển thị sau khi quản trị nhập báo cáo chuyển đổi từ sàn. Nếu thiếu đơn, liên hệ hỗ trợ kèm mã đơn và tracking."},
 {"question":"Khi nào có thể rút tiền?","answer":"Chỉ tiền từ hoa hồng đã đối soát/duyệt và tiền đổi xu được bật mới vào số dư khả dụng."},
 {"question":"Vì sao số dự kiến và thực nhận khác nhau?","answer":"Sàn có thể điều chỉnh giá trị, giới hạn hoa hồng hoặc từ chối đơn hủy/hoàn."},
 {"question":"Xu dùng để làm gì?","answer":"Xu dùng đổi voucher hoặc tiền khi quản trị bật quy đổi."}
]'::jsonb) WHERE NOT settings ? 'faq';
