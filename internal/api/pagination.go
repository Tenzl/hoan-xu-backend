package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hoanxu/internal/platform"
	"net/http"
	"strings"
	"time"
)

type cursor struct {
	Time string `json:"time"`
	ID   string `json:"id"`
}
type paging struct {
	Columns, Field string
	LimitIndex     int
}

var cursorRoutes = map[string]paging{
	"/api/v1/orders":              {"o.ordered_at,o.id", "orderedAt", 1},
	"/api/v1/affiliate-links":     {"created_at,id", "createdAt", 1},
	"/api/v1/coins/transactions":  {"created_at,id", "createdAt", 1},
	"/api/v1/wallet/transactions": {"t.created_at,t.id", "createdAt", 1},
	"/api/v1/notifications":       {"n.created_at,n.id", "createdAt", 1},
	"/api/v1/deals":               {"d.created_at,d.id", "createdAt", 0},
	"/api/v1/withdrawals":         {"w.created_at,w.id", "createdAt", 1},
	"/api/v1/gift-redemptions":    {"r.created_at,r.id", "createdAt", 1},
}

func (s *Server) pageRows(r *http.Request, q string, args ...any) ([]json.RawMessage, error) {
	p, ok := cursorRoutes[r.URL.Path]
	if !ok {
		return s.Store.Rows(r.Context(), q, args...)
	}
	params := append([]any{}, args...)
	limit := params[p.LimitIndex].(int)
	params[p.LimitIndex] = limit + 1
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		if len(encoded) > 512 {
			return nil, platform.Fail(422, "INVALID_CURSOR", "Cursor không hợp lệ.")
		}
		b, e := base64.RawURLEncoding.DecodeString(encoded)
		var c cursor
		if e != nil || json.Unmarshal(b, &c) != nil || !platform.ID(c.ID) {
			return nil, platform.Fail(422, "INVALID_CURSOR", "Cursor không hợp lệ.")
		}
		t, e := time.Parse(time.RFC3339Nano, c.Time)
		if e != nil {
			return nil, platform.Fail(422, "INVALID_CURSOR", "Cursor không hợp lệ.")
		}
		boundary := strings.LastIndex(q, " ORDER BY ")
		if i := strings.LastIndex(q, " GROUP BY "); i >= 0 {
			boundary = i
		}
		if boundary < 0 {
			return nil, platform.Fail(500, "PAGINATION_ERROR", "Không thể phân trang.")
		}
		n := len(params)
		q = q[:boundary] + fmt.Sprintf(" AND (%s)<($%d::timestamptz,$%d::uuid)", p.Columns, n+1, n+2) + q[boundary:]
		params = append(params, t, c.ID)
		params[p.LimitIndex+1] = 0
	}
	rows, e := s.Store.Rows(r.Context(), q, params...)
	if e != nil {
		return nil, e
	}
	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	meta := map[string]any{"hasNext": hasNext, "nextCursor": nil}
	if hasNext && len(rows) > 0 {
		var last map[string]any
		if e = json.Unmarshal(rows[len(rows)-1], &last); e != nil {
			return nil, e
		}
		c := cursor{Time: fmt.Sprint(last[p.Field]), ID: fmt.Sprint(last["id"])}
		raw, _ := json.Marshal(c)
		meta["nextCursor"] = base64.RawURLEncoding.EncodeToString(raw)
	}
	*r = *r.WithContext(context.WithValue(r.Context(), contextKey("pagination"), meta))
	return rows, nil
}
