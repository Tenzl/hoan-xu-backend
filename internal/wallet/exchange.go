package wallet

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"hoanxu/internal/platform/db"
	"math/big"
	"time"
)

type ExchangePolicy struct {
	ID         string    `json:"id"`
	GoldUnits  int64     `json:"goldUnits"`
	GreenUnits int64     `json:"greenUnits"`
	CreatedAt  time.Time `json:"createdAt"`
}
type ExchangePolicyInput struct {
	CurrentVersionID string `json:"currentVersionId"`
	GoldUnits        int64  `json:"goldUnits"`
	GreenUnits       int64  `json:"greenUnits"`
}
type ExchangeInput struct {
	GoldAmountXu             int64  `json:"goldAmountXu"`
	ExpectedPolicyID         string `json:"expectedPolicyId"`
	ExpectedTierCode         string `json:"expectedTierCode,omitempty"`
	ExpectedCashbackPolicyID string `json:"expectedCashbackPolicyId,omitempty"`
}
type ExchangeQuotePolicy struct {
	ExchangePolicy
	TierCode         string `json:"tierCode"`
	BonusPercent     int64  `json:"bonusPercent"`
	CashbackPolicyID string `json:"cashbackPolicyId"`
	NameVI           string `json:"nameVi"`
	NameEN           string `json:"nameEn"`
}
type ExchangeResult struct {
	ID               string `json:"id"`
	PolicyID         string `json:"policyId"`
	GoldSpent        int64  `json:"goldSpent"`
	GreenReceived    int64  `json:"greenReceived"`
	GoldAvailable    int64  `json:"goldAvailable"`
	GreenAvailable   int64  `json:"greenAvailable"`
	TierCode         string `json:"tierCode,omitempty"`
	BonusPercent     int64  `json:"bonusPercent,omitempty"`
	CashbackPolicyID string `json:"cashbackPolicyId,omitempty"`
}

const exchangePolicySQL = `SELECT id::text,gold_units,green_units,created_at FROM xu_exchange_policies ORDER BY created_at DESC,id DESC LIMIT 1`

