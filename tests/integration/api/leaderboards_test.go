package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"hoanxu/internal/auth"
	"hoanxu/internal/leaderboards"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
)

func boardOrder(t *testing.T, s *platform.Store, uid string, cash int64, ordered, approved time.Time) string {
	t.Helper()
	var id string
	err := s.Pool.QueryRow(context.Background(), `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at)
 SELECT $1,id,'shopee','board',$2,'one','Test',1000000,$3*2,$3,'approved','approved',$4,$5 FROM cashback_policies WHERE mode='fixed' ORDER BY created_at DESC LIMIT 1 RETURNING orders.id::text`, uid, platform.Token(), cash, ordered, approved).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLeaderboardRanksEveryoneByApprovalAndNetCashback(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	svc := &leaderboards.Service{Store: s}
	// Twelve eligible customers. The last one remains visible in the personal API.
	for i := 0; i < 11; i++ {
		var id string
		if err := s.Pool.QueryRow(ctx, `INSERT INTO users(name,role) VALUES($1,'customer') RETURNING id::text`, fmt.Sprintf("Member %d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		boardOrder(t, s, id, int64(12000-i*1000), now.AddDate(0, -1, 0), now.Add(-time.Hour))
	}
	ownOrder := boardOrder(t, s, uid, 500, now.AddDate(0, -1, 0), now.Add(-time.Hour))
	// Bought this week but approved before this week: not eligible for the current race.
	boardOrder(t, s, uid, 99999, now, now.AddDate(0, 0, -8))
	var blocked string
	if err := s.Pool.QueryRow(ctx, `INSERT INTO users(name,role,blocked) VALUES('Blocked','customer',true) RETURNING id::text`).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	boardOrder(t, s, blocked, 900000, now, now.Add(-time.Hour))
	boardOrder(t, s, admin, 900000, now, now.Add(-time.Hour))
	snapshot, err := svc.Read(ctx, "week", now, uid)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Participants != 12 || len(snapshot.Items) != 10 || snapshot.Items[0].Xu != 12000 || snapshot.Me.Rank == nil || *snapshot.Me.Rank != 12 || snapshot.Me.Xu != 500 || *snapshot.Me.XuToNext != 1501 {
		t.Fatalf("unexpected board: %+v me=%+v", snapshot, snapshot.Me)
	}
	// Withdrawal balances do not enter the ranking.
	if _, err = s.Pool.Exec(ctx, `UPDATE wallet_accounts SET balance=0 WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Read(ctx, "week", now, uid)
	if err != nil || after.Me.Xu != 500 {
		t.Fatal(after.Me, err)
	}
	// An adjustment changes the original period, not the approval timestamp.
	if _, err = s.Pool.Exec(ctx, `UPDATE orders SET cashback=13000 WHERE id=$1`, ownOrder); err != nil {
		t.Fatal(err)
	}
	after, err = svc.Read(ctx, "week", now, uid)
	if err != nil || *after.Me.Rank != 1 || *after.Me.Lead != 1000 {
		t.Fatal(after.Me, err)
	}
	all, err := svc.Read(ctx, "all", now, uid)
	if err != nil || all.Me.Xu != 112999 || all.StartsAt != nil || all.EndsAt != nil {
		t.Fatal(all.Me, err)
	}
}

func TestLeaderboardTiesAndAPIContracts(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(-time.Second)
	var other string
	if err := s.Pool.QueryRow(ctx, `INSERT INTO users(name,role) VALUES('Public name','customer') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	boardOrder(t, s, uid, 1000, now, now)
	boardOrder(t, s, other, 500, now, now)
	boardOrder(t, s, other, 500, now, now)
	svc := &leaderboards.Service{Store: s}
	snap, err := svc.Read(ctx, "all", time.Now(), uid)
	if err != nil || snap.Items[0].ID != other || *snap.Me.XuToNext != 1 {
		t.Fatal(snap, err)
	}
	boardOrder(t, s, uid, 0, now, now) // Tie in amount and order count uses stable IDs.
	snap, err = svc.Read(ctx, "all", time.Now(), uid)
	if err != nil {
		t.Fatal(err)
	}
	expected := uid
	if other < uid {
		expected = other
	}
	if snap.Items[0].ID != expected {
		t.Fatal("unstable tie", snap.Items)
	}
	a := &auth.Service{Store: s}
	srv := New(&Server{Store: s, Auth: a, Origin: "http://localhost:3000"})
	token := func(id string) string {
		tx, e := s.Pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		v, e := a.NewSession(ctx, tx, id)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		return v
	}
	customerToken, adminToken := token(uid), token(admin)
	for _, c := range []struct {
		path, token string
		status      int
	}{
		{"/api/v1/leaderboards?period=all", "", 200}, {"/api/v1/leaderboards?period=invalid", "", 422},
		{"/api/v1/me/leaderboard?period=all", "", 401}, {"/api/v1/me/leaderboard?period=all", customerToken, 200},
		{"/api/v1/me/leaderboard?period=all", adminToken, 403}, {"/api/v1/leaderboard", "", 200},
	} {
		req := httptest.NewRequest("GET", c.path, nil)
		if c.token != "" {
			req.AddCookie(&http.Cookie{Name: "hx_session", Value: c.token})
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != c.status {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("response must be private from caches")
		}
		if c.path == "/api/v1/leaderboards?period=all" {
			var payload struct {
				Data leaderboards.Board `json:"data"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Data.Items) != 2 {
				t.Fatal(payload)
			}
			var raw map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &raw)
			text := w.Body.String()
			for _, key := range []string{"email", "bankDetails", "csrfToken", "available"} {
				if containsJSONKey(text, key) {
					t.Fatal("private data leaked", key)
				}
			}
		}
	}
}

func containsJSONKey(text, key string) bool {
	var v any
	_ = json.Unmarshal([]byte(text), &v)
	var search func(any) bool
	search = func(value any) bool {
		switch x := value.(type) {
		case map[string]any:
			for k, v := range x {
				if k == key || search(v) {
					return true
				}
			}
		case []any:
			for _, v := range x {
				if search(v) {
					return true
				}
			}
		}
		return false
	}
	return search(v)
}

func TestApprovalTimeAtomicAndAdjustmentIsRejected(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	var id string
	err := s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,source_status,ordered_at) SELECT $1,id,'shopee','board','approval','one','Test',100000,2000,1000,'pending',now() FROM cashback_policies WHERE mode='fixed' ORDER BY created_at DESC LIMIT 1 RETURNING orders.id::text`, uid).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	svc := &orders.Service{Store: s}
	if _, err = svc.Event(ctx, admin, id, platform.Token(), orders.Event{Action: "approved"}); err == nil {
		t.Fatal("unapproved source accepted")
	}
	var stamp *time.Time
	if err = s.Pool.QueryRow(ctx, `SELECT approved_at FROM orders WHERE id=$1`, id).Scan(&stamp); err != nil || stamp != nil {
		t.Fatal(stamp, err)
	}
	_, err = s.Pool.Exec(ctx, `UPDATE orders SET source_status='approved' WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Event(ctx, admin, id, platform.Token(), orders.Event{Action: "approved"}); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	var credits int
	if err = s.Pool.QueryRow(ctx, `SELECT approved_at,(SELECT count(*) FROM wallet_transactions WHERE reference='order_credit:'||orders.id::text) FROM orders WHERE id=$1`, id).Scan(&first, &credits); err != nil || credits != 1 {
		t.Fatal(credits, err)
	}
	if _, err = svc.Event(ctx, admin, id, platform.Token(), orders.Event{Action: "adjustment", Reason: "Correct the statement"}); err == nil {
		t.Fatal(err)
	}
	var after time.Time
	if err = s.Pool.QueryRow(ctx, `SELECT approved_at FROM orders WHERE id=$1`, id).Scan(&after); err != nil || !after.Equal(first) {
		t.Fatal(after, first, err)
	}
}

func TestApprovalMigrationRequiresEvidenceAndBackfills(t *testing.T) {
	s, uid, _ := testStore(t)
	ctx := context.Background()
	down, err := os.ReadFile("../../database/migrations/000007_order_approval_time.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		var id string
		err = s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,ordered_at) SELECT $1,id,'shopee','migration',$2,'one','Test',100,10,5,'approved',now() FROM cashback_policies WHERE mode='fixed' ORDER BY created_at DESC LIMIT 1 RETURNING orders.id::text`, uid, fmt.Sprint(i)).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO order_events(order_id,action,created_at) VALUES($1,'approved','2026-10-01T01:00:00Z'),($1,'approved','2026-10-02T01:00:00Z')`, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO wallet_transactions(reference,description,created_at) VALUES('order_credit:'||$1::text,'test','2026-10-03T01:00:00Z')`, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../database/migrations/000007_order_approval_time.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, string(up)); err == nil {
		t.Fatal("migration invented an approval date for orphan")
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO order_events(order_id,action,created_at) VALUES($1,'approved','2026-10-04T01:00:00Z')`, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		var day int
		err = s.Pool.QueryRow(ctx, `SELECT extract(day FROM approved_at AT TIME ZONE 'UTC')::int FROM orders WHERE id=$1`, id).Scan(&day)
		if err != nil || day != []int{1, 3, 4}[i] {
			t.Fatal(day, err)
		}
	}
}

func TestLeaderboardCalendarFiltersAndUnrankedViewer(t *testing.T) {
	s, uid, _ := testStore(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, "2026-10-05T05:00:00Z")
	svc := &leaderboards.Service{Store: s}
	empty, err := svc.Read(ctx, "week", now, uid)
	if err != nil || len(empty.Items) != 0 || empty.Participants != 0 || empty.Me.Rank != nil || empty.Me.Xu != 0 || empty.Me.Target != nil {
		t.Fatal(empty, err)
	}
	for _, row := range []struct {
		date string
		cash int64
	}{
		{"2026-09-30T16:59:59Z", 100},
		{"2026-09-30T17:00:00Z", 200},
		{"2026-10-04T16:59:59Z", 400},
		{"2026-10-04T17:00:00Z", 800},
		{"2026-10-06T17:00:00Z", 1600}, // Future approval is not earned yet.
	} {
		stamp, _ := time.Parse(time.RFC3339, row.date)
		boardOrder(t, s, uid, row.cash, now.AddDate(0, -1, 0), stamp)
	}
	for _, row := range []struct {
		period string
		total  int64
	}{{"week", 800}, {"month", 1400}, {"all", 1500}} {
		board, err := svc.Read(ctx, row.period, now, uid)
		if err != nil || board.Me.Xu != row.total {
			t.Fatal(row.period, board.Me, err)
		}
	}
}
