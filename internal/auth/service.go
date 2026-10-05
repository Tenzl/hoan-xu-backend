package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"hoanxu/internal/platform"
	"hoanxu/internal/users"
	"strings"
)

type User struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Email       string             `json:"email"`
	Role        string             `json:"role"`
	Blocked     bool               `json:"blocked"`
	Tracking    string             `json:"trackingCode"`
	MustChange  bool               `json:"mustChangePassword"`
	Permissions []string           `json:"permissions"`
	SessionID   string             `json:"sessionId"`
	CSRF        string             `json:"csrfToken"`
	Recent      bool               `json:"recentAuthentication"`
	BankDetails *users.BankDetails `json:"bankDetails"`
}
type Service struct {
	Store    *platform.Store
	OAuth    *oauth2.Config
	Verifier *oidc.IDTokenVerifier
}

func (s *Service) ConfigureGoogle(ctx context.Context, client, secret, callback string) error {
	if client == "" || secret == "" {
		return nil
	}
	p, e := oidc.NewProvider(ctx, "https://accounts.google.com")
	if e != nil {
		return e
	}
	s.OAuth = &oauth2.Config{ClientID: client, ClientSecret: secret, RedirectURL: callback, Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}
	s.Verifier = p.Verifier(&oidc.Config{ClientID: client})
	return nil
}
func (s *Service) Session(ctx context.Context, token string) (*User, error) {
	u := &User{Permissions: []string{}}
	e := s.Store.Pool.QueryRow(ctx, `SELECT u.id::text,u.name,u.email,u.role,u.blocked,u.tracking_code,coalesce(c.must_change,false),s.id::text,s.csrf_token,coalesce(s.reauthenticated_at>now()-interval '15 minutes',false) FROM sessions s JOIN users u ON u.id=s.user_id LEFT JOIN internal_credentials c ON c.user_id=u.id WHERE s.token_hash=$1 AND s.expires_at>now()`, platform.Hash(token)).Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Blocked, &u.Tracking, &u.MustChange, &u.SessionID, &u.CSRF, &u.Recent)
	if e != nil || u.Blocked {
		return nil, platform.Fail(401, "UNAUTHENTICATED", "Vui lòng đăng nhập.")
	}
	if u.Role == "customer" {
		var encrypted *string
		if e = s.Store.Pool.QueryRow(ctx, `SELECT bank_details FROM users WHERE id=$1`, u.ID).Scan(&encrypted); e != nil {
			return nil, e
		}
		if encrypted != nil {
			plain, err := s.Store.Decrypt(*encrypted)
			if err != nil {
				return nil, err
			}
			u.BankDetails = &users.BankDetails{}
			if e = json.Unmarshal([]byte(plain), u.BankDetails); e != nil {
				return nil, e
			}
		}
	}
	rows, e := s.Store.Pool.Query(ctx, `SELECT permission FROM user_permissions WHERE user_id=$1`, u.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			return nil, e
		}
		u.Permissions = append(u.Permissions, p)
	}
	return u, rows.Err()
}
func (u *User) Can(permission string) bool {
	if u.Role == "admin" {
		return true
	}
	if u.Role != "staff" {
		return false
	}
	for _, p := range u.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}
