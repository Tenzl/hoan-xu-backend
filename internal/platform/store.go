package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hoanxu/internal/platform/db"
)

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string                    { return e.Message }
func Fail(status int, code, message string) error { return &Error{status, code, message} }

type Store struct {
	Pool        *pgxpool.Pool
	Cipher      cipher.AEAD
	Queries     *db.Queries
	trackingKey []byte
}

func New(pool *pgxpool.Pool, key string) (*Store, error) {
	raw, e := base64.StdEncoding.DecodeString(key)
	if e != nil || len(raw) != 32 {
		return nil, errors.New("DATA_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
	}
	block, e := aes.NewCipher(raw)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	h := hmac.New(sha256.New, raw)
	h.Write([]byte("hoanxu:cashback-link-signing:v1"))
	return &Store{Pool: pool, Cipher: a, Queries: db.New(pool), trackingKey: h.Sum(nil)}, e
}

// SignTracking binds the publisher and all four claims using an unambiguous encoding.
func (s *Store) SignTracking(publisher string, ids [4]string) string {
	b, _ := json.Marshal(struct {
		Publisher string
		SubIDs    [4]string
	}{publisher, ids})
	h := hmac.New(sha256.New, s.trackingKey)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)[:16])
}
func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (s *Store) Encrypt(value string) string {
	n := make([]byte, s.Cipher.NonceSize())
	if _, e := rand.Read(n); e != nil {
		panic(e)
	}
	return base64.StdEncoding.EncodeToString(s.Cipher.Seal(n, n, []byte(value), nil))
}
func (s *Store) Decrypt(value string) (string, error) {
	b, e := base64.StdEncoding.DecodeString(value)
	if e != nil || len(b) < s.Cipher.NonceSize() {
		return "", errors.New("invalid encrypted value")
	}
	v, e := s.Cipher.Open(nil, b[:s.Cipher.NonceSize()], b[s.Cipher.NonceSize():], nil)
	return string(v), e
}

// JSONRows deliberately projects API fields in SQL rather than exposing entire rows.
func (s *Store) Rows(ctx context.Context, q string, args ...any) ([]json.RawMessage, error) {
	rows, e := s.Pool.Query(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var v []byte
		if e = rows.Scan(&v); e != nil {
			return nil, e
		}
		out = append(out, json.RawMessage(v))
	}
	return out, rows.Err()
}
func (s *Store) One(ctx context.Context, q string, args ...any) (json.RawMessage, error) {
	var v []byte
	e := s.Pool.QueryRow(ctx, q, args...).Scan(&v)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, Fail(404, "NOT_FOUND", "Không tìm thấy dữ liệu.")
	}
	return json.RawMessage(v), e
}
func Audit(ctx context.Context, tx pgx.Tx, actor, action, resource string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO audit_logs(actor_id,action,resource,payload) VALUES(nullif($1,'')::uuid,$2,$3,$4)`, actor, action, resource, b)
	return e
}
func Accounts(ctx context.Context, tx pgx.Tx, user string) error {
	_, e := tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,kind) VALUES($1,'available'),($1,'held'),($1,'debt') ON CONFLICT DO NOTHING`, user)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,kind) SELECT $1,'gift_held' WHERE EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='gift_redemptions'::regclass AND attname='cost_xu' AND NOT attisdropped) ON CONFLICT DO NOTHING`, user); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,kind) SELECT $1,k.kind FROM (VALUES('green_available'),('green_gift_held')) k(kind) WHERE to_regclass('xu_exchange_policies') IS NOT NULL ON CONFLICT DO NOTHING`, user); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO coin_accounts(user_id) VALUES($1) ON CONFLICT DO NOTHING`, user)
	if e != nil {
		return e
	}
	var totals bool
	if e = tx.QueryRow(ctx, `SELECT to_regclass('wallet_user_totals') IS NOT NULL`).Scan(&totals); e != nil {
		return e
	}
	if totals {
		_, e = tx.Exec(ctx, `INSERT INTO wallet_user_totals(user_id) VALUES($1) ON CONFLICT DO NOTHING`, user)
	}
	return e
}

// Action serializes retries per actor/key and saves the result in the same transaction.
func (s *Store) Action(ctx context.Context, user, key, operation string, payload any, fn func(pgx.Tx) (any, error)) (any, error) {
	if len(key) < 8 || len(key) > 128 {
		return nil, Fail(422, "IDEMPOTENCY_REQUIRED", "Cần Idempotency-Key từ 8–128 ký tự.")
	}
	b, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	hash := Hash(string(b))
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, user+":"+key); e != nil {
		return nil, e
	}
	var oldOp, oldHash string
	var old []byte
	e = tx.QueryRow(ctx, `SELECT operation,payload_hash,response FROM idempotency_records WHERE user_id=$1 AND key=$2`, user, key).Scan(&oldOp, &oldHash, &old)
	if e == nil {
		if oldOp != operation || oldHash != hash {
			return nil, Fail(409, "IDEMPOTENCY_CONFLICT", "Key đã dùng cho yêu cầu khác.")
		}
		return json.RawMessage(old), nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	result, e := fn(tx)
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(result)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO idempotency_records(user_id,key,operation,payload_hash,response) VALUES($1,$2,$3,$4,$5)`, user, key, operation, hash, raw)
	if e != nil {
		return nil, e
	}
	return result, tx.Commit(ctx)
}

func (s *Store) Limit(ctx context.Context, key string, max int) error {
	window := time.Now().Unix() / 60
	var n int
	e := s.Pool.QueryRow(ctx, `INSERT INTO rate_limit_buckets(key,window_id,count,expires_at) VALUES($1,$2,1,now()+interval '2 minutes') ON CONFLICT(key,window_id) DO UPDATE SET count=rate_limit_buckets.count+1 RETURNING count`, key, window).Scan(&n)
	if e != nil {
		return e
	}
	if n > max {
		return Fail(429, "RATE_LIMITED", "Quá nhiều yêu cầu, vui lòng thử lại sau.")
	}
	return nil
}
func Text(v string, min, max int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= min && n <= max
}
func ID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func Conflict(e error) error {
	if e == nil {
		return nil
	}
	if strings.Contains(e.Error(), "SQLSTATE 23505") {
		return Fail(409, "CONFLICT", "Dữ liệu đã tồn tại.")
	}
	return e
}
func Ref(prefix, id string) string { return fmt.Sprintf("%s:%s", prefix, id) }