func (s *Service) CurrentExchangePolicy(ctx context.Context) (ExchangePolicy, error) {
	var p ExchangePolicy
	e := s.Store.Pool.QueryRow(ctx, exchangePolicySQL).Scan(&p.ID, &p.GoldUnits, &p.GreenUnits, &p.CreatedAt)
	return p, e
}
func (s *Service) CustomerExchangePolicy(ctx context.Context, user string) (ExchangeQuotePolicy, error) {
	var quote ExchangeQuotePolicy
	// A single read snapshot keeps the base rate and membership quote consistent.
	tx, e := s.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return quote, e
	}
	defer tx.Rollback(ctx)
	if e = tx.QueryRow(ctx, exchangePolicySQL).Scan(&quote.ID, &quote.GoldUnits, &quote.GreenUnits, &quote.CreatedAt); e != nil {
		return quote, e
	}
	m, e := cashback.MembershipFor(ctx, db.New(tx), user)
	if e != nil {
		return quote, e
	}
	quote.TierCode = m.Code
	quote.BonusPercent = m.ExchangeBonus
	quote.CashbackPolicyID = m.PolicyID
	quote.NameVI = m.NameVI
	quote.NameEN = m.NameEN
	return quote, tx.Commit(ctx)
}
func (s *Service) SetExchangePolicy(ctx context.Context, actor, key string, input ExchangePolicyInput) (ExchangePolicy, error) {
	var result ExchangePolicy
	if !platform.ID(input.CurrentVersionID) || input.GoldUnits < 1 || input.GoldUnits > 1000000 || input.GreenUnits < 1 || input.GreenUnits > 1000000 {
		return result, platform.Fail(422, "VALIDATION_ERROR", "Tỷ lệ đổi Xu không hợp lệ.")
	}
	v, e := s.Store.Action(ctx, actor, key, "xu-exchange-policy", input, func(tx pgx.Tx) (any, error) {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('xu-exchange-policy',0))`); e != nil {
			return nil, e
		}
		var current ExchangePolicy
		if e := tx.QueryRow(ctx, exchangePolicySQL).Scan(&current.ID, &current.GoldUnits, &current.GreenUnits, &current.CreatedAt); e != nil {
			return nil, e
		}
		if current.ID != input.CurrentVersionID {
			return nil, platform.Fail(409, "EXCHANGE_POLICY_CHANGED", "Tỷ lệ đổi Xu đã thay đổi. Vui lòng kiểm tra lại.")
		}
		var p ExchangePolicy
		if e := tx.QueryRow(ctx, `INSERT INTO xu_exchange_policies(gold_units,green_units,actor_id) VALUES($1,$2,$3) RETURNING id::text,gold_units,green_units,created_at`, input.GoldUnits, input.GreenUnits, actor).Scan(&p.ID, &p.GoldUnits, &p.GreenUnits, &p.CreatedAt); e != nil {
			return nil, e
		}
		return p, platform.Audit(ctx, tx, actor, "xu_exchange_policy_created", p.ID, input)
	})
	if e != nil {
		return result, e
	}
	b, e := json.Marshal(v)
	if e == nil {
		e = json.Unmarshal(b, &result)
	}
	return result, e
}
func (s *Service) Exchange(ctx context.Context, user, key string, p ExchangeInput) (any, error) {
	validTier := p.ExpectedTierCode == "" || p.ExpectedTierCode == "member" || p.ExpectedTierCode == "silver" || p.ExpectedTierCode == "gold" || p.ExpectedTierCode == "bronze" || p.ExpectedTierCode == "platinum" || p.ExpectedTierCode == "diamond"
	if p.GoldAmountXu < 1 || p.GoldAmountXu > 1000000000000 || !platform.ID(p.ExpectedPolicyID) || !validTier || (p.ExpectedCashbackPolicyID != "" && !platform.ID(p.ExpectedCashbackPolicyID)) || (p.ExpectedTierCode == "") != (p.ExpectedCashbackPolicyID == "") {
		return nil, platform.Fail(422, "VALIDATION_ERROR", "Số Xu vàng đổi không hợp lệ.")
	}
	return s.Store.Action(ctx, user, key, "xu-exchange", p, func(tx pgx.Tx) (any, error) {
		if _, e := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
			return nil, e
		}
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('xu-exchange-policy',0))`); e != nil {
			return nil, e
		}
		if _, e := tx.Exec(ctx, `SELECT id FROM app_settings FOR SHARE`); e != nil {
			return nil, e
		}
		var rate ExchangePolicy
		if e := tx.QueryRow(ctx, exchangePolicySQL).Scan(&rate.ID, &rate.GoldUnits, &rate.GreenUnits, &rate.CreatedAt); e != nil {
			return nil, e
		}
		if rate.ID != p.ExpectedPolicyID {
			return nil, platform.Fail(409, "EXCHANGE_POLICY_CHANGED", "Tỷ lệ đổi Xu đã thay đổi. Vui lòng kiểm tra lại.")
		}
		membership, e := cashback.MembershipFor(ctx, db.New(tx), user)
		if e != nil {
			return nil, e
		}
		if p.ExpectedTierCode != "" && (p.ExpectedTierCode != membership.Code || p.ExpectedCashbackPolicyID != membership.PolicyID) {
			return nil, platform.Fail(409, "EXCHANGE_TIER_CHANGED", "Hạng hoặc chính sách hạng đã thay đổi. Vui lòng kiểm tra lại tỷ lệ đổi Xu.")
		}
		bonus := membership.ExchangeBonus
		// Round only once, after applying the tier bonus. The intermediate product
		// can exceed int64 even when the final result is within the allowed limit.
		amount := new(big.Int).Mul(big.NewInt(p.GoldAmountXu), big.NewInt(rate.GreenUnits))
		amount.Mul(amount, big.NewInt(100+bonus))
		amount.Quo(amount, new(big.Int).Mul(big.NewInt(rate.GoldUnits), big.NewInt(100)))
		if !amount.IsInt64() || amount.Sign() < 1 || amount.Cmp(big.NewInt(1000000000000)) > 0 {
			return nil, platform.Fail(422, "INVALID_EXCHANGE_RESULT", "Số Xu xanh nhận phải từ 1 đến 1.000.000.000.000.")
		}
		green := amount.Int64()
		id := platform.Token()
		if e := Post(ctx, tx, "xu_exchange:"+id, "Đổi Xu vàng sang Xu xanh", []Entry{{user, "available", -p.GoldAmountXu}, {"", "system", p.GoldAmountXu}, {"", "green_system", -green}, {user, "green_available", green}}); e != nil {
			return nil, e
		}
		if e := RefreshGoldTotals(ctx, tx, user); e != nil {
			return nil, e
		}
		result := ExchangeResult{ID: id, PolicyID: rate.ID, GoldSpent: p.GoldAmountXu, GreenReceived: green, TierCode: membership.Code, BonusPercent: bonus, CashbackPolicyID: membership.PolicyID}
		if e := tx.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available')`, user).Scan(&result.GoldAvailable, &result.GreenAvailable); e != nil {
			return nil, e
		}
		return result, nil
	})
}

// Green points never settle gold debt.
func CreditGreen(ctx context.Context, tx pgx.Tx, user, ref, description string, amount int64) error {
	if amount < 0 {
		return platform.Fail(422, "INVALID_AMOUNT", "Số tiền không hợp lệ.")
	}
	if amount == 0 {
		return nil
	}
	return Post(ctx, tx, ref, description, []Entry{{"", "green_system", -amount}, {user, "green_available", amount}})
}
