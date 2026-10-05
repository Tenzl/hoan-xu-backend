package users

import (
	"context"
	"encoding/json"
	"hoanxu/internal/platform"
	"regexp"
	"strings"
)

type BankDetails struct {
	Bank    string `json:"bank"`
	Account string `json:"account"`
	Holder  string `json:"holder"`
}
type ProfileInput struct {
	Name        *string      `json:"name"`
	BankDetails *BankDetails `json:"bankDetails"`
}
type Service struct{ Store *platform.Store }

func (s *Service) Update(ctx context.Context, id, role string, p ProfileInput) error {
	if p.Name == nil && p.BankDetails == nil {
		return platform.Fail(422, "VALIDATION_ERROR", "Cần thông tin hồ sơ cần cập nhật.")
	}
	if p.Name != nil && !platform.Text(*p.Name, 1, 80) {
		return platform.Fail(422, "VALIDATION_ERROR", "Tên cần 1–80 ký tự.")
	}
	var encrypted any
	if p.BankDetails != nil {
		if role != "customer" {
			return platform.Fail(403, "CUSTOMER_REQUIRED", "Thông tin ngân hàng dành cho khách hàng.")
		}
		p.BankDetails.Bank = strings.TrimSpace(p.BankDetails.Bank)
		p.BankDetails.Account = strings.TrimSpace(p.BankDetails.Account)
		p.BankDetails.Holder = strings.TrimSpace(p.BankDetails.Holder)
		if p.BankDetails.Bank != "" || p.BankDetails.Account != "" || p.BankDetails.Holder != "" {
			if !platform.Text(p.BankDetails.Bank, 2, 80) || !platform.Text(p.BankDetails.Holder, 2, 80) || !regexp.MustCompile(`^[0-9]{6,20}$`).MatchString(p.BankDetails.Account) {
				return platform.Fail(422, "INVALID_BANK_DETAILS", "Nhập ngân hàng, số tài khoản gồm 6–20 chữ số và họ tên đầy đủ đúng như hiển thị trên ngân hàng.")
			}
			raw, e := json.Marshal(p.BankDetails)
			if e != nil {
				return e
			}
			encrypted = s.Store.Encrypt(string(raw))
		}
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if p.Name != nil {
		if _, e = tx.Exec(ctx, `UPDATE users SET name=$2 WHERE id=$1`, id, strings.TrimSpace(*p.Name)); e != nil {
			return e
		}
	}
	if p.BankDetails != nil {
		if _, e = tx.Exec(ctx, `UPDATE users SET bank_details=$2 WHERE id=$1`, id, encrypted); e != nil {
			return e
		}
		// Bank numbers and names are deliberately excluded from the audit payload.
		if e = platform.Audit(ctx, tx, id, "bank_profile_updated", id, map[string]bool{"configured": encrypted != nil}); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
