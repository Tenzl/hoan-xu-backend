package api

import (
	"context"
	"encoding/json"
	"errors"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/platform"
	"hoanxu/internal/shopeeconfig"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShopeeVerificationRequiresCurrentProofAndNeverWritesFinancialData(t *testing.T) {
	store, _, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	if _, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.NewSession(ctx, tx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := a.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, u.SessionID); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000"}
	handler := New(server)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(raw)))
		r.Header.Set("Origin", server.Origin)
		r.Header.Set("X-CSRF-Token", u.CSRF)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	fields := shopeeconfig.Default().Fields
	fields.Publisher = "123456789"
	save := func(version string, enabled bool) *httptest.ResponseRecorder {
		copy := fields
		copy.Enabled = enabled
		return request("PUT", "/admin/browser/settings", shopeeconfig.Input{Fields: copy, Version: version})
	}
	if w := save("0:0", false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cfg, err := shopeeconfig.Load(ctx, store, server.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if w := save(cfg.Version, true); w.Code != 409 {
		t.Fatal("unverified config enabled", w.Code, w.Body.String())
	}
	ready := make(chan struct{})
	server.VerifyShopee = func(c context.Context, _ shopeeconfig.Config, _ string) error {
		close(ready)
		<-c.Done()
		return c.Err()
	}
	start := func(version string) *httptest.ResponseRecorder {
		return request("POST", "/admin/browser/verifications", map[string]string{"version": version, "productUrl": "https://shopee.vn/product/83496725/6939920023"})
	}
	w := start(cfg.Version)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct{ Data shopeeconfig.Verification }
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not started")
	}
	if w = start(cfg.Version); w.Code != 409 {
		t.Fatal("parallel verification accepted", w.Code, w.Body.String())
	}
	fields.PriceScale = 1
	if w = save(cfg.Version, false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	poll := func(id string, status string) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			v, e := shopeeconfig.GetVerification(ctx, store, server.Origin, id)
			if e != nil {
				t.Fatal(e)
			}
			if v.Status == status {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("verification did not finish", status)
	}
	poll(response.Data.ID, "cancelled")
	cfg, err = shopeeconfig.Load(ctx, store, server.Origin)
	if err != nil || cfg.TrackingVerified {
		t.Fatal("stale proof accepted", cfg, err)
	}
	server.VerifyShopee = func(context.Context, shopeeconfig.Config, string) error {
		return errors.New("fixture upstream failure")
	}
	w = start(cfg.Version)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	poll(response.Data.ID, "failed")
	if w = save(cfg.Version, true); w.Code != 409 {
		t.Fatal("failure enabled config", w.Code)
	}
	server.VerifyShopee = func(context.Context, shopeeconfig.Config, string) error { return nil }
	w = start(cfg.Version)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	poll(response.Data.ID, "succeeded")
	verified, err := shopeeconfig.Load(ctx, store, server.Origin)
	if err != nil || !verified.SchemaVerified || !verified.TrackingVerified || verified.Enabled || verified.Version == cfg.Version {
		t.Fatal("proof missing", verified, err)
	}
	if w = save(verified.Version, true); w.Code != 200 {
		t.Fatal("verified config not enabled", w.Code, w.Body.String())
	}
	if !server.Affiliate.CheckEnabled() {
		t.Fatal("enabled state not applied")
	}
	fields.Publisher = "999999999"
	current, _ := shopeeconfig.Load(ctx, store, server.Origin)
	if w = save(current.Version, true); w.Code != 409 {
		t.Fatal("publisher change kept proof", w.Code)
	}
	var n int
	if err = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM affiliate_links)+(SELECT count(*) FROM orders)+(SELECT count(*) FROM wallet_entries)`).Scan(&n); err != nil || n != 0 {
		t.Fatal("diagnostics wrote financial data", n, err)
	}
	if _, err = shopeeconfig.GetVerification(ctx, store, "https://another.example", response.Data.ID); err == nil {
		t.Fatal("job leaked to another origin")
	}
	if w = request("PUT", "/admin/browser/publisher", map[string]string{"publisher": "123"}); w.Code != 410 {
		t.Fatal("old publisher path accepted", w.Code)
	}
	if w = request("PATCH", "/admin/affiliate-channels/shopee", map[string]string{"status": "available"}); w.Code != 410 {
		t.Fatal("old channel path accepted", w.Code)
	}
	// A successful proof from one origin cannot enable another local/remote profile.
	other := shopeeconfig.Default().Fields
	other.Publisher = current.Publisher
	other.Enabled = true
	if _, err = shopeeconfig.Save(ctx, store, "https://another.example", admin, shopeeconfig.Input{Fields: other, Version: "0:1"}); err == nil {
		t.Fatal("proof reused across origins")
	}
	var problem *platform.Error
	if !errors.As(err, &problem) || problem.Code != "TRACKING_NOT_VERIFIED" {
		t.Fatal(err)
	}
}

func TestShopeeConfigurationConcurrentSavesRollbackExpiryAndRecovery(t *testing.T) {
	store, _, admin := testStore(t)
	ctx := context.Background()
	origin := "http://localhost:3000"
	fields := shopeeconfig.Default().Fields
	fields.Publisher = "123456789"
	cfg, err := shopeeconfig.Save(ctx, store, origin, admin, shopeeconfig.Input{Fields: fields, Version: "0:0"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := shopeeconfig.Save(ctx, store, origin, admin, shopeeconfig.Input{Fields: fields, Version: cfg.Version})
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
			continue
		}
		var p *platform.Error
		if !errors.As(e, &p) || p.Code != "SHOPEE_SETTINGS_CONFLICT" {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("concurrent saves overwrote", success)
	}
	cfg, err = shopeeconfig.Load(ctx, store, origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `CREATE FUNCTION reject_shopee_save() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_shopee_save BEFORE UPDATE ON affiliate_channels FOR EACH ROW EXECUTE FUNCTION reject_shopee_save()`); err != nil {
		t.Fatal(err)
	}
	changed := fields
	changed.Publisher = "987654321"
	changed.PriceScale = 1
	if _, err = shopeeconfig.Save(ctx, store, origin, admin, shopeeconfig.Input{Fields: changed, Version: cfg.Version}); err == nil {
		t.Fatal("database failure accepted")
	}
	after, e := shopeeconfig.Load(ctx, store, origin)
	if e != nil || after.Version != cfg.Version || after.Publisher != cfg.Publisher || after.PriceScale != cfg.PriceScale {
		t.Fatal("save partially applied", after, e)
	}
	if _, err = store.Pool.Exec(ctx, `DROP TRIGGER reject_shopee_save ON affiliate_channels; DROP FUNCTION reject_shopee_save()`); err != nil {
		t.Fatal(err)
	}
	job, _, err := shopeeconfig.StartVerification(ctx, store, origin, admin, cfg.Version, "https://shopee.vn/product/1/2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE shopee_verifications SET expires_at=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = shopeeconfig.FinishVerification(ctx, store, origin, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	result, err := shopeeconfig.GetVerification(ctx, store, origin, job.ID)
	if err != nil || result.Status != "cancelled" {
		t.Fatal(result, err)
	}
	after, _ = shopeeconfig.Load(ctx, store, origin)
	if after.TrackingVerified {
		t.Fatal("late result enabled proof")
	}
	job, _, err = shopeeconfig.StartVerification(ctx, store, origin, admin, cfg.Version, "https://shopee.vn/product/1/2")
	if err != nil {
		t.Fatal(err)
	}
	if err = shopeeconfig.RecoverVerifications(ctx, store, origin); err != nil {
		t.Fatal(err)
	}
	if err = shopeeconfig.FinishVerification(ctx, store, origin, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	result, err = shopeeconfig.GetVerification(ctx, store, origin, job.ID)
	if err != nil || result.Status != "cancelled" || result.ErrorCode == nil || *result.ErrorCode != "BACKEND_RESTARTED" {
		t.Fatal(result, err)
	}
}
