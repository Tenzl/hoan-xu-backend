package affiliate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"
	"hoanxu/internal/browser"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	at   time.Time
	data json.RawMessage
}

type OfferLinkGenerator interface {
	CreateOfferLink(context.Context, string, string, [5]string) (string, error)
}

type Service struct {
	Store         *platform.Store
	Browser       *browser.Manager
	LinkGenerator OfferLinkGenerator                         // Defaults to Browser.
	ProductLookup func(context.Context, string) (any, error) // Defaults to the verified product checker.
	Lifetime      context.Context

	Enabled, TrackingVerified bool
	ManagedConfig             bool
	Publisher                 string
	SchemaVerified            bool
	PriceScale                int64
	mu                        sync.Mutex
	cache                     map[string]cacheEntry
	group                     singleflight.Group
}

func (s *Service) CheckEnabled() bool {
	if s.ManagedConfig {
		return s.Enabled
	}
	return s.Enabled || (s.Browser != nil && (s.Browser.UsesManualLogin() || s.Browser.CookiesConfigured()))
}
func (s *Service) ClearCache() { s.mu.Lock(); defer s.mu.Unlock(); s.cache = nil }
func (s *Service) Check(ctx context.Context, raw string) (value any, checkErr error) {
	started := time.Now()
	var resolveTime, checkWait time.Duration
	var cacheHit, shared bool
	defer func() {
		// Timings only: never log the input URL, response, cookies or account.
		slog.Info("shopee_check_completed", "resolve_ms", resolveTime.Milliseconds(), "check_wait_ms", checkWait.Milliseconds(), "total_ms", time.Since(started).Milliseconds(), "cache_hit", cacheHit, "shared", shared, "success", checkErr == nil)
	}()
	resolveCtx, resolveCancel := context.WithTimeout(ctx, 8*time.Second)
	shop, item, canonical, e := Resolve(resolveCtx, raw)
	resolveCancel()
	resolveTime = time.Since(started)
	if e != nil {
		return nil, resolveError(e)
	}
	if !s.CheckEnabled() || s.Browser == nil {
		return nil, platform.Fail(503, "SHOPEE_NOT_CONFIGURED", "Shopee đang ở trạng thái chưa sẵn sàng; mẫu được giữ tại /demo.")
	}
	publisher := s.Publisher
	if s.Store != nil && s.Store.Pool != nil {
		publisher, e = s.PublisherID(ctx)
		if e != nil {
			return nil, e
		}
	}
	schemaVerified, priceScale, manager := s.SchemaVerified, s.PriceScale, s.Browser
	key := publisher + ":" + shop + ":" + item + ":v3:" + strconv.FormatInt(priceScale, 10) + ":" + strconv.FormatBool(schemaVerified) + ":" + strconv.FormatUint(manager.SessionVersion(), 10)
	s.mu.Lock()
	entry, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(entry.at) < 600*time.Second {
		cacheHit = true
		return json.RawMessage(entry.data), nil
	}
	ch := s.group.DoChan(key, func() (any, error) {
		deadline := time.Now().Add(40 * time.Second)
		if incoming, ok := ctx.Deadline(); ok && incoming.Before(deadline) {
			deadline = incoming
		}
		c, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		b, e := manager.Check(c, item)
		if e != nil {
			return nil, checkError(e)
		}
		var body map[string]any
		if e = json.Unmarshal(b, &body); e != nil {
			return nil, checkError(errors.New("SHOPEE_RESPONSE_INVALID"))
		}
		if code, ok := body["code"].(float64); ok && code != 0 {
			return nil, platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Shopee trả lỗi sản phẩm.")
		}
		data, ok := body["data"].(map[string]any)
		if !ok {
			return nil, platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Schema Shopee chưa được xác minh.")
		}
		result := map[string]any{"shopId": shop, "itemId": item, "productLink": canonical, "checkedAt": time.Now(), "estimated": true, "schemaVerified": false}
		if schemaVerified {
			product, err := Normalize(data, priceScale)
			if err != nil {
				manager.RecordFailure("SHOPEE_RESPONSE_INVALID", "normalize")
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
		if !schemaVerified {
			return json.RawMessage(serialized), nil
		}
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
	waitStarted := time.Now()
	select {
	case <-ctx.Done():
		checkWait = time.Since(waitStarted)
		return nil, platform.Fail(504, "SHOPEE_TIMEOUT", "Kiểm tra Shopee quá thời gian.")
	case r := <-ch:
		checkWait = time.Since(waitStarted)
		shared = r.Shared
		return r.Val, r.Err
	}
}
func (s *Service) CreateLink(ctx context.Context, user, raw string) (any, error) {
	return s.createLink(ctx, user, raw, nil)
}
func (s *Service) createLink(ctx context.Context, user, raw string, complete func(pgx.Tx, string, any) error) (any, error) {
	ctx, finish := context.WithTimeout(ctx, 55*time.Second)
	defer finish()
	resolveCtx, resolveDone := context.WithTimeout(ctx, 8*time.Second)
	shop, item, canonical, e := Resolve(resolveCtx, raw)
	resolveDone()
	if e != nil {
		return nil, resolveError(e)
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
	if !s.ManagedConfig && status != "available" {
		return nil, platform.Fail(503, "CHANNEL_UNAVAILABLE", "Kênh Shopee chưa được bật.")
	}
	var conf struct {
		Publisher string `json:"publisher"`
	}
	if e = json.Unmarshal(settings, &conf); e != nil {
		return nil, e
	}
	if conf.Publisher == "" {
		return nil, platform.Fail(503, "PUBLISHER_NOT_CONFIGURED", "Nhập Affiliate ID tại trang Đăng nhập Shopee trước khi tạo link.")
	}
	var customerTracking string
	if e = tx.QueryRow(ctx, `SELECT tracking_code FROM users WHERE id=$1 AND role='customer' AND NOT blocked`, user).Scan(&customerTracking); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM app_settings FOR SHARE`); e != nil {
		return nil, e
	}
	// Snapshot before contacting Shopee, without holding a transaction over the network.
	m, e := cashback.MembershipFor(ctx, s.Store.Queries.WithTx(tx), user)
	if e != nil {
		return nil, e
	}
	var version uint32
	if e = tx.QueryRow(ctx, `SELECT tracking_version FROM cashback_policies WHERE id=$1`, m.PolicyID).Scan(&version); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	lookup := s.ProductLookup
	if lookup == nil {
		lookup = s.Check
	}
	checked, e := lookup(ctx, canonical)
	if e != nil {
		return nil, e
	}
	productRaw, e := json.Marshal(checked)
	if e != nil {
		return nil, e
	}
	var product struct {
		Name     string `json:"productName"`
		Shop     string `json:"shopId"`
		Item     string `json:"itemId"`
		Verified bool   `json:"schemaVerified"`
	}
	if json.Unmarshal(productRaw, &product) != nil || !product.Verified || strings.TrimSpace(product.Name) == "" || product.Shop != shop || product.Item != item {
		return nil, platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Dữ liệu sản phẩm Shopee không hợp lệ. Vui lòng thử lại sau.")
	}
	product.Name = strings.TrimSpace(product.Name)
	rate, e := cashback.SampleLink(nil, int(m.Min), int(m.Max), int(m.Tax))
	if e != nil {
		return nil, platform.Fail(422, "INVALID_CASHBACK_POLICY", "Khoảng tỷ lệ cần là số nguyên và chênh lệch ít nhất 5 điểm phần trăm.")
	}
	bps := rate.MainBps
	shopID, e := strconv.ParseUint(shop, 10, 64)
	if e != nil {
		return nil, e
	}
	itemID, e := strconv.ParseUint(item, 10, 64)
	if e != nil {
		return nil, e
	}
	created := time.Now().UTC().Truncate(time.Second)
	claims := tracking.Claims{CreatedAt: created, Shop: shopID, Item: itemID, Policy: version, Tier: m.Code, Bps: bps}
	ids, e := tracking.Issue(claims, customerTracking, conf.Publisher, rate.Factor(), s.Store.SignTracking)
	if e != nil {
		return nil, e
	}
	generator := s.LinkGenerator
	if generator == nil {
		if s.Browser == nil {
			return nil, platform.Fail(503, "BROWSER_UNAVAILABLE", "Chrome Shopee chưa sẵn sàng.")
		}
		generator = s.Browser
	}
	linkCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	shortURL, e := generator.CreateOfferLink(linkCtx, shop, item, ids)
	if e != nil {
		return nil, checkError(e)
	}
	subIDs, e := json.Marshal(ids)
	if e != nil {
		return nil, e
	}
	var id string
	save, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer save.Rollback(ctx)
	e = save.QueryRow(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id,item_id,created_at,tier_code,min_share_bps,max_share_bps,tracking_sub_ids,expires_at,payout_factor,effective_share_bps,lifecycle_status,product_name) VALUES($1,'shopee',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'active',$15) RETURNING id::text`, user, canonical, shortURL, ids[2], m.PolicyID, item, created, m.Code, int(m.Public().Min), int(m.Public().Max), subIDs, claims.ExpiresAt(), rate.Factor(), rate.EffectiveBps, product.Name).Scan(&id)
	if e != nil {
		return nil, e
	}
	result := map[string]any{"id": id, "status": "active", "canDelete": false, "legacy": false, "affiliateUrl": shortURL, "trackingCode": ids[2], "channel": "shopee", "policyId": m.PolicyID, "tierCode": m.Code, "minSharePercent": m.Public().Min, "maxSharePercent": m.Public().Max, "effectiveSharePercent": cashback.Percent(rate.EffectiveBps), "payoutFactor": rate.Factor(), "createdAt": created, "expiresAt": claims.ExpiresAt()}
	result["productName"] = product.Name
	if complete != nil {
		if e = complete(save, id, result); e != nil {
			return nil, e
		}
	}
	return result, save.Commit(ctx)
}
