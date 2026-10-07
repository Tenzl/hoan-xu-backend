package leaderboards

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
)

type PrizeService struct{ Store *platform.Store }
type GiftSnapshot struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ImageURL    string `json:"imageUrl"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}
type CampaignInput struct {
	WeekStart   time.Time `json:"weekStart"`
	GiftID      string    `json:"giftId"`
	Enabled     bool      `json:"enabled"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Version     int       `json:"version"`
}
type Campaign struct {
	ID            string       `json:"id"`
	WeekStart     time.Time    `json:"weekStart"`
	WeekEnd       time.Time    `json:"weekEnd"`
	GiftID        string       `json:"giftId"`
	Gift          GiftSnapshot `json:"gift"`
	Title         string       `json:"title"`
	Description   string       `json:"description"`
	Status        string       `json:"status"`
	ReservedCount int          `json:"reservedCount"`
	Version       int          `json:"version"`
	SettledAt     *time.Time   `json:"settledAt"`
}
type PrizePreview struct {
	Campaign  Campaign `json:"campaign"`
	Winners   []Entry  `json:"winners"`
	Hash      string   `json:"hash"`
	CanSettle bool     `json:"canSettle"`
}
type Award struct {
	ID           string       `json:"id"`
	CampaignID   string       `json:"campaignId"`
	UserID       string       `json:"userId"`
	UserName     string       `json:"userName"`
	Rank         int64        `json:"rank"`
	Xu           int64        `json:"xu"`
	Orders       int64        `json:"orders"`
	Gift         GiftSnapshot `json:"gift"`
	WeekStart    time.Time    `json:"weekStart"`
	WeekEnd      time.Time    `json:"weekEnd"`
	Status       string       `json:"status"`
	DeliveryNote string       `json:"deliveryNote"`
	DeliveredAt  *time.Time   `json:"deliveredAt"`
}
type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const campaignSQL = `SELECT jsonb_build_object('id',id,'weekStart',week_start,'weekEnd',week_end,'giftId',gift_id,'gift',gift_snapshot,'title',title,'description',description,'status',status,'reservedCount',reserved_count,'version',version,'settledAt',settled_at) FROM weekly_prize_campaigns`

