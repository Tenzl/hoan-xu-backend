package cashback

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"hoanxu/internal/platform"
	"hoanxu/internal/platform/db"
)

// Percent uses exact hundredths of one percent, including at the JSON boundary.
type Percent int

var decimalPercent = regexp.MustCompile(`^\d{1,3}(\.\d{1,2})?$`)
var ErrInvalidPercent = errors.New("invalid cashback percentage precision")
var ErrInvalidTier = errors.New("missing cashback tier fields")

func (p *Percent) UnmarshalJSON(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	if !decimalPercent.MatchString(s) {
		return ErrInvalidPercent
	}
	parts := strings.SplitN(s, ".", 2)
	whole, _ := strconv.Atoi(parts[0])
	fraction := 0
	if len(parts) == 2 {
		fraction, _ = strconv.Atoi((parts[1] + "00")[:2])
	}
	*p = Percent(whole*100 + fraction)
	return nil
}
func (p Percent) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d.%02d", p/100, p%100)), nil
}

type Tier struct {
	Code      string  `json:"tierCode"`
	MinOrders int64   `json:"minApprovedOrders"`
	Min       Percent `json:"minSharePercent"`
	Max       Percent `json:"maxSharePercent"`
}

func (t *Tier) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return e
	}
	for _, key := range []string{"tierCode", "minApprovedOrders", "minSharePercent", "maxSharePercent"} {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return ErrInvalidTier
		}
	}
	type plain Tier
	var value plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&value); e != nil {
		return e
	}
	*t = Tier(value)
	return nil
}

type Policy struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Tiers     []Tier    `json:"tiers"`
	Tax       Percent   `json:"taxPercent"`
}
type Input struct {
	CurrentVersionID string  `json:"currentVersionId"`
	Tiers            []Tier  `json:"tiers"`
	Tax              Percent `json:"taxPercent"`
}
type Membership struct {
	PolicyID string `json:"policyId"`
	Tier
	Tax            Percent `json:"-"`
	ApprovedOrders int64   `json:"approvedOrders"`
	NextTier       *Tier   `json:"nextTier"`
	OrdersToNext   int64   `json:"ordersToNext"`
}
type Service struct{ Store *platform.Store }

