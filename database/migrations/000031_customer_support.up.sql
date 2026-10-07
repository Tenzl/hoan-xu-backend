-- Replace known defaults only; preserve custom questions, answers and array order.
UPDATE app_settings SET settings=jsonb_set(settings,'{faq}',(
  SELECT jsonb_agg(CASE
    WHEN item->>'question'='Đơn bao lâu được ghi nhận?' AND item->>'answer'='Đơn hiển thị sau khi quản trị nhập báo cáo chuyển đổi từ sàn. Nếu thiếu đơn, liên hệ hỗ trợ kèm mã đơn và tracking.'
      THEN jsonb_set(item,'{answer}',to_jsonb('Đơn xuất hiện sau khi Hoàn Xu nhận được dữ liệu từ Shopee. Tạo link chưa đồng nghĩa với đơn đã được ghi nhận.'::text))
    WHEN item->>'question'='Khi nào có thể rút tiền?' AND item->>'answer' IN ('Chỉ tiền từ hoa hồng đã đối soát/duyệt và tiền đổi xu được bật mới vào số dư khả dụng.','Xu từ cashback đã duyệt và điểm danh vào ví khả dụng. Rút từ 50.000 Xu, theo bội số 1.000; khoản chờ duyệt và tạm giữ chưa thể rút.')
      THEN jsonb_set(item,'{answer}',to_jsonb('Chỉ rút từ Xu vàng khả dụng: tối thiểu 50.000, theo bội số 1.000 và không có khoản thiếu. Khoản chờ duyệt, đang giữ và Xu xanh không thể rút.'::text))
    WHEN item->>'question'='Xu dùng để làm gì?' AND item->>'answer' IN ('Xu dùng đổi voucher hoặc tiền khi quản trị bật quy đổi.','1 Xu = 1đ. Hoàn tiền và thưởng điểm danh cùng vào ví, dùng rút tiền hoặc đổi voucher.')
      THEN jsonb_set(item,'{answer}',to_jsonb('Xu vàng từ tiền hoàn đã duyệt có thể rút khi đủ điều kiện. Xu xanh từ điểm danh hoặc đổi Xu vàng dùng đổi quà. Đổi Xu vàng sang Xu xanh là một chiều.'::text))
    ELSE item END ORDER BY ord)
  FROM jsonb_array_elements(settings->'faq') WITH ORDINALITY AS entries(item,ord)
)) WHERE CASE WHEN jsonb_typeof(settings->'faq')='array' THEN jsonb_array_length(settings->'faq')>0 ELSE false END;
