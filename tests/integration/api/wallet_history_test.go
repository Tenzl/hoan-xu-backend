package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/auth"
	"hoanxu/internal/wallet"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWalletHistoryIncludesDebtAndHeldMovements(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = wallet.Post(ctx, tx, "history-debt", "Debt", []wallet.Entry{{User: customer, Kind: "debt", Amount: 100}, {Kind: "system", Amount: -100}}); err != nil {
		t.Fatal(err)
	}
	if err = wallet.Credit(ctx, tx, customer, "history-repay", "Điểm danh", 300); err != nil {
		t.Fatal(err)
	}
	if err = wallet.Post(ctx, tx, "history-hold", "Giữ Xu đổi quà", []wallet.Entry{{User: customer, Kind: "available", Amount: -200}, {User: customer, Kind: "gift_held", Amount: 200}}); err != nil {
		t.Fatal(err)
	}
	if err = wallet.Post(ctx, tx, "history-paid", "Đã cấp voucher", []wallet.Entry{{User: customer, Kind: "gift_held", Amount: -200}, {Kind: "system", Amount: 200}}); err != nil {
		t.Fatal(err)
	}
	a := &auth.Service{Store: s}
	token, err := a.NewSession(ctx, tx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	srv := New(&Server{Store: s, Auth: a, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	r := httptest.NewRequest("GET", "/api/v1/wallet/transactions?perPage=20", nil)
	r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 4 {
		t.Fatal("missing movements", body.Data)
	}
	for _, row := range body.Data {
		if _, ok := row["debtAmount"]; !ok {
			t.Fatal("debt delta omitted", row)
		}
		switch row["description"] {
		case "Điểm danh":
			if row["amount"] != float64(200) || row["debtAmount"] != float64(-100) {
				t.Fatal(row)
			}
		case "Đã cấp voucher":
			if row["amount"] != float64(0) || row["giftHeldAmount"] != float64(-200) {
				t.Fatal(row)
			}
		}
	}
	assertLedger(t, s)
}