func readCampaign(ctx context.Context, q querier, id string, lock bool) (Campaign, error) {
	query := campaignSQL + ` WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var raw []byte
	var c Campaign
	err := q.QueryRow(ctx, query, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, platform.Fail(404, "NOT_FOUND", "Không tìm thấy chương trình.")
	}
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, err
}
func (s *PrizeService) List(ctx context.Context) ([]json.RawMessage, error) {
	return s.Store.Rows(ctx, campaignSQL+` ORDER BY week_start DESC LIMIT 100`)
}
func (s *PrizeService) Current(ctx context.Context, now time.Time) (*Campaign, error) {
	var raw []byte
	err := s.Store.Pool.QueryRow(ctx, campaignSQL+` WHERE status='active' AND week_start<=$1 AND week_end>$1`, now).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Campaign
	err = json.Unmarshal(raw, &c)
	return &c, err
}
func (s *PrizeService) Save(ctx context.Context, actor, key string, p CampaignInput, now time.Time) (any, error) {
	p.Title = strings.TrimSpace(p.Title)
	p.Description = strings.TrimSpace(p.Description)
	if !platform.Text(p.Title, 1, 120) || !platform.Text(p.Description, 0, 500) || !platform.Text(p.GiftID, 1, 200) || p.Version < 0 {
		return nil, platform.Fail(422, "INVALID_CAMPAIGN", "Nhập quà, tiêu đề và lời giới thiệu hợp lệ.")
	}
	start, end, err := Bounds("week", now)
	if err != nil {
		return nil, err
	}
	if !p.WeekStart.Equal(*start) && !p.WeekStart.Equal(*end) {
		return nil, platform.Fail(422, "INVALID_PRIZE_WEEK", "Chỉ cấu hình tuần hiện tại hoặc tuần kế tiếp.")
	}
	return s.Store.Action(ctx, actor, key, "weekly-prize-save", p, func(tx pgx.Tx) (any, error) {
		// Serialize creation too: row locks alone cannot lock a missing week.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "prize-week:"+p.WeekStart.UTC().Format(time.RFC3339)); err != nil {
			return nil, err
		}
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM weekly_prize_campaigns WHERE week_start=$1`, p.WeekStart).Scan(&id)
		c := Campaign{WeekStart: p.WeekStart, WeekEnd: p.WeekStart.AddDate(0, 0, 7)}
		if err == nil {
			c, err = readCampaign(ctx, tx, id, true)
		} else if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		if err != nil {
			return nil, err
		}
		if c.Status == "settled" || !now.Before(c.WeekEnd) {
			return nil, platform.Fail(409, "CAMPAIGN_CLOSED", "Tuần đã kết thúc, không thể sửa chương trình.")
		}
		if c.Version != p.Version {
			return nil, platform.Fail(409, "CAMPAIGN_VERSION", "Cấu hình đã thay đổi. Tải lại trước khi lưu.")
		}
		// Acquire both catalog locks in a stable order before returning/reserving stock.
		rows, err := tx.Query(ctx, `SELECT id FROM gift_catalog WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, c.GiftID, p.GiftID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if c.ReservedCount > 0 {
			if _, err = tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock+$2 WHERE id=$1`, c.GiftID, c.ReservedCount); err != nil {
				return nil, err
			}
		}
		var raw []byte
		var stock int
		var active bool
		err = tx.QueryRow(ctx, `SELECT jsonb_build_object('id',id,'name',name,'imageUrl',image_url,'icon',icon,'description',description),stock,active FROM gift_catalog WHERE id=$1`, p.GiftID).Scan(&raw, &stock, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.Fail(422, "GIFT_UNAVAILABLE", "Quà không còn hoạt động.")
		}
		if err != nil {
			return nil, err
		}
		if !active && p.Enabled {
			return nil, platform.Fail(422, "GIFT_UNAVAILABLE", "Quà không còn hoạt động.")
		}
		c.Status = "draft"
		c.ReservedCount = 0
		if p.Enabled {
			if stock < 5 {
				return nil, platform.Fail(409, "INSUFFICIENT_PRIZE_STOCK", "Cần ít nhất năm phần quà trong kho để bật chương trình.")
			}
			if _, err = tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock-5 WHERE id=$1`, p.GiftID); err != nil {
				return nil, err
			}
			c.Status = "active"
			c.ReservedCount = 5
		}
		if err = json.Unmarshal(raw, &c.Gift); err != nil {
			return nil, err
		}
		c.Title = p.Title
		c.Description = p.Description
		c.GiftID = p.GiftID
		c.Version++
		if id == "" {
			err = tx.QueryRow(ctx, `INSERT INTO weekly_prize_campaigns(week_start,week_end,gift_id,gift_snapshot,title,description,status,reserved_count,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`, c.WeekStart, c.WeekEnd, c.GiftID, raw, c.Title, c.Description, c.Status, c.ReservedCount, actor).Scan(&c.ID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE weekly_prize_campaigns SET gift_id=$2,gift_snapshot=$3,title=$4,description=$5,status=$6,reserved_count=$7,version=$8,updated_at=now() WHERE id=$1`, id, c.GiftID, raw, c.Title, c.Description, c.Status, c.ReservedCount, c.Version)
		}
		if err != nil {
			return nil, err
		}
		if err = platform.Audit(ctx, tx, actor, "weekly_prize_configured", c.ID, p); err != nil {
			return nil, err
		}
		return c, nil
	})
}
func preview(ctx context.Context, q querier, c Campaign, now time.Time) (PrizePreview, error) {
	var raw []byte
	var board Snapshot
	err := q.QueryRow(ctx, rankingSQL, c.WeekStart, c.WeekEnd, nil, now).Scan(&raw)
	if err != nil {
		return PrizePreview{}, err
	}
	if err = json.Unmarshal(raw, &board); err != nil {
		return PrizePreview{}, err
	}
	winners := board.Items
	if len(winners) > 5 {
		winners = winners[:5]
	}
	b, err := json.Marshal(struct {
		Version int
		Winners []Entry
	}{c.Version, winners})
	if err != nil {
		return PrizePreview{}, err
	}
	return PrizePreview{Campaign: c, Winners: winners, Hash: platform.Hash(string(b)), CanSettle: c.Status == "active" && !now.Before(c.WeekEnd)}, nil
}
func (s *PrizeService) Preview(ctx context.Context, id string, now time.Time) (PrizePreview, error) {
	c, err := readCampaign(ctx, s.Store.Pool, id, false)
	if err != nil {
		return PrizePreview{}, err
	}
	if c.Status == "settled" {
		awards, err := s.Awards(ctx, id, "")
		winners := make([]Entry, 0, len(awards))
		for _, a := range awards {
			winners = append(winners, Entry{ID: a.UserID, Name: a.UserName, Rank: a.Rank, Xu: a.Xu, Orders: a.Orders})
		}
		return PrizePreview{Campaign: c, Winners: winners}, err
	}
	return preview(ctx, s.Store.Pool, c, now)
}
func (s *PrizeService) Settle(ctx context.Context, actor, key, id, hash string, now time.Time) (any, error) {
	return s.Store.Action(ctx, actor, key, "weekly-prize-settle:"+id, map[string]string{"hash": hash}, func(tx pgx.Tx) (any, error) {
		c, err := readCampaign(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if c.Status == "settled" {
			return c, nil
		}
		if c.Status != "active" || now.Before(c.WeekEnd) {
			return nil, platform.Fail(409, "PRIZE_NOT_ENDED", "Chỉ chốt chương trình đang bật sau khi tuần kết thúc.")
		}
		p, err := preview(ctx, tx, c, now)
		if err != nil {
			return nil, err
		}
		if hash == "" || hash != p.Hash {
			return nil, platform.Fail(409, "PRIZE_PREVIEW_CHANGED", "Top hoặc cấu hình đã thay đổi. Tải lại bản xem trước.")
		}
		gift, _ := json.Marshal(c.Gift)
		for _, w := range p.Winners {
			if _, err = tx.Exec(ctx, `INSERT INTO weekly_prize_awards(campaign_id,user_id,user_name,rank,xu,orders,gift_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, w.ID, w.Name, w.Rank, w.Xu, w.Orders, gift); err != nil {
				return nil, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES($1,'Bạn nhận quà Top 5 tuần!',$2)`, w.ID, "Phần thưởng: "+c.Gift.Name+". Xem tại trang Top."); err != nil {
				return nil, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock+$2 WHERE id=$1`, c.GiftID, c.ReservedCount-len(p.Winners)); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE weekly_prize_campaigns SET status='settled',reserved_count=0,version=version+1,settled_at=$2,updated_at=now() WHERE id=$1`, id, now); err != nil {
			return nil, err
		}
		if err = platform.Audit(ctx, tx, actor, "weekly_prize_settled", id, p.Winners); err != nil {
			return nil, err
		}
		return readCampaign(ctx, tx, id, false)
	})
}
func (s *PrizeService) Awards(ctx context.Context, campaign, user string) ([]Award, error) {
	rows, err := s.Store.Pool.Query(ctx, `SELECT a.id::text,a.campaign_id::text,a.user_id::text,a.user_name,a.rank,a.xu,a.orders,a.gift_snapshot,c.week_start,c.week_end,a.status,coalesce(a.delivery_cipher,''),a.delivered_at FROM weekly_prize_awards a JOIN weekly_prize_campaigns c ON c.id=a.campaign_id WHERE ($1::uuid IS NULL OR a.campaign_id=$1) AND ($2::uuid IS NULL OR a.user_id=$2) ORDER BY c.week_start DESC,a.rank`, nullableID(campaign), nullableID(user))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Award{}
	for rows.Next() {
		var a Award
		var raw []byte
		var encrypted string
		if err = rows.Scan(&a.ID, &a.CampaignID, &a.UserID, &a.UserName, &a.Rank, &a.Xu, &a.Orders, &raw, &a.WeekStart, &a.WeekEnd, &a.Status, &encrypted, &a.DeliveredAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &a.Gift); err != nil {
			return nil, err
		}
		if encrypted != "" {
			a.DeliveryNote, err = s.Store.Decrypt(encrypted)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func (s *PrizeService) Deliver(ctx context.Context, actor, key, id, note string) (any, error) {
	note = strings.TrimSpace(note)
	if !platform.Text(note, 1, 2000) {
		return nil, platform.Fail(422, "INVALID_PRIZE_DELIVERY", "Nhập mã voucher hoặc ghi chú giao quà từ 1–2000 ký tự.")
	}
	// Idempotency responses and audit contain no plaintext delivery information.
	return s.Store.Action(ctx, actor, key, "weekly-prize-deliver:"+id, map[string]string{"noteHash": platform.Hash(note)}, func(tx pgx.Tx) (any, error) {
		var status, owner string
		err := tx.QueryRow(ctx, `SELECT status,user_id::text FROM weekly_prize_awards WHERE id=$1 FOR UPDATE`, id).Scan(&status, &owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.Fail(404, "NOT_FOUND", "Không tìm thấy phần thưởng.")
		}
		if err != nil {
			return nil, err
		}
		if status == "delivered" {
			return map[string]string{"id": id, "status": "delivered"}, nil
		}
		if _, err = tx.Exec(ctx, `UPDATE weekly_prize_awards SET status='delivered',delivery_cipher=$2,delivered_by=$3,delivered_at=now() WHERE id=$1`, id, s.Store.Encrypt(note), actor); err != nil {
			return nil, err
		}
		if err = platform.Audit(ctx, tx, actor, "weekly_prize_delivered", id, nil); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES($1,'Quà Top tuần đã trao','Bạn có thể xem thông tin giao quà tại trang Top.')`, owner); err != nil {
			return nil, err
		}
		return map[string]string{"id": id, "status": "delivered"}, nil
	})
}
