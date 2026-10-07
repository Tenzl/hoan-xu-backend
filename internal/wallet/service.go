package wallet

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"regexp"
	"sort"
	"strings"
)

type Service struct{ Store *platform.Store }
type Entry struct {
	User   string
	Kind   string
	Amount int64
}

// Post locks accounts in a consistent order and updates balances with immutable entries.
func Post(ctx context.Context, tx pgx.Tx, reference, description string, entries []Entry) error {
	sums := map[bool]int64{}
	for _, v := range entries {
		sums[strings.HasPrefix(v.Kind, "green_")] += v.Amount
	}
	if sums[false] != 0 || sums[true] != 0 {
		return platform.Fail(500, "LEDGER_UNBALANCED", "Giao dịch không cân bằng.")
	}
	var tid string
	e := tx.QueryRow(ctx, `INSERT INTO wallet_transactions(reference,description) VALUES($1,$2) RETURNING id::text`, reference, description).Scan(&tid)
	if e != nil {
		return platform.Conflict(e)
	}
	// System account is locked first for all money operations, then customer accounts.
	var system string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM wallet_accounts WHERE kind='system' FOR UPDATE`).Scan(&system); e != nil {
		return e
	}
	var greenSystem string
	for _, v := range entries {
		if strings.HasPrefix(v.Kind, "green_") {
			if e = tx.QueryRow(ctx, `SELECT id::text FROM wallet_accounts WHERE kind='green_system' FOR UPDATE`).Scan(&greenSystem); e != nil {
				return e
			}
			break
		}
	}
	entries = append([]Entry(nil), entries...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].User+":"+entries[i].Kind < entries[j].User+":"+entries[j].Kind })
	for _, v := range entries {
		if v.Amount == 0 {
			continue
		}
		id := system
		if v.Kind == "green_system" {
			id = greenSystem
		}
		if v.Kind != "system" && v.Kind != "green_system" {
			if e = tx.QueryRow(ctx, `SELECT id::text FROM wallet_accounts WHERE user_id=$1 AND kind=$2 FOR UPDATE`, v.User, v.Kind).Scan(&id); e != nil {
				return e
			}
		}
		tag, e := tx.Exec(ctx, `UPDATE wallet_accounts SET balance=balance+$2 WHERE id=$1 AND (kind IN ('system','green_system') OR balance+$2>=0)`, id, v.Amount)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return platform.Fail(409, "INSUFFICIENT_BALANCE", "Số dư không đủ.")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO wallet_entries(transaction_id,account_id,amount) VALUES($1,$2,$3)`, tid, id, v.Amount); e != nil {
			return e
		}
	}
	return nil
}
func Credit(ctx context.Context, tx pgx.Tx, user, ref, description string, amount int64) error {
	if amount < 0 {
		return platform.Fail(422, "INVALID_AMOUNT", "Số tiền không hợp lệ.")
	}
	if amount == 0 {
		return nil
	}
	if _, e := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
		return e
	}
	var debt int64
	if e := tx.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='debt' FOR UPDATE`, user).Scan(&debt); e != nil {
		return e
	}
	repay := min(debt, amount)
	entries := []Entry{{"", "system", -amount}, {user, "available", amount - repay}}
	// Debt is a separate positive claim; clearing it requires a balancing system entry.
	if repay > 0 {
		entries = append(entries, Entry{user, "debt", -repay}, Entry{"", "system", 2 * repay})
	}
	return Post(ctx, tx, ref, description, entries)
}

type WithdrawalInput struct {
	Amount  int64  `json:"amount"`
	Bank    string `json:"bank"`
	Account string `json:"account"`
	Holder  string `json:"holder"`
}

func (s *Service) Withdraw(ctx context.Context, user, key string, p WithdrawalInput) (any, error) {
	if p.Amount < 50000 || p.Amount%1000 != 0 || p.Amount > 1e12 || !platform.Text(p.Bank, 2, 80) || !platform.Text(p.Holder, 2, 80) || !regexp.MustCompile(`^[0-9]{6,20}$`).MatchString(p.Account) {
		return nil, platform.Fail(422, "VALIDATION_ERROR", "Thông tin rút tiền không hợp lệ.")
	}
	return s.Store.Action(ctx, user, key, "withdraw", p, func(tx pgx.Tx) (any, error) {
		// All money operations serialize on the system account before user locks.
		if _, e := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
			return nil, e
		}
		var debt int64
		if e := tx.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='debt' FOR UPDATE`, user).Scan(&debt); e != nil {
			return nil, e
		}
		if debt > 0 {
			return nil, platform.Fail(409, "OUTSTANDING_DEBT", "Cần xử lý khoản thiếu trước khi rút.")
		}
		var id string
		details, _ := json.Marshal(map[string]string{"account": p.Account, "holder": p.Holder})
		e := tx.QueryRow(ctx, `INSERT INTO withdrawals(user_id,amount,bank,bank_details) VALUES($1,$2,$3,$4) RETURNING id::text`, user, p.Amount, p.Bank, s.Store.Encrypt(string(details))).Scan(&id)
		if e != nil {
			return nil, e
		}
		e = Post(ctx, tx, "withdraw_hold:"+id, "Tạm giữ tiền rút", []Entry{{user, "available", -p.Amount}, {user, "held", p.Amount}})
		return map[string]any{"id": id, "status": "pending"}, e
	})
}

