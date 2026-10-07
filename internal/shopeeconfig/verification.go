package shopeeconfig

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"time"
)

type Verification struct {
	ID           string     `json:"id"`
	Version      string     `json:"version"`
	Status       string     `json:"status"`
	Stage        string     `json:"stage"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	ErrorCode    *string    `json:"errorCode"`
	ErrorMessage *string    `json:"errorMessage"`
}

func StartVerification(ctx context.Context, s *platform.Store, origin, actor, version, productURL string) (Verification, Config, error) {
	tx, cfg, _, err := lock(ctx, s, origin)
	if err != nil {
		return Verification{}, cfg, err
	}
	defer tx.Rollback(ctx)
	if version != cfg.Version {
		return Verification{}, cfg, platform.Fail(409, "SHOPEE_SETTINGS_CONFLICT", "Cấu hình đã thay đổi ở nơi khác. Tải lại cấu hình trước khi lưu.")
	}
	if cfg.Publisher == "" {
		return Verification{}, cfg, platform.Fail(422, "PUBLISHER_NOT_CONFIGURED", "Nhập Affiliate ID và lưu cấu hình trước khi kiểm tra.")
	}
	_, err = tx.Exec(ctx, `UPDATE shopee_verifications SET status='cancelled',stage='finished',finished_at=now(),error_code='VERIFICATION_EXPIRED',error_message='Tác vụ kiểm tra đã hết hạn.' WHERE origin_key=$1 AND status IN ('queued','running') AND expires_at<=now()`, Key(origin))
	if err != nil {
		return Verification{}, cfg, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM shopee_verifications WHERE origin_key=$1 AND status IN ('queued','running')`, Key(origin)).Scan(&count); err != nil {
		return Verification{}, cfg, err
	}
	if count > 0 {
		return Verification{}, cfg, platform.Fail(409, "VERIFICATION_RUNNING", "Đang có tác vụ kiểm tra Shopee. Chờ kết quả trước khi kiểm tra lại.")
	}
	v := Verification{Version: cfg.Version, Status: "queued", Stage: "queued"}
	err = tx.QueryRow(ctx, `INSERT INTO shopee_verifications(origin_key,configuration_version,fingerprint,product_url,status) VALUES($1,$2,$3,$4,'queued') RETURNING id::text,created_at,expires_at`, Key(origin), cfg.Version, cfg.Fingerprint(), productURL).Scan(&v.ID, &v.CreatedAt, &v.ExpiresAt)
	if err != nil {
		return v, cfg, err
	}
	if err = platform.Audit(ctx, tx, actor, "shopee_verification_started", "shopee", map[string]string{"origin": origin, "id": v.ID, "version": cfg.Version}); err != nil {
		return v, cfg, err
	}
	if err = tx.Commit(ctx); err != nil {
		return v, cfg, err
	}
	return v, cfg, nil
}
func GetVerification(ctx context.Context, s *platform.Store, origin, id string) (Verification, error) {
	v := Verification{}
	// Expired/restarted jobs never appear successful or block another attempt.
	_, err := s.Pool.Exec(ctx, `UPDATE shopee_verifications SET status='cancelled',stage='finished',finished_at=now(),error_code='VERIFICATION_EXPIRED',error_message='Tác vụ kiểm tra đã hết hạn.' WHERE id=$1 AND origin_key=$2 AND status IN ('queued','running') AND expires_at<=now()`, id, Key(origin))
	if err != nil {
		return v, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT id::text,configuration_version,status,stage,created_at,expires_at,finished_at,error_code,error_message FROM shopee_verifications WHERE id=$1 AND origin_key=$2`, id, Key(origin)).Scan(&v.ID, &v.Version, &v.Status, &v.Stage, &v.CreatedAt, &v.ExpiresAt, &v.FinishedAt, &v.ErrorCode, &v.ErrorMessage)
	if errors.Is(err, pgx.ErrNoRows) {
		err = platform.Fail(404, "NOT_FOUND", "Không tìm thấy tác vụ kiểm tra.")
	}
	return v, err
}
func FinishVerification(ctx context.Context, s *platform.Store, origin, id string, checkErr error) error {
	tx, cfg, all, err := lock(ctx, s, origin)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var version, fingerprint, status string
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT configuration_version,fingerprint,status,expires_at FROM shopee_verifications WHERE id=$1 AND origin_key=$2 FOR UPDATE`, id, Key(origin)).Scan(&version, &fingerprint, &status, &expires)
	if err != nil {
		return err
	}
	if status != "queued" && status != "running" {
		return nil
	}
	state, code, message := "succeeded", "", ""
	if cfg.Version != version || cfg.Fingerprint() != fingerprint || !time.Now().Before(expires) {
		state = "cancelled"
		code = "VERIFICATION_STALE"
		message = "Cấu hình đã thay đổi hoặc tác vụ hết hạn. Kiểm tra lại cấu hình hiện tại."
	} else if checkErr != nil {
		state = "failed"
		code = "SHOPEE_VERIFICATION_FAILED"
		message = "Kiểm tra Shopee chưa thành công. Mở Chrome để đăng nhập hoặc xác minh rồi thử lại."
		var p *platform.Error
		if errors.As(checkErr, &p) {
			code = p.Code
			message = p.Message
		}
	} else {
		now := time.Now().UTC()
		cfg.VerifiedAt = &now
		cfg.VerifiedFingerprint = fingerprint
		cfg.TrackingVerified = true
		cfg.SchemaVerified = true
		cfg.Revision++
		cfg.Version = ""
		if err = persist(ctx, tx, origin, cfg, all); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE shopee_verifications SET status=$3,stage='finished',finished_at=now(),error_code=nullif($4,''),error_message=nullif($5,'') WHERE id=$1 AND origin_key=$2`, id, Key(origin), state, code, message)
	if err != nil {
		return err
	}
	if err = platform.Audit(ctx, tx, "", "shopee_verification_finished", "shopee", map[string]string{"origin": origin, "id": id, "status": state, "code": code}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func RecoverVerifications(ctx context.Context, s *platform.Store, origin string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE shopee_verifications SET status='cancelled',stage='finished',finished_at=now(),error_code='BACKEND_RESTARTED',error_message='Backend đã khởi động lại. Vui lòng kiểm tra lại.' WHERE origin_key=$1 AND status IN ('queued','running')`, Key(origin))
	return err
}
