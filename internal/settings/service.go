package settings

import (
	"context"
	"encoding/json"
	"hoanxu/internal/platform"
	"net/mail"
)

type Service struct{ Store *platform.Store }
type FAQ struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}
type Input struct {
	Brand             string   `json:"brand"`
	SupportEmail      string   `json:"supportEmail"`
	WalletUnit        string   `json:"walletUnit"`
	XuPerVnd          int      `json:"xuPerVnd"`
	MaxDisplayPercent *float64 `json:"maxDisplayPercent"`
	FAQ               []FAQ    `json:"faq"`
}

func (s *Service) Update(ctx context.Context, actor string, p Input) error {
	p.WalletUnit = "xu"
	p.XuPerVnd = 1
	if len(p.FAQ) > 20 {
		return platform.Fail(422, "VALIDATION_ERROR", "Tối đa 20 câu hỏi thường gặp.")
	}
	for _, item := range p.FAQ {
		if !platform.Text(item.Question, 1, 150) || !platform.Text(item.Answer, 1, 1000) {
			return platform.Fail(422, "VALIDATION_ERROR", "Câu hỏi cần 1–150 ký tự, câu trả lời 1–1.000 ký tự.")
		}
	}
	if !platform.Text(p.Brand, 1, 30) {
		return platform.Fail(422, "VALIDATION_ERROR", "Tên thương hiệu không hợp lệ.")
	}
	if p.SupportEmail != "" {
		if _, e := mail.ParseAddress(p.SupportEmail); e != nil {
			return platform.Fail(422, "VALIDATION_ERROR", "Email hỗ trợ không hợp lệ.")
		}
	}
	if p.MaxDisplayPercent != nil && (*p.MaxDisplayPercent <= 0 || *p.MaxDisplayPercent > 100) {
		return platform.Fail(422, "VALIDATION_ERROR", "Mức hiển thị không hợp lệ.")
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT id FROM app_settings FOR UPDATE`); e != nil {
		return e
	}
	if p.FAQ == nil {
		var existing []byte
		if e = tx.QueryRow(ctx, `SELECT coalesce(settings->'faq','[]'::jsonb) FROM app_settings`).Scan(&existing); e != nil {
			return e
		}
		if e = json.Unmarshal(existing, &p.FAQ); e != nil {
			return e
		}
	}
	b, _ := json.Marshal(p)
	if _, e = tx.Exec(ctx, `UPDATE app_settings SET settings=$1`, b); e != nil {
		return e
	}
	if e = platform.Audit(ctx, tx, actor, "settings_updated", "app", p); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
