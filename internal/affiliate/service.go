package affiliate

import (
	"context"
	"encoding/json"
	"golang.org/x/sync/singleflight"
	"hoanxu/internal/browser"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type cacheEntry struct {
	at   time.Time
	data json.RawMessage
}
type Service struct {
	Store                     *platform.Store
	Browser                   *browser.Manager
	Enabled, TrackingVerified bool
	Publisher                 string
	SchemaVerified            bool
	PriceScale                int64
	mu                        sync.Mutex
	cache                     map[string]cacheEntry
	group                     singleflight.Group
}

func (s *Service) CheckEnabled() bool {
	return s.Enabled || (s.Browser != nil && (s.Browser.UsesManualLogin() || s.Browser.CookiesConfigured()))
}
func (s *Service) Check(ctx context.Context, raw string) (any, error) {
	shop, item, canonical, e := Resolve(ctx, raw)
	if e != nil {
		return nil, platform.Fail(422, "INVALID_URL", e.Error())
	}
	if !s.CheckEnabled() || s.Browser == nil {
		return nil, platform.Fail(503, "SHOPEE_NOT_CONFIGURED", "Shopee đang ở trạng thái chưa sẵn sàng; mẫu được giữ tại /demo.")
	}
	key := s.Publisher + ":" + shop + ":" + item + ":v1:" + strconv.FormatUint(s.Browser.SessionVersion(), 10)
	s.mu.Lock()
	entry, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(entry.at) < 600*time.Second {
		return json.RawMessage(entry.data), nil
	}
	ch := s.group.DoChan(key, func() (any, error) {
		c, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		b, e := s.Browser.Check(c, item)
		if e != nil {
			return nil, checkError(e)
		}
		var body map[string]any
		if e = json.Unmarshal(b, &body); e != nil {
			return nil, e
		}
		if code, ok := body["code"].(float64); ok && code != 0 {
			return nil, platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Shopee trả lỗi sản phẩm.")
		}
		data, ok := body["data"].(map[string]any)
		if !ok {
			return nil, platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Schema Shopee chưa được xác minh.")
		}
		result := map[string]any{"shopId": shop, "itemId": item, "productLink": canonical, "checkedAt": time.Now(), "estimated": true, "schemaVerified": false}
		if s.SchemaVerified {
			product, err := Normalize(data, s.PriceScale)
			if err != nil {
				return nil, platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Schema Shopee khác dữ liệu đã xác minh.")
			}
			b, _ := json.Marshal(product)
			var fields map[string]any
			_ = json.Unmarshal(b, &fields)
			for k, v := range fields {
				result[k] = v
			}
			result["schemaVerified"] = true
		}
		serialized, _ := json.Marshal(result)
		s.mu.Lock()
		if s.cache == nil {
			s.cache = map[string]cacheEntry{}
		}
		if len(s.cache) >= 1000 {
			var oldest string
			var at time.Time
			for k, v := range s.cache {
				if oldest == "" || v.at.Before(at) {
					oldest, at = k, v.at
				}
			}
			delete(s.cache, oldest)
		}
		s.cache[key] = cacheEntry{time.Now(), serialized}
		s.mu.Unlock()
		return json.RawMessage(serialized), nil
	})
	select {
	case <-ctx.Done():
		return nil, platform.Fail(504, "SHOPEE_TIMEOUT", "Kiểm tra Shopee quá thời gian.")
	case r := <-ch:
		return r.Val, r.Err
	}
}
func (s *Service) CreateLink(ctx context.Context, user, raw string) (any, error) {
	shop, item, canonical, e := Resolve(ctx, raw)
	_ = shop
	if e != nil {
		return nil, platform.Fail(422, "INVALID_URL", e.Error())
	}
	if !s.CheckEnabled() || !s.TrackingVerified {
		return nil, platform.Fail(503, "TRACKING_NOT_VERIFIED", "Chưa xác minh tracking Shopee; chưa tạo link hoàn tiền thật.")
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var settings []byte
	var status string
	e = tx.QueryRow(ctx, `SELECT status,settings FROM affiliate_channels WHERE id='shopee'`).Scan(&status, &settings)
	if e != nil {
		return nil, e
	}
	if status != "available" {
		return nil, platform.Fail(503, "CHANNEL_UNAVAILABLE", "Kênh Shopee chưa được bật.")
	}
	var conf struct {
		Template  string `json:"template"`
		Publisher string `json:"publisher"`
	}
	if e = json.Unmarshal(settings, &conf); e != nil {
		return nil, e
	}
	if conf.Publisher == "" {
		conf.Publisher = s.Publisher
	}
	if conf.Publisher == "" {
		return nil, platform.Fail(503, "PUBLISHER_NOT_CONFIGURED", "Nhập Affiliate ID tại trang Đăng nhập Shopee trước khi tạo link.")
	}
	tracking := platform.Hash(platform.Token())[:20]
	u, e := url.Parse(conf.Template)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Hostname() != "s.shopee.vn" || u.Path != "/an_redir" {
		return nil, platform.Fail(503, "INVALID_TEMPLATE", "Mẫu link chưa được cấu hình hợp lệ.")
	}
	q := u.Query()
	q.Set("origin_link", canonical)
	q.Set("affiliate_id", conf.Publisher)
	q.Set("sub_id", tracking)
	u.RawQuery = q.Encode()
	var id string
	// Pin the policy while resolving membership and writing the link snapshot.
	_, e = tx.Exec(ctx, `SELECT id FROM app_settings FOR SHARE`)
	if e != nil {
		return nil, e
	}
	m, e := cashback.MembershipFor(ctx, s.Store.Queries.WithTx(tx), user)
	if e != nil {
		return nil, e
	}
	e = tx.QueryRow(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id,item_id,tier_code,min_share_bps,max_share_bps) VALUES($1,'shopee',$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`, user, raw, u.String(), tracking, m.PolicyID, item, m.Code, int(m.Min), int(m.Max)).Scan(&id)
	if e != nil {
		return nil, e
	}
	return map[string]any{"id": id, "affiliateUrl": u.String(), "trackingCode": tracking, "channel": "shopee", "policyId": m.PolicyID, "tierCode": m.Code, "minSharePercent": m.Min, "maxSharePercent": m.Max}, tx.Commit(ctx)
}
