package legacyimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
)

// Apply commits the entire historical batch, including balanced order credits, atomically.
func (p *Plan) Apply(ctx context.Context, pool *pgxpool.Pool) (Summary, error) {
	if p == nil || len(p.customers) == 0 {
		return Summary{}, fmt.Errorf("empty legacy plan")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "legacy-import:"+Batch); err != nil {
		return Summary{}, err
	}
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM audit_logs WHERE action='legacy_import_completed' AND resource=$1`, Batch).Scan(&previous)
	if err == nil {
		var saved Summary
		if err = json.Unmarshal(previous, &saved); err != nil {
			return Summary{}, err
		}
		if saved.Hash != p.summary.Hash || saved.GeneratorVersion != generatorVersion {
			return Summary{}, fmt.Errorf("legacy batch %s already contains different source data or generator version", Batch)
		}
		saved.AlreadyApplied = true
		return saved, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, err
	}
	// Match wallet.Post's lock ordering. All destination wallets are created in this
	// transaction and start at zero, so there can be no prior debt to repay.
	var system string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM wallet_accounts WHERE kind='system' FOR UPDATE`).Scan(&system); err != nil {
		return Summary{}, err
	}
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE legacy_import_customers(id uuid PRIMARY KEY,name text NOT NULL,created_at timestamptz NOT NULL) ON COMMIT DROP;
	 CREATE TEMP TABLE legacy_import_orders(id uuid PRIMARY KEY,link_id uuid NOT NULL,user_id uuid NOT NULL,tracking text NOT NULL,external_id text NOT NULL,line_id text NOT NULL,ordered_at timestamptz NOT NULL,cashback bigint NOT NULL CHECK(cashback BETWEEN 5000 AND 40000)) ON COMMIT DROP;`); err != nil {
		return Summary{}, err
	}
	customers := make([][]any, 0, len(p.customers))
	orders := make([][]any, 0, p.summary.Orders)
	for _, c := range p.customers {
		customers = append(customers, []any{c.id, c.Name, c.orders[0].at})
		for _, o := range c.orders {
			orders = append(orders, []any{o.id, o.link, o.user, o.tracking, o.external, o.line, o.at, o.cash})
		}
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"legacy_import_customers"}, []string{"id", "name", "created_at"}, pgx.CopyFromRows(customers)); err != nil {
		return Summary{}, err
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"legacy_import_orders"}, []string{"id", "link_id", "user_id", "tracking", "external_id", "line_id", "ordered_at", "cashback"}, pgx.CopyFromRows(orders)); err != nil {
		return Summary{}, err
	}
	policy := stableID("policy", 0, 0)
	if _, err = tx.Exec(ctx, `INSERT INTO cashback_policies(id,share_percent,mode,created_at) SELECT $1,100,'fixed',min(created_at) FROM legacy_import_customers`, policy); err != nil {
		return Summary{}, err
	}
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,name,email,role,tracking_code,created_at) SELECT id,name,'','customer','legacy_'||replace(id::text,'-',''),created_at FROM legacy_import_customers`, nil},
		{`INSERT INTO wallet_accounts(user_id,kind) SELECT c.id,k.kind FROM legacy_import_customers c CROSS JOIN (VALUES ('available'),('held'),('debt'),('gift_held')) k(kind)`, nil},
		{`INSERT INTO wallet_accounts(user_id,kind) SELECT c.id,k.kind FROM legacy_import_customers c CROSS JOIN (VALUES ('green_available'),('green_gift_held')) k(kind) WHERE to_regclass('xu_exchange_policies') IS NOT NULL`, nil},
		{`INSERT INTO coin_accounts(user_id) SELECT id FROM legacy_import_customers`, nil},
		{`INSERT INTO affiliate_links(id,user_id,channel,original_url,affiliate_url,tracking_code,policy_id,min_share_bps,max_share_bps,created_at)
		 SELECT link_id,user_id,'shopee','link','link',tracking,$1,10000,10000,ordered_at FROM legacy_import_orders`, []any{policy}},
		{`INSERT INTO orders(id,user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,share_bps,ordered_at,approved_at,created_at)
		 SELECT id,user_id,link_id,$1,'shopee','legacy-server',external_id,line_id,'Đơn hàng từ server cũ',0,cashback,cashback,'approved','approved',10000,ordered_at,ordered_at,ordered_at FROM legacy_import_orders`, []any{policy}},
		{`INSERT INTO order_items(order_id,quantity) SELECT id,1 FROM legacy_import_orders`, nil},
		{`INSERT INTO order_events(order_id,action,reason,payload,created_at)
		 SELECT id,'approved','Nhập lịch sử từ server cũ; thiếu dữ liệu gốc',jsonb_build_object('batch',$1::text,'cashback',cashback,'commission',cashback,'shareBps',10000,'generatedFields',jsonb_build_array('orderedAtTime','approvedAt','cashback','commission','value','productName','urls','policy')),ordered_at FROM legacy_import_orders`, []any{Batch}},
		// These are real immutable ledger transactions, one per order, with the
		// standard order_credit reference. Balances are derived from their entries.
		{`INSERT INTO wallet_transactions(reference,description) SELECT 'order_credit:'||id::text,'Hoàn tiền đơn hàng từ server cũ' FROM legacy_import_orders`, nil},
		{`INSERT INTO wallet_entries(transaction_id,account_id,amount)
		 SELECT t.id,a.id,o.cashback FROM legacy_import_orders o JOIN wallet_transactions t ON t.reference='order_credit:'||o.id::text JOIN wallet_accounts a ON a.user_id=o.user_id AND a.kind='available'
		 UNION ALL SELECT t.id,$1::uuid,-o.cashback FROM legacy_import_orders o JOIN wallet_transactions t ON t.reference='order_credit:'||o.id::text`, []any{system}},
		{`UPDATE wallet_accounts a SET balance=a.balance+c.amount FROM (SELECT user_id,sum(cashback)::bigint amount FROM legacy_import_orders GROUP BY user_id) c WHERE a.user_id=c.user_id AND a.kind='available'`, nil},
		{`UPDATE wallet_accounts SET balance=balance-(SELECT sum(cashback) FROM legacy_import_orders) WHERE id=$1`, []any{system}},
	}
	for _, step := range steps {
		if _, err = tx.Exec(ctx, step.sql, step.args...); err != nil {
			return Summary{}, err
		}
	}
	ids := make([]string, 0, len(p.customers))
	for _, c := range p.customers {
		ids = append(ids, c.id)
	}
	if err = wallet.RefreshGoldTotals(ctx, tx, ids...); err != nil {
		return Summary{}, err
	}
	var invalid int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM wallet_accounts a JOIN legacy_import_customers c ON c.id=a.user_id WHERE a.balance<>coalesce((SELECT sum(e.amount) FROM wallet_entries e WHERE e.account_id=a.id),0)`).Scan(&invalid); err != nil {
		return Summary{}, err
	}
	if invalid != 0 {
		return Summary{}, fmt.Errorf("legacy wallet reconciliation failed")
	}
	if err = platform.Audit(ctx, tx, "", "legacy_import_completed", Batch, p.summary); err != nil {
		return Summary{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Summary{}, err
	}
	return p.summary, nil
}
