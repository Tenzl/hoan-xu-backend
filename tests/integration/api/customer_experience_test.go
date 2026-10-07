package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"hoanxu/internal/auth"
)

func TestCommunityLikesFollowViewer(t *testing.T) {
	store, uid, _ := testStore(t)
	ctx := context.Background()
	var id string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO deals(user_id,channel,body) VALUES($1,'shopee','Offer') RETURNING id`, uid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO deal_likes(deal_id,user_id) VALUES($1,$2)`, id, uid); err != nil {
		t.Fatal(err)
	}
	a := &auth.Service{Store: store}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.NewSession(ctx, tx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	server := New(&Server{Store: store, Auth: a})
	for _, signedIn := range []bool{false, true} {
		req := httptest.NewRequest("GET", "/api/v1/deals", nil)
		if signedIn {
			req.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		var body struct {
			Data []struct {
				Likes int
				Liked bool
			}
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Data) != 1 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if body.Data[0].Likes != 1 || body.Data[0].Liked != signedIn {
			t.Fatalf("viewer=%v: %+v", signedIn, body.Data)
		}
	}
}

func TestCustomerFAQMigrationPreservesCustomAnswers(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	_, err := store.Pool.Exec(ctx, `UPDATE app_settings SET settings=jsonb_set(settings,'{faq}','[{"question":"Đơn bao lâu được ghi nhận?","answer":"Đơn hiển thị sau khi quản trị nhập báo cáo chuyển đổi từ sàn. Nếu thiếu đơn, liên hệ hỗ trợ kèm mã đơn và tracking."},{"question":"Khi nào có thể rút tiền?","answer":"Câu trả lời riêng của quản trị"}]'::jsonb)`)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile("../../database/migrations/000031_customer_support.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err = store.Pool.QueryRow(ctx, `SELECT settings#>>'{faq,0,answer}',settings#>>'{faq,1,answer}' FROM app_settings`).Scan(&first, &second); err != nil {
		t.Fatal(err)
	}
	if first != "Đơn xuất hiện sau khi Hoàn Xu nhận được dữ liệu từ Shopee. Tạo link chưa đồng nghĩa với đơn đã được ghi nhận." || second != "Câu trả lời riêng của quản trị" {
		t.Fatal(first, second)
	}
}
