package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hoanxu/internal/auth"
	"hoanxu/internal/leaderboards"
	"hoanxu/internal/platform"
)

func TestLegacyOrderBatch(t *testing.T) {
	store, fresh, admin := testStoreWithTiers(t, true)
	ctx := context.Background()
	var legacy string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Batch customer','','customer') RETURNING id::text`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	_, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin)
	if err != nil {
		t.Fatal(err)
	}
	a := &auth.Service{Store: store}
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
	principal, err := a.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&Server{Store: store, Auth: a, Origin: "http://localhost:3000"})
	request := func(owner, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/admin/users/"+owner+"/orders/batch", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", principal.CSRF)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	good := `{"orders":[{"productName":"Tai nghe","orderedAt":"2026-01-01T10:00:00+07:00","cashback":12000,"note":"Bổ sung"},{"productName":"Cốc","orderedAt":"2026-01-02T11:00:00+07:00","cashback":8000}]}`
	invalid := strings.Replace(good, `"cashback":8000`, `"cashback":0`, 1)
	if w := request(legacy, invalid, "batch-invalid"); w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(fresh, good, "batch-fresh"); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i, body := range []string{`{"orders":[]}`, `{"orders":null}`, strings.Replace(good, "+07:00", "", 1), strings.Replace(good, "12000", "12.3", 1), strings.Replace(good, "2026-01-01", "2099-01-01", 1), strings.Replace(good, `"note":"Bổ sung"`, `"unknown":"no"`, 1)} {
		if w := request(legacy, body, fmt.Sprintf("batch-bad-%d", i)); w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// A database failure on the second order rolls back the first order AND its credit.
	if _, err = store.Pool.Exec(ctx, `CREATE FUNCTION fail_second_batch_order() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.product_name='Cốc' THEN RAISE EXCEPTION 'forced batch failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_batch BEFORE INSERT ON orders FOR EACH ROW EXECUTE FUNCTION fail_second_batch_order()`); err != nil {
		t.Fatal(err)
	}
	if w := request(legacy, good, "batch-rollback"); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	var rollbackCount, rollbackEntries int
	if err = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM orders WHERE user_id=$1),(SELECT count(*) FROM wallet_entries)`, legacy).Scan(&rollbackCount, &rollbackEntries); err != nil || rollbackCount != 0 || rollbackEntries != 0 {
		t.Fatal(rollbackCount, rollbackEntries, err)
	}
	if _, err = store.Pool.Exec(ctx, `DROP TRIGGER fail_batch ON orders; DROP FUNCTION fail_second_batch_order()`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=NULL WHERE id=$1`, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	if w := request(legacy, good, "batch-no-reauth"); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := request(legacy, good, "batch-retry"); w.Code != 201 {
				t.Error(w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	var count, balance int64
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id=$1`, legacy).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, legacy).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if count != 2 || balance != 20000 {
		t.Fatal(count, balance)
	}
	if w := request(legacy, strings.Replace(good, "12000", "13000", 1), "batch-retry"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	var payload struct {
		Data []struct {
			ID       string
			WeekRank *int64 `json:"weekRank"`
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/admin/users?kind=legacy", nil)
	r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if err = json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].WeekRank == nil || *payload.Data[0].WeekRank != 1 {
		t.Fatal(w.Body.String())
	}
	// Rankings are global before kind filtering and pagination; order date is irrelevant.
	boardOrder(t, store, fresh, 30000, time.Now().AddDate(0, -2, 0), time.Now().Add(-time.Minute))
	r = httptest.NewRequest("GET", "/api/v1/admin/users?kind=legacy&perPage=1", nil)
	r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if err = json.Unmarshal(w.Body.Bytes(), &payload); err != nil || len(payload.Data) != 1 || *payload.Data[0].WeekRank != 2 {
		t.Fatal(w.Body.String(), err)
	}
	r = httptest.NewRequest("GET", "/api/v1/admin/users/"+legacy, nil)
	r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var detail struct {
		Data struct{ WeekRank, MonthRank *int64 }
	}
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.Data.WeekRank == nil || *detail.Data.WeekRank != 2 || detail.Data.MonthRank == nil {
		t.Fatal(w.Body.String(), err)
	}
}

func TestWeeklyPrizesReserveSettleAndDeliver(t *testing.T) {
	store, user, admin := testStoreWithTiers(t, true)
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	start, end, _ := leaderboards.Bounds("week", now)
	_, err := store.Pool.Exec(ctx, `INSERT INTO gift_catalog(id,name,cost,stock,active) VALUES('weekly-gift','Quà tuần',100,8,true)`)
	if err != nil {
		t.Fatal(err)
	}
	svc := &leaderboards.PrizeService{Store: store}
	input := leaderboards.CampaignInput{WeekStart: *start, GiftID: "weekly-gift", Enabled: true, Title: "Top 5 tuần nhận quà", Description: "Mỗi người một quà"}
	result, err := svc.Save(ctx, admin, "weekly-config", input, now)
	if err != nil {
		t.Fatal(err)
	}
	campaign := result.(leaderboards.Campaign)
	// Retrying activation must not reserve another five gifts.
	if _, err = svc.Save(ctx, admin, "weekly-config", input, now); err != nil {
		t.Fatal(err)
	}
	var stock int
	if err = store.Pool.QueryRow(ctx, `SELECT stock FROM gift_catalog WHERE id='weekly-gift'`).Scan(&stock); err != nil || stock != 3 {
		t.Fatal(stock, err)
	}
	for i := 0; i < 2; i++ {
		owner := user
		if i == 1 {
			if err = store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Legacy winner','','customer') RETURNING id::text`).Scan(&owner); err != nil {
				t.Fatal(err)
			}
		}
		_, err = store.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1,id,'shopee','test',$2,'1','Test',10000,10000,$3,'approved','approved',$4,$4 FROM cashback_policies WHERE mode='fixed' LIMIT 1`, owner, fmt.Sprint(i), 10000-i*1000, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	preview, err := svc.Preview(ctx, campaign.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Settle(ctx, admin, "weekly-early", campaign.ID, preview.Hash, now); err == nil {
		t.Fatal("settled before week ended")
	}
	closed := end.Add(time.Second)
	preview, err = svc.Preview(ctx, campaign.ID, closed)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Winners) != 2 {
		t.Fatal(preview)
	}
	if _, err = svc.Settle(ctx, admin, "weekly-stale", campaign.ID, "stale", closed); err == nil {
		t.Fatal("stale preview accepted")
	}
	if _, err = svc.Settle(ctx, admin, "weekly-settle", campaign.ID, preview.Hash, closed); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Settle(ctx, admin, "weekly-settle", campaign.ID, preview.Hash, closed); err != nil {
		t.Fatal(err)
	}
	awards, err := svc.Awards(ctx, campaign.ID, "")
	if err != nil || len(awards) != 2 {
		t.Fatal(awards, err)
	}
	if _, err = svc.Deliver(ctx, admin, "weekly-deliver", awards[0].ID, "VOUCHER-PRIVATE"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Deliver(ctx, admin, "weekly-deliver", awards[0].ID, "VOUCHER-PRIVATE"); err != nil {
		t.Fatal(err)
	}
	private, err := svc.Awards(ctx, "", user)
	if err != nil || len(private) != 1 || private[0].DeliveryNote != "VOUCHER-PRIVATE" {
		t.Fatal(private, err)
	}
	var encrypted string
	if err = store.Pool.QueryRow(ctx, `SELECT delivery_cipher FROM weekly_prize_awards WHERE id=$1`, awards[0].ID).Scan(&encrypted); err != nil || strings.Contains(encrypted, "VOUCHER-PRIVATE") {
		t.Fatal("plaintext delivery", err)
	}
	var leaks int
	if err = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_logs WHERE payload::text LIKE '%VOUCHER-PRIVATE%')+(SELECT count(*) FROM idempotency_records WHERE response::text LIKE '%VOUCHER-PRIVATE%')+(SELECT count(*) FROM notifications WHERE body LIKE '%VOUCHER-PRIVATE%')`).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("private information leaked", leaks, err)
	}
	// Different keys must also be harmless once settlement/delivery already succeeded.
	if _, err = svc.Settle(ctx, admin, "weekly-settle-new-key", campaign.ID, preview.Hash, closed); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Deliver(ctx, admin, "weekly-deliver-new-key", awards[0].ID, "REPLACEMENT"); err != nil {
		t.Fatal(err)
	}
	private, err = svc.Awards(ctx, "", user)
	if err != nil || private[0].DeliveryNote != "VOUCHER-PRIVATE" {
		t.Fatal(private, err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE users SET blocked=true WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	fixed, err := svc.Preview(ctx, campaign.ID, closed)
	if err != nil || len(fixed.Winners) != 2 || fixed.Winners[0].ID != user {
		t.Fatal("history changed", fixed, err)
	}
	if err = store.Pool.QueryRow(ctx, `SELECT stock FROM gift_catalog WHERE id='weekly-gift'`).Scan(&stock); err != nil || stock != 6 {
		t.Fatal(stock, err)
	}
	var entries int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM wallet_entries`).Scan(&entries); err != nil || entries != 0 {
		t.Fatal("prizes must not debit wallets", entries, err)
	}
	// Reconfiguration after settlement is prohibited and snapshots stay fixed.
	input.Version = campaign.Version
	input.Enabled = false
	if _, err = svc.Save(ctx, admin, "weekly-edit-ended", input, closed); err == nil {
		t.Fatal("ended campaign changed")
	}
}

func TestWeeklyPrizesInventoryVersionsAndStaleWinners(t *testing.T) {
	store, uid, admin := testStoreWithTiers(t, true)
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	start, end, _ := leaderboards.Bounds("week", now)
	svc := &leaderboards.PrizeService{Store: store}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO gift_catalog(id,name,cost,stock,active) VALUES('a','Gift A',100,6,true),('b','Gift B',100,4,true)`); err != nil {
		t.Fatal(err)
	}
	input := leaderboards.CampaignInput{WeekStart: *start, GiftID: "b", Enabled: true, Title: "Weekly", Description: "Gift"}
	if _, err := svc.Save(ctx, admin, "prize-low-stock", input, now); err == nil {
		t.Fatal("insufficient stock accepted")
	}
	input.GiftID = "a"
	result, err := svc.Save(ctx, admin, "prize-create", input, now)
	if err != nil {
		t.Fatal(err)
	}
	c := result.(leaderboards.Campaign)
	if _, err = svc.Save(ctx, admin, "prize-stale-version", input, now); err == nil {
		t.Fatal("stale version accepted")
	}
	input.Version = c.Version
	input.GiftID = "b"
	if _, err = svc.Save(ctx, admin, "prize-switch-insufficient", input, now); err == nil {
		t.Fatal("insufficient switch accepted")
	}
	checkStock := func(a, b int) {
		t.Helper()
		var sa, sb int
		if err := store.Pool.QueryRow(ctx, `SELECT (SELECT stock FROM gift_catalog WHERE id='a'),(SELECT stock FROM gift_catalog WHERE id='b')`).Scan(&sa, &sb); err != nil || sa != a || sb != b {
			t.Fatal(sa, sb, err)
		}
	}
	checkStock(1, 4)
	if _, err = store.Pool.Exec(ctx, `UPDATE gift_catalog SET stock=7 WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	result, err = svc.Save(ctx, admin, "prize-switch", input, now)
	if err != nil {
		t.Fatal(err)
	}
	c = result.(leaderboards.Campaign)
	checkStock(6, 2)
	input.Version = c.Version
	input.Enabled = false
	result, err = svc.Save(ctx, admin, "prize-disable", input, now)
	if err != nil {
		t.Fatal(err)
	}
	c = result.(leaderboards.Campaign)
	checkStock(6, 7)
	input.Version = c.Version
	input.Enabled = true
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := input
			p.Title = fmt.Sprint("Race", i)
			_, e := svc.Save(ctx, admin, fmt.Sprint("prize-config-race", i), p, now)
			errors <- e
		}(i)
	}
	wg.Wait()
	close(errors)
	success := 0
	for err := range errors {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("expected one version winner", success)
	}
	checkStock(6, 2)
	boardOrder(t, store, uid, 10000, now.AddDate(0, -1, 0), now)
	p, err := svc.Preview(ctx, c.ID, end.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	boardOrder(t, store, uid, 9000, now, now)
	if _, err = svc.Settle(ctx, admin, "prize-changed-winners", c.ID, p.Hash, end.Add(time.Second)); err == nil {
		t.Fatal("changed ranking accepted")
	}
	p, err = svc.Preview(ctx, c.ID, end.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Settle(ctx, admin, "prize-final", c.ID, p.Hash, end.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	checkStock(6, 6)
	// Next-week configuration stays private until the exact Vietnam week boundary.
	next := input
	next.WeekStart = *end
	next.Version = 0
	next.GiftID = "a"
	if _, err = svc.Save(ctx, admin, "prize-next", next, now); err != nil {
		t.Fatal(err)
	}
	public, err := svc.Current(ctx, now)
	if err != nil || public != nil {
		t.Fatal("future program advertised", public, err)
	}
	public, err = svc.Current(ctx, *end)
	if err != nil || public == nil || public.GiftID != "a" {
		t.Fatal(public, err)
	}
	p, err = svc.Preview(ctx, public.ID, end.AddDate(0, 0, 7))
	if err != nil || len(p.Winners) != 0 {
		t.Fatal(p, err)
	}
	if _, err = svc.Settle(ctx, admin, "prize-empty", public.ID, p.Hash, end.AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}
	checkStock(6, 6)
}

func TestWeeklyPrizeAPIPermissionsAndPrivateAwards(t *testing.T) {
	store, uid, admin := testStoreWithTiers(t, true)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	srv := New(&Server{Store: store, Auth: a, Origin: "http://localhost:3000"})
	session := func(id string) (string, string) {
		t.Helper()
		_, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, id)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		token, err := a.NewSession(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		p, err := a.Session(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		return token, p.CSRF
	}
	token, csrf := session(admin)
	userToken, _ := session(uid)
	noGift, err := auth.CreateInternal(ctx, store, admin, "prize-staff", "Staff", "prize-staff-password", "staff", []string{"users", "orders"})
	if err != nil {
		t.Fatal(err)
	}
	staffToken, _ := session(noGift)
	request := func(path, method, token, csrf, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", platform.Token())
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	for _, tk := range []string{userToken, staffToken} {
		if w := request("/admin/leaderboard-prizes", "GET", tk, "", ""); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	start, _, _ := leaderboards.Bounds("week", time.Now())
	body := fmt.Sprintf(`{"weekStart":%q,"giftId":"g","enabled":true,"title":"Top","description":"","version":0}`, start.Format(time.RFC3339))
	if w := request("/admin/leaderboard-prizes", "POST", token, "wrong", body); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("/admin/leaderboard-prizes", "POST", token, csrf, body); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A recent staff session without BOTH customer permissions cannot batch-import.
	if _, err = store.Pool.Exec(ctx, `DELETE FROM user_permissions WHERE user_id=$1 AND permission='orders'`, noGift); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE user_id=$1`, noGift); err != nil {
		t.Fatal(err)
	}
	principal, err := a.Session(ctx, staffToken)
	if err != nil {
		t.Fatal(err)
	}
	if w := request("/admin/users/"+uid+"/orders/batch", "POST", staffToken, principal.CSRF, `{"orders":[]}`); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	svc := &leaderboards.PrizeService{Store: store}
	past := time.Now().AddDate(0, 0, -14)
	week, ending, _ := leaderboards.Bounds("week", past)
	if _, err = store.Pool.Exec(ctx, `INSERT INTO gift_catalog(id,name,cost,stock) VALUES('g','Prize',100,5)`); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Save(ctx, admin, "private-config", leaderboards.CampaignInput{WeekStart: *week, GiftID: "g", Enabled: true, Title: "Top"}, past)
	if err != nil {
		t.Fatal(err)
	}
	c := result.(leaderboards.Campaign)
	boardOrder(t, store, uid, 1000, past, past)
	p, err := svc.Preview(ctx, c.ID, *ending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Settle(ctx, admin, "private-settle", c.ID, p.Hash, *ending); err != nil {
		t.Fatal(err)
	}
	awards, err := svc.Awards(ctx, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Deliver(ctx, admin, "private-deliver", awards[0].ID, "SECRET-CODE"); err != nil {
		t.Fatal(err)
	}
	if w := request("/me/leaderboard-awards?userId="+uid, "GET", staffToken, "", ""); w.Code != 403 || strings.Contains(w.Body.String(), "SECRET-CODE") {
		t.Fatal(w.Body.String())
	}
	var other string
	if err = store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Other','other-prize@example.com','customer') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	otherToken, _ := session(other)
	if w := request("/me/leaderboard-awards?userId="+uid, "GET", otherToken, "", ""); w.Code != 200 || strings.Contains(w.Body.String(), "SECRET-CODE") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("/me/leaderboard-awards", "GET", userToken, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "SECRET-CODE") {
		t.Fatal(w.Body.String())
	}
	if w := request("/leaderboard-prizes/current", "GET", "", "", ""); w.Code != 200 || strings.Contains(w.Body.String(), "SECRET-CODE") {
		t.Fatal(w.Body.String())
	}
}
