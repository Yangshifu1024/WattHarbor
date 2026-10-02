package admin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"gorm.io/gorm"
	"time"
)

// WebhookDispatch 承接 worker 投递侧需要的 webhook 状态读写：
// 订阅清单读取与投递日志落库。配置写仍走 /api/v1/admin/webhooks，
// 投递日志每个订阅 + 事件只占一行，重试时就地更新，attempt_count 自增。
type WebhookDispatch struct {
	AdminDB *gorm.DB
}

// WebhookSubscription 是启用中的订阅，Secret 仅在内部网络上明文传输。
type WebhookSubscription struct {
	ID         uint64   `json:"id"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Secret     string   `json:"secret"`
	EventTypes []string `json:"event_types" gorm:"-"`
}

// ActiveSubscriptions 返回全部启用订阅并解码 event_types 的 JSON 列。
// 投递方按事件类型自行匹配；无效 JSON 视为不匹配任何事件，不中止清单。
func (w WebhookDispatch) ActiveSubscriptions(ctx context.Context) ([]WebhookSubscription, error) {
	rows, err := (database.WebhookDispatchStore{DB: w.AdminDB}).ActiveSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WebhookSubscription, 0, len(rows))
	for _, row := range rows {
		out = append(out, WebhookSubscription{ID: row.ID, Name: row.Name, URL: row.URL, Secret: row.Secret, EventTypes: row.EventTypes})
	}
	return out, nil
}

// RecordDeliveries 在一个事务里批量 upsert 投递结果。与旧 worker 版语义一致：
// 无论成功失败都写日志（投递日志就是审计凭证），重试覆盖旧行，
// attempt_count 由数据库自增，不由投递方计算。
func (w WebhookDispatch) RecordDeliveries(ctx context.Context, records []delivery.DeliveryRecord) error {
	if len(records) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]database.WebhookDeliveryRowInput, 0, len(records))
	for _, record := range records {
		rows = append(rows, database.WebhookDeliveryRowInput{
			SubscriptionID: record.SubscriptionID, EventID: record.EventID, EventType: record.EventType,
			RequestBody: record.RequestBody, ResponseStatus: record.ResponseStatus,
			ResponseBody: record.ResponseBody, ErrorMsg: errorOrNil(record.Error),
			DurationMS: durationOrNil(record.DurationMS), DeliveredAt: now,
		})
	}
	return (database.WebhookDispatchStore{DB: w.AdminDB}).RecordDeliveries(ctx, rows)
}

// durationOrNil 把"未测得耗时"表达为 nil：0 毫秒与没测到是两件事。
func durationOrNil(ms int64) *uint64 {
	if ms <= 0 {
		return nil
	}
	v := uint64(ms)
	return &v
}

// errorOrNil 把空错误串转成 nil，让日志里的"没有错误"是 NULL 而不是空串。
func errorOrNil(message string) *string {
	if message == "" {
		return nil
	}
	return &message
}

// RegulatoryDispatch 承接监管报送的租约领取与状态推进，全部在 central
// 单库事务内完成；worker 只经 Sender 签名发送，按 lease_token 回执结果。
type RegulatoryDispatch struct {
	AdminDB *gorm.DB
}

// ClaimedReport 是一笔领取到的待报送记录及其租约凭证。
type ClaimedReport struct {
	ID         uint64          `json:"id"`
	EventID    string          `json:"event_id"`
	ObjectType string          `json:"object_type"`
	ObjectKey  string          `json:"object_key"`
	Data       json.RawMessage `json:"data"`
	LeaseToken string          `json:"lease_token"`
}

// Claim 在一个事务内锁定并领取待报送记录：行锁取代旧实现的乐观租约重查，
// 语义不变——queued/processing 且到期的记录置为 processing 并获得 2 分钟租约。
func (r RegulatoryDispatch) Claim(ctx context.Context, limit int) ([]ClaimedReport, error) {
	if limit <= 0 || limit > 20 {
		return nil, errors.New("claim limit must be between 1 and 20")
	}
	now := time.Now().UTC()
	store := database.WebhookDispatchStore{DB: r.AdminDB}
	tx := r.AdminDB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback().Error
		}
	}()
	claimed, err := store.ClaimRegulatoryReports(tx, now, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	committed = true
	out := make([]ClaimedReport, 0, len(claimed))
	for _, row := range claimed {
		out = append(out, ClaimedReport{ID: row.ID, EventID: row.EventID, ObjectType: row.ObjectType, ObjectKey: row.ObjectKey, Data: row.Data, LeaseToken: row.LeaseToken})
	}
	return out, nil
}

// FinishItem 是一笔报送的发送结果；lease_token 不匹配的记录被跳过（租约已失效）。
type FinishItem struct {
	ID         uint64 `json:"id" binding:"required"`
	LeaseToken string `json:"lease_token" binding:"required"`
	Delivered  bool   `json:"delivered"`
	Mode       string `json:"mode"`
	Error      string `json:"error"`
}

// FinishResult 汇总状态推进结果：finished 是推进成功的笔数，lost 是
// 租约已被抢占而跳过的笔数。
type FinishResult struct {
	Finished int `json:"finished"`
	Lost     int `json:"lost"`
}

// Finish 在一个事务内按 lease_token 条件推进状态。成功置 delivered；
// 失败回到 queued 并按有界指数退避（2 的 attempts 次幂秒，封顶 2^11）重排。
func (r RegulatoryDispatch) Finish(ctx context.Context, items []FinishItem) (FinishResult, error) {
	now := time.Now().UTC()
	finishes := make([]database.RegulatoryFinish, 0, len(items))
	for _, item := range items {
		finishes = append(finishes, database.RegulatoryFinish{
			ID: item.ID, LeaseToken: item.LeaseToken, Delivered: item.Delivered, Mode: item.Mode, Error: item.Error,
		})
	}
	store := database.WebhookDispatchStore{DB: r.AdminDB}
	var result FinishResult
	err := r.AdminDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result.Finished, result.Lost, err = store.FinishRegulatoryReports(tx, now, finishes)
		return err
	})
	return result, err
}
