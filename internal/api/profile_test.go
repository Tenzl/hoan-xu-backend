package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/users"
	"hoanxu/internal/wallet"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBankProfileEncryptionOwnershipAndWithdrawalSnapshot(t *testing.T) {
	store, uid, admin := testStore(t)
	ctx := context.Background()
	profile := &users.Service{Store: store}
	a := &auth.Service{Store: store}
	name := "Display name"
	bank := users.BankDetails{Bank: "Vietcombank", Account: "001234567890", Holder: "NGUYEN VAN AN"}
	if e := profile.Update(ctx, uid, "customer", users.ProfileInput{Name: &name, BankDetails: &bank}); e != nil {
		t.Fatal(e)
	}
	var encrypted string
	if e := store.Pool.QueryRow(ctx, `SELECT bank_details FROM users WHERE id=$1`, uid).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(encrypted, bank.Account) || strings.Contains(encrypted, bank.Holder) {
		t.Fatal("Bank data stored in plain text")
	}
	plain, e := store.Decrypt(encrypted)
	if e != nil || !strings.Contains(plain, bank.Account) {
		t.Fatal(plain, e)
	}
	var audit []byte
	if e = store.Pool.QueryRow(ctx, `SELECT payload FROM audit_logs WHERE action='bank_profile_updated'`).Scan(&audit); e != nil || strings.Contains(string(audit), bank.Account) || strings.Contains(string(audit), bank.Holder) {
		t.Fatal("Private data leaked to audit", e)
	}
	newName := "Should not commit"
	bad := users.BankDetails{Bank: "VCB", Account: "invalid", Holder: "Full name"}
	if e = profile.Update(ctx, uid, "customer", users.ProfileInput{Name: &newName, BankDetails: &bad}); e == nil {
		t.Fatal("invalid account number accepted")
	}
	var unchanged string
	if e = store.Pool.QueryRow(ctx, `SELECT name FROM users WHERE id=$1`, uid).Scan(&unchanged); e != nil || unchanged != name {
		t.Fatal("partial update committed", unchanged, e)
	}
	if e = profile.Update(ctx, admin, "admin", users.ProfileInput{BankDetails: &bank}); e == nil {
		t.Fatal("internal bank profile accepted")
	}
	tx, e := store.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	token, e := a.NewSession(ctx, tx, uid)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, uid, "profile-credit", "Test funds", 100000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	session, e := a.Session(ctx, token)
	if e != nil || session.BankDetails == nil || session.BankDetails.Account != bank.Account {
		t.Fatal(session, e)
	}
	srv := New(&Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	r := httptest.NewRequest("GET", "/api/v1/me", nil)
	r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), bank.Account) {
		t.Fatal(w.Code, w.Body.String())
	}
	withdrawal, e := (&wallet.Service{Store: store}).Withdraw(ctx, uid, "profile-withdraw", wallet.WithdrawalInput{Amount: 50000, Bank: bank.Bank, Account: bank.Account, Holder: bank.Holder})
	if e != nil {
		t.Fatal(e)
	}
	bank.Account = "009999999999"
	bank.Holder = "TRAN THI BINH"
	if e = profile.Update(ctx, uid, "customer", users.ProfileInput{BankDetails: &bank}); e != nil {
		t.Fatal(e)
	}
	var snapshot string
	if e = store.Pool.QueryRow(ctx, `SELECT bank_details FROM withdrawals WHERE id=$1`, withdrawal.(map[string]any)["id"]).Scan(&snapshot); e != nil {
		t.Fatal(e)
	}
	plain, e = store.Decrypt(snapshot)
	if e != nil || !strings.Contains(plain, "001234567890") || !strings.Contains(plain, "NGUYEN VAN AN") {
		t.Fatal("Withdrawal snapshot changed", plain, e)
	}
	if e = profile.Update(ctx, uid, "customer", users.ProfileInput{BankDetails: &users.BankDetails{}}); e != nil {
		t.Fatal(e)
	}
	session, e = a.Session(ctx, token)
	if e != nil || session.BankDetails != nil {
		t.Fatal("profile was not cleared", session, e)
	}
}
func TestEnglishErrorResponse(t *testing.T) {
	store, _, _ := testStore(t)
	srv := New(&Server{Store: store, Auth: &auth.Service{Store: store}, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	for _, language := range []string{"vi", "en-US", "en;q=0.9,vi;q=0.8"} {
		r := httptest.NewRequest("GET", "/api/v1/me", nil)
		r.Header.Set("Accept-Language", language)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		var body struct {
			Error struct{ Code, Message string }
		}
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		expected := "Vui lòng đăng nhập."
		if strings.HasPrefix(language, "en") {
			expected = "Please sign in."
		}
		if w.Code != 401 || body.Error.Code != "UNAUTHENTICATED" || body.Error.Message != expected {
			t.Fatal(w.Code, body)
		}
	}
}