func uuid(s string) pgtype.UUID { var id pgtype.UUID; _ = id.Scan(s); return id }
func Current(ctx context.Context, q *db.Queries) (*Policy, error) {
	row, e := q.CurrentCashbackPolicy(ctx)
	if e != nil {
		return nil, e
	}
	tiers, e := q.CashbackTiers(ctx, uuid(row.ID))
	if e != nil {
		return nil, e
	}
	p := &Policy{ID: row.ID, CreatedAt: row.CreatedAt.Time, Tiers: []Tier{}, Tax: Percent(row.TaxBps)}
	for _, t := range tiers {
		p.Tiers = append(p.Tiers, Tier{t.TierCode, t.MinApprovedOrders, Percent(t.MinShareBps), Percent(t.MaxShareBps)})
	}
	if len(p.Tiers) != 3 {
		return nil, fmt.Errorf("incomplete cashback policy")
	}
	return p, nil
}
func Select(p *Policy, count int64) Membership {
	m := Membership{PolicyID: p.ID, Tier: p.Tiers[0], Tax: p.Tax, ApprovedOrders: count}
	for i, t := range p.Tiers {
		if count >= t.MinOrders {
			m.Tier = t
			continue
		}
		m.NextTier = &p.Tiers[i]
		m.OrdersToNext = t.MinOrders - count
		break
	}
	return m
}
func MembershipFor(ctx context.Context, q *db.Queries, user string) (Membership, error) {
	p, e := Current(ctx, q)
	if e != nil {
		return Membership{}, e
	}
	n, e := q.ApprovedOrderCount(ctx, uuid(user))
	if e != nil {
		return Membership{}, e
	}
	return Select(p, n), nil
}
func Validate(p Input) error {
	if !platform.ID(p.CurrentVersionID) || len(p.Tiers) != 3 {
		return platform.Fail(422, "INVALID_CASHBACK_POLICY", "Cần đủ ba hạng và phiên bản chính sách hợp lệ.")
	}
	if p.Tax < 0 || p.Tax > 10000 {
		return platform.Fail(422, "INVALID_CASHBACK_POLICY", "Phần trăm thuế phải từ 0–100%, tối đa hai chữ số thập phân.")
	}
	codes := []string{"bronze", "platinum", "diamond"}
	var last int64 = -1
	for i, t := range p.Tiers {
		if t.Code != codes[i] || t.MinOrders <= last || t.MinOrders > 1000000000 || (i == 0 && t.MinOrders != 0) || t.Min < 0 || t.Max > 10000 || t.Min > t.Max {
			return platform.Fail(422, "INVALID_CASHBACK_POLICY", "Ngưỡng hạng phải tăng dần; tỷ lệ từ 0–100%, tối thiểu không vượt tối đa.")
		}
		if e := ValidateLinkRange(int(t.Min), int(t.Max)); e != nil {
			return platform.Fail(422, "INVALID_CASHBACK_POLICY", "Tỷ lệ phải là số nguyên; tối đa cao hơn tối thiểu ít nhất 5 điểm phần trăm.")
		}
		last = t.MinOrders
	}
	return nil
}
func (s *Service) Current(ctx context.Context) (*Policy, error) { return Current(ctx, s.Store.Queries) }
func (s *Service) Create(ctx context.Context, actor, key string, p Input) (any, error) {
	if e := Validate(p); e != nil {
		return nil, e
	}
	return s.Store.Action(ctx, actor, key, "cashback-policy", p, func(tx pgx.Tx) (any, error) {
		if _, e := tx.Exec(ctx, `SELECT id FROM app_settings FOR UPDATE`); e != nil {
			return nil, e
		}
		old, e := Current(ctx, s.Store.Queries.WithTx(tx))
		if e != nil {
			return nil, e
		}
		if old.ID != p.CurrentVersionID {
			return nil, platform.Fail(409, "POLICY_VERSION_CONFLICT", "Chính sách đã thay đổi. Tải lại cấu hình trước khi lưu.")
		}
		var id string
		if e = tx.QueryRow(ctx, `INSERT INTO cashback_policies(share_percent,mode,created_at,tax_bps) VALUES($1::numeric/100,'tiered',clock_timestamp(),$2) RETURNING id::text`, int(p.Tiers[0].Min), int(p.Tax)).Scan(&id); e != nil {
			return nil, e
		}
		for _, t := range p.Tiers {
			if _, e = tx.Exec(ctx, `INSERT INTO cashback_tiers(policy_id,tier_code,min_approved_orders,min_share_bps,max_share_bps) VALUES($1,$2,$3,$4,$5)`, id, t.Code, t.MinOrders, int(t.Min), int(t.Max)); e != nil {
				return nil, e
			}
		}
		if e = platform.Audit(ctx, tx, actor, "cashback_policy_created", id, p); e != nil {
			return nil, e
		}
		return Current(ctx, s.Store.Queries.WithTx(tx))
	})
}
func Sample(source io.Reader, min, max int) (int, error) {
	if min < 0 || max > 10000 || min > max {
		return 0, fmt.Errorf("invalid cashback range")
	}
	if min == max {
		return min, nil
	}
	if source == nil {
		source = rand.Reader
	}
	n, e := rand.Int(source, big.NewInt(int64(max-min+1)))
	if e != nil {
		return 0, e
	}
	return min + int(n.Int64()), nil
}
func Amount(commission int64, bps int) (int64, error) {
	if commission < 0 || commission > 1e12 || bps < 0 || bps > 10000 {
		return 0, fmt.Errorf("invalid cashback amount")
	}
	return commission * int64(bps) / 10000, nil
}

// The natural source key is shared by CSV and manual orders; retries read the stored draw.
func LockOrder(ctx context.Context, tx pgx.Tx, channel, publisher, external, line string) error {
	b, _ := json.Marshal([]string{channel, publisher, external, line})
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cashback-order:"+string(b))
	return e
}
