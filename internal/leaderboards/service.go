package leaderboards

import (
	"context"
	"encoding/json"
	"time"
	_ "time/tzdata"

	"hoanxu/internal/platform"
)

type Entry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Rank   int64  `json:"rank"`
	Xu     int64  `json:"xu"`
	Orders int64  `json:"orders"`
}
type PeriodInfo struct {
	Period       string     `json:"period"`
	StartsAt     *time.Time `json:"startsAt"`
	EndsAt       *time.Time `json:"endsAt"`
	AsOf         time.Time  `json:"asOf"`
	Participants int64      `json:"participants"`
}
type Board struct {
	PeriodInfo
	Items []Entry `json:"items"`
}
type Position struct {
	Rank     *int64 `json:"rank"`
	Xu       int64  `json:"xu"`
	Orders   int64  `json:"orders"`
	Target   *Entry `json:"target"`
	XuToNext *int64 `json:"xuToNext"`
	Lead     *int64 `json:"lead"`
}
type Personal struct {
	PeriodInfo
	Position
}
type Snapshot struct {
	Board
	Me *Position `json:"me"`
}
type Service struct{ Store *platform.Store }

// Bounds uses calendar periods in Vietnam, independent of the server timezone.
func Bounds(period string, now time.Time) (*time.Time, *time.Time, error) {
	if period == "all" {
		return nil, nil, nil
	}
	loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		return nil, nil, err
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	var end time.Time
	switch period {
	case "week":
		start = start.AddDate(0, 0, -(int(local.Weekday())+6)%7)
		end = start.AddDate(0, 0, 7)
	case "month":
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	default:
		return nil, nil, platform.Fail(422, "INVALID_PERIOD", "Kỳ đua không hợp lệ. Chọn tuần, tháng hoặc toàn bộ.")
	}
	return &start, &end, nil
}

const rankingSQL = `WITH totals AS (
 SELECT u.id,u.name,sum(o.cashback)::bigint AS xu,count(*) AS orders
 FROM orders o JOIN users u ON u.id=o.user_id
 WHERE o.status='approved' AND u.role='customer' AND NOT u.blocked
 AND ($1::timestamptz IS NULL OR o.approved_at >= $1)
 AND ($2::timestamptz IS NULL OR o.approved_at < $2)
 AND o.approved_at <= $4::timestamptz
 GROUP BY u.id HAVING sum(o.cashback)>0
), ranked AS (
 SELECT *,row_number() OVER(ORDER BY xu DESC,orders DESC,id) AS rank FROM totals
), viewer AS (
 SELECT r.rank,coalesce(r.xu,0) AS xu,coalesce(r.orders,0) AS orders
 FROM (SELECT $3::uuid AS id) v LEFT JOIN ranked r ON r.id=v.id
), target AS (
 SELECT r.* FROM ranked r CROSS JOIN viewer v
 WHERE (v.rank>1 AND r.rank=v.rank-1) OR v.rank IS NULL
 ORDER BY r.rank DESC LIMIT 1
)
SELECT jsonb_build_object(
 'participants',(SELECT count(*) FROM ranked),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(top) ORDER BY top.rank) FROM (SELECT * FROM ranked ORDER BY rank LIMIT 10) top),'[]'::jsonb),
 'me',CASE WHEN $3::uuid IS NULL THEN NULL ELSE (
 SELECT jsonb_build_object('rank',v.rank,'xu',v.xu,'orders',v.orders,
 'target',(SELECT to_jsonb(t) FROM target t),
 'xuToNext',(SELECT t.xu-v.xu+1 FROM target t),
 'lead',CASE WHEN v.rank=1 THEN (SELECT v.xu-r.xu FROM ranked r WHERE r.rank=2) ELSE NULL END)
 FROM viewer v) END
)`

// Read aggregates the entire ranking once; viewer rank is not restricted to top 10.
func (s *Service) Read(ctx context.Context, period string, now time.Time, userID string) (Snapshot, error) {
	start, end, err := Bounds(period, now)
	if err != nil {
		return Snapshot{}, err
	}
	var viewer any
	if userID != "" {
		viewer = userID
	}
	raw, err := s.Store.One(ctx, rankingSQL, start, end, viewer, now)
	if err != nil {
		return Snapshot{}, err
	}
	var result Snapshot
	if err = json.Unmarshal(raw, &result); err != nil {
		return Snapshot{}, err
	}
	result.PeriodInfo = PeriodInfo{Period: period, StartsAt: start, EndsAt: end, AsOf: now, Participants: result.Participants}
	return result, nil
}
