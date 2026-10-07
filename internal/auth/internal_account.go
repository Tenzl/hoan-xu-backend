package auth

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
)

// UpdateInternal changes exactly one administrative property, in the same
// transaction as session revocation and the audit record. A password is never
// included in an audit payload.
func (s *Service) UpdateInternal(ctx context.Context, actor, id, action string, permissions []string, password string, blocked bool) error {
	if actor == id {
		return platform.Fail(409, "SELF_RESET_DENIED", "Dùng Tài khoản của tôi để quản lý tài khoản hiện tại.")
	}
	var hash string
	var err error
	switch action {
	case "permissions":
		valid := map[string]bool{"orders": true, "withdrawals": true, "users": true, "gifts": true, "community": true, "notifications": true, "settings": true, "audit": true}
		seen := map[string]bool{}
		for _, p := range permissions {
			if !valid[p] || seen[p] {
				return platform.Fail(422, "INVALID_PERMISSION", "Quyền không hợp lệ.")
			}
			seen[p] = true
		}
	case "reset-password":
		hash, err = HashPassword(password)
		if err != nil {
			return platform.Fail(422, "INVALID_PASSWORD", err.Error())
		}
	case "status":
	default:
		return platform.Fail(422, "VALIDATION_ERROR", "Thao tác tài khoản không hợp lệ.")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var role string
	err = tx.QueryRow(ctx, `SELECT u.role FROM users u JOIN internal_credentials c ON c.user_id=u.id WHERE u.id=$1 FOR UPDATE OF u,c`, id).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return platform.Fail(404, "NOT_FOUND", "Không có tài khoản nội bộ.")
	}
	if err != nil {
		return err
	}
	payload := map[string]any{}
	auditAction := ""
	switch action {
	case "permissions":
		if role != "staff" {
			return platform.Fail(409, "ADMIN_FULL_ACCESS", "Quản trị viên có toàn quyền.")
		}
		if _, err = tx.Exec(ctx, `DELETE FROM user_permissions WHERE user_id=$1`, id); err != nil {
			return err
		}
		for _, p := range permissions {
			if _, err = tx.Exec(ctx, `INSERT INTO user_permissions(user_id,permission) VALUES($1,$2)`, id, p); err != nil {
				return err
			}
		}
		payload["permissions"] = permissions
		auditAction = "internal_permissions_updated"
	case "reset-password":
		_, err = tx.Exec(ctx, `UPDATE internal_credentials SET password_hash=$2,must_change=true WHERE user_id=$1`, id, hash)
		auditAction = "internal_password_reset"
	case "status":
		_, err = tx.Exec(ctx, `UPDATE users SET blocked=$2 WHERE id=$1`, id, blocked)
		payload["blocked"] = blocked
		auditAction = "internal_status_updated"
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
		return err
	}
	if err = platform.Audit(ctx, tx, actor, auditAction, id, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