func (s *Service) NewSession(ctx context.Context, tx pgx.Tx, user string) (string, error) {
	token, csrf := platform.Token(), platform.Token()
	_, e := tx.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,csrf_hash,csrf_token,expires_at) VALUES($1,$2,$3,$4,now()+interval '7 days')`, user, platform.Hash(token), platform.Hash(csrf), csrf)
	return token, e
}
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	var id, hash string
	var blocked bool
	e := s.Store.Pool.QueryRow(ctx, `SELECT u.id::text,c.password_hash,u.blocked FROM internal_credentials c JOIN users u ON u.id=c.user_id WHERE c.username=$1 AND u.role IN ('staff','admin')`, strings.ToLower(strings.TrimSpace(username))).Scan(&id, &hash, &blocked)
	if errors.Is(e, pgx.ErrNoRows) {
		VerifyPassword(dummyHash, password)
	}
	if e != nil || blocked || !VerifyPassword(hash, password) {
		return "", platform.Fail(401, "INVALID_CREDENTIALS", "Tài khoản hoặc mật khẩu không đúng.")
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	token, e := s.NewSession(ctx, tx, id)
	if e != nil {
		return "", e
	}
	return token, tx.Commit(ctx)
}

var dummyHash = func() string { v, _ := HashPassword("dummy-login-password"); return v }()

func (s *Service) Reauth(ctx context.Context, u *User, password string) error {
	var h string
	e := s.Store.Pool.QueryRow(ctx, `SELECT password_hash FROM internal_credentials WHERE user_id=$1`, u.ID).Scan(&h)
	if e != nil || !VerifyPassword(h, password) {
		return platform.Fail(401, "INVALID_CREDENTIALS", "Mật khẩu không đúng.")
	}
	_, e = s.Store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, u.SessionID)
	return e
}
func (s *Service) ChangePassword(ctx context.Context, u *User, old, password string) error {
	if e := s.Reauth(ctx, u, old); e != nil {
		return e
	}
	h, e := HashPassword(password)
	if e != nil {
		return platform.Fail(422, "INVALID_PASSWORD", e.Error())
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE internal_credentials SET password_hash=$2,must_change=false WHERE user_id=$1`, u.ID, h)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND id<>$2`, u.ID, u.SessionID)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) GoogleStart(ctx context.Context) (string, string, error) {
	if s.OAuth == nil {
		return "", "", platform.Fail(503, "GOOGLE_NOT_CONFIGURED", "Chưa cấu hình Google OAuth.")
	}
	state, nonce, verifier := platform.Token(), platform.Token(), oauth2.GenerateVerifier()
	_, e := s.Store.Pool.Exec(ctx, `INSERT INTO oauth_requests(state_hash,nonce,verifier,expires_at) VALUES($1,$2,$3,now()+interval '10 minutes')`, platform.Hash(state), nonce, verifier)
	return s.OAuth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), state, e
}
func (s *Service) GoogleFinish(ctx context.Context, state, code string) (string, error) {
	if s.OAuth == nil {
		return "", platform.Fail(503, "GOOGLE_NOT_CONFIGURED", "Chưa cấu hình Google.")
	}
	var nonce, verifier string
	e := s.Store.Pool.QueryRow(ctx, `DELETE FROM oauth_requests WHERE state_hash=$1 AND expires_at>now() RETURNING nonce,verifier`, platform.Hash(state)).Scan(&nonce, &verifier)
	if e != nil {
		return "", platform.Fail(401, "OAUTH_STATE_INVALID", "Phiên đăng nhập Google không hợp lệ.")
	}
	tok, e := s.OAuth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if e != nil {
		return "", platform.Fail(401, "OAUTH_FAILED", "Google không xác thực được phiên này.")
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return "", platform.Fail(401, "OAUTH_FAILED", "Thiếu Google ID token.")
	}
	id, e := s.Verifier.Verify(ctx, raw)
	if e != nil || id.Nonce != nonce || !platform.Text(id.Subject, 1, 255) {
		return "", platform.Fail(401, "OAUTH_FAILED", "Google token không hợp lệ.")
	}
	var c struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if e = id.Claims(&c); e != nil || !c.Verified || !platform.Text(c.Email, 3, 254) {
		return "", platform.Fail(401, "OAUTH_FAILED", "Email Google chưa được xác minh.")
	}
	if c.Name == "" {
		c.Name = "Thành viên"
	}
	if len([]rune(c.Name)) > 80 {
		c.Name = string([]rune(c.Name)[:80])
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id.Subject)
	if e != nil {
		return "", e
	}
	var uid string
	e = tx.QueryRow(ctx, `SELECT user_id::text FROM auth_identities WHERE subject=$1`, id.Subject).Scan(&uid)
	if errors.Is(e, pgx.ErrNoRows) {
		e = tx.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES($1,$2,'customer') RETURNING id::text`, c.Name, c.Email).Scan(&uid)
		if e == nil {
			_, e = tx.Exec(ctx, `INSERT INTO auth_identities(subject,user_id) VALUES($1,$2)`, id.Subject, uid)
		}
		if e == nil {
			e = platform.Accounts(ctx, tx, uid)
		}
	}
	if e != nil {
		return "", e
	}
	var blocked bool
	if e = tx.QueryRow(ctx, `SELECT blocked FROM users WHERE id=$1`, uid).Scan(&blocked); e != nil {
		return "", e
	}
	if blocked {
		return "", platform.Fail(403, "BLOCKED", "Tài khoản đã khóa.")
	}
	token, e := s.NewSession(ctx, tx, uid)
	if e != nil {
		return "", e
	}
	return token, tx.Commit(ctx)
}
func CreateInternal(ctx context.Context, store *platform.Store, actor, username, name, password, role string, permissions []string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !platform.Text(username, 3, 40) || !platform.Text(name, 1, 80) || (role != "staff" && role != "admin") {
		return "", platform.Fail(422, "VALIDATION_ERROR", "Thông tin tài khoản không hợp lệ.")
	}
	for _, r := range username {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return "", platform.Fail(422, "VALIDATION_ERROR", "Username chỉ gồm chữ, số, dấu . _ -")
		}
	}
	h, e := HashPassword(password)
	if e != nil {
		return "", platform.Fail(422, "INVALID_PASSWORD", e.Error())
	}
	tx, e := store.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	var uid string
	e = tx.QueryRow(ctx, `INSERT INTO users(name,role) VALUES($1,$2) RETURNING id::text`, name, role).Scan(&uid)
	if e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `INSERT INTO internal_credentials(user_id,username,password_hash) VALUES($1,$2,$3)`, uid, username, h)
	if e != nil {
		return "", platform.Conflict(e)
	}
	for _, p := range permissions {
		if _, e = tx.Exec(ctx, `INSERT INTO user_permissions VALUES($1,$2)`, uid, p); e != nil {
			return "", platform.Fail(422, "INVALID_PERMISSION", "Quyền không hợp lệ.")
		}
	}
	e = platform.Audit(ctx, tx, actor, "internal_account_created", uid, map[string]any{"role": role, "permissions": permissions, "cli": actor == ""})
	if e != nil {
		return "", e
	}
	return uid, tx.Commit(ctx)
}
func (s *Service) ResetInternal(ctx context.Context, actor, id, password string, permissions []string, blocked bool) error {
	h, e := HashPassword(password)
	if e != nil {
		return platform.Fail(422, "INVALID_PASSWORD", e.Error())
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE internal_credentials SET password_hash=$2,must_change=true WHERE user_id=$1`, id, h)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return platform.Fail(404, "NOT_FOUND", "Không có tài khoản nội bộ.")
	}
	if _, e = tx.Exec(ctx, `UPDATE users SET blocked=$2 WHERE id=$1`, id, blocked); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM user_permissions WHERE user_id=$1`, id); e != nil {
		return e
	}
	for _, p := range permissions {
		if _, e = tx.Exec(ctx, `INSERT INTO user_permissions VALUES($1,$2)`, id, p); e != nil {
			return platform.Fail(422, "INVALID_PERMISSION", "Quyền không hợp lệ.")
		}
	}
	if e = platform.Audit(ctx, tx, actor, "internal_account_reset", id, map[string]any{"permissions": permissions, "blocked": blocked, "cli": actor == ""}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