type Event struct {
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	BankReference string `json:"bankReference"`
	Evidence      string `json:"evidenceId"`
}

func (s *Service) Process(ctx context.Context, actor, id, key string, p Event) (any, error) {
	return s.Store.Action(ctx, actor, key, "withdraw-event:"+id, p, func(tx pgx.Tx) (any, error) {
		if _, e := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
			return nil, e
		}
		var user, st string
		var amount int64
		var processor *string
		e := tx.QueryRow(ctx, `SELECT user_id::text,status,amount,processor_id::text FROM withdrawals WHERE id=$1 FOR UPDATE`, id).Scan(&user, &st, &amount, &processor)
		if e == pgx.ErrNoRows {
			return nil, platform.Fail(404, "NOT_FOUND", "Không tìm thấy yêu cầu.")
		}
		if e != nil {
			return nil, e
		}
		next := ""
		switch p.Action {
		case "processing":
			if st != "pending" {
				return nil, platform.Fail(409, "INVALID_TRANSITION", "Yêu cầu không còn chờ xử lý.")
			}
			next = "processing"
		case "paid":
			if st != "processing" || processor == nil || *processor != actor {
				return nil, platform.Fail(409, "INVALID_TRANSITION", "Chỉ người nhận xử lý được xác nhận chi.")
			}
			if !platform.Text(p.BankReference, 3, 100) || !platform.ID(p.Evidence) {
				return nil, platform.Fail(422, "EVIDENCE_REQUIRED", "Cần mã giao dịch và bằng chứng đã tải lên.")
			}
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM private_files WHERE id=$1 AND owner_id=$2 AND purpose='evidence')`, p.Evidence, actor).Scan(&exists); e != nil {
				return nil, e
			}
			if !exists {
				return nil, platform.Fail(422, "EVIDENCE_REQUIRED", "Bằng chứng không hợp lệ.")
			}
			next = "paid"
			e = Post(ctx, tx, "withdraw_paid:"+id, "Đã chuyển khoản", []Entry{{user, "held", -amount}, {"", "system", amount}})
		case "rejected":
			if st != "pending" && st != "processing" {
				return nil, platform.Fail(409, "INVALID_TRANSITION", "Không thể từ chối yêu cầu này.")
			}
			if st == "processing" && (processor == nil || *processor != actor) {
				return nil, platform.Fail(409, "INVALID_TRANSITION", "Yêu cầu do người khác xử lý.")
			}
			if !platform.Text(p.Reason, 3, 500) {
				return nil, platform.Fail(422, "REASON_REQUIRED", "Cần lý do từ chối.")
			}
			next = "rejected"
			e = Post(ctx, tx, "withdraw_reject:"+id, "Hoàn tiền rút bị từ chối", []Entry{{user, "held", -amount}, {user, "available", amount}})
		default:
			return nil, platform.Fail(422, "INVALID_ACTION", "Thao tác không hợp lệ.")
		}
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE withdrawals SET status=$2,processor_id=$3,reason=$4,bank_reference=nullif($5,''),evidence_path=nullif($6,'') WHERE id=$1`, id, next, actor, p.Reason, p.BankReference, p.Evidence)
		if e != nil {
			return nil, platform.Conflict(e)
		}
		if next == "paid" {
			if e = RefreshGoldTotals(ctx, tx, user); e != nil {
				return nil, e
			}
		}
		e = platform.Audit(ctx, tx, actor, "withdraw_"+next, id, p)
		return map[string]string{"id": id, "status": next}, e
	})
}
