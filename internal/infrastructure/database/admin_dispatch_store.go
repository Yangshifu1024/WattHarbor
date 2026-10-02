package database

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 投递侧读取与写入：Webhook 订阅清单与投递日志、监管报送的租约领取与状态推进。
// 配置的写仍走后台接口；这里只承接 worker 投递时需要的状态读写。
//
// 监管报送用租约而不是行锁跨请求持有：领取时给记录打上 lease_token 与
// 过期时间，发送完成再凭 token 推进状态。token 不匹配的更新影响 0 行，
// 说明租约已被别人抢走，本次结果作废——这比长时间持锁更耐进程崩溃。

// WebhookDispatchStore 读取启用中的订阅并写入投递结果。
type WebhookDispatchStore struct{ DB *gorm.DB }

// WebhookSubscriptionRow 是启用中的订阅。secret 只在内部网络上明文传输，
// 不会出现在任何面向前端的响应里。
type WebhookSubscriptionRow struct {
	ID         uint64   `json:"id"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Secret     string   `json:"secret"`
	EventTypes []string `json:"event_types" gorm:"-"`
}

// ActiveSubscriptions 返回全部启用订阅并解码 event_types 的 JSON 列。
// 事件类型无效的订阅按"不匹配任何事件"处理，不中止整份清单：一条坏数据
// 不该让所有订阅都投不出去。
func (s WebhookDispatchStore) ActiveSubscriptions(ctx context.Context) ([]WebhookSubscriptionRow, error) {
	rows := []WebhookSubscriptionRow{}
	if err := s.DB.WithContext(ctx).Table("webhook_subscription").
		Select("id, name, url, secret").Where("enabled = 1 AND deleted_at IS NULL").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var types []WebhookEventTypesRow
	if err := s.DB.WithContext(ctx).Table("webhook_subscription").
		Select("id, CAST(event_types AS CHAR) AS event_types").
		Where("id IN ?", ids).Find(&types).Error; err != nil {
		return nil, err
	}
	byID := map[uint64][]string{}
	for _, row := range types {
		var decoded []string
		if len(row.EventTypes) > 0 {
			_ = json.Unmarshal(row.EventTypes, &decoded)
		}
		byID[row.ID] = decoded
	}
	for i := range rows {
		rows[i].EventTypes = byID[rows[i].ID]
	}
	return rows, nil
}

// WebhookDeliveryRowInput 是一次投递结果。三个可空列用指针区分"没有响应"
// 与"响应是空串"：投递日志是审计凭证，这个区别不能丢。
type WebhookDeliveryRowInput struct {
	SubscriptionID uint64
	EventID        string
	EventType      string
	RequestBody    string
	ResponseStatus *int
	ResponseBody   *string
	ErrorMsg       *string
	DurationMS     *uint64
	DeliveredAt    time.Time
}

// RecordDeliveries 批量 upsert 投递结果：无论成功失败都写日志。
// 每个订阅 + 事件只占一行，重试时就地更新，attempt_count 由数据库自增
// （webhook_delivery_log.attempt_count + 1），不由投递方计算——
// 投递方算错会让重试次数对不上真实投递次数。
func (s WebhookDispatchStore) RecordDeliveries(ctx context.Context, records []WebhookDeliveryRowInput) error {
	if len(records) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(records))
	for _, record := range records {
		rows = append(rows, map[string]any{
			"subscription_id": record.SubscriptionID,
			"event_id":        record.EventID,
			"event_type":      record.EventType,
			"request_body":    record.RequestBody,
			"response_status": record.ResponseStatus,
			"response_body":   record.ResponseBody,
			"error_msg":       record.ErrorMsg,
			"attempt_count":   1,
			"duration_ms":     record.DurationMS,
			"delivered_at":    record.DeliveredAt,
		})
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Table("webhook_delivery_log").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "subscription_id"}, {Name: "event_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"event_type":      gorm.Expr("VALUES(event_type)"),
				"request_body":    gorm.Expr("VALUES(request_body)"),
				"response_status": gorm.Expr("VALUES(response_status)"),
				"response_body":   gorm.Expr("VALUES(response_body)"),
				"error_msg":       gorm.Expr("VALUES(error_msg)"),
				"attempt_count":   gorm.Expr("webhook_delivery_log.attempt_count + 1"),
				"duration_ms":     gorm.Expr("VALUES(duration_ms)"),
				"delivered_at":    gorm.Expr("VALUES(delivered_at)"),
			}),
		}).Create(&rows).Error
	})
}

// PendingRegulatoryRow 是待领取的一条报送。
type PendingRegulatoryRow struct {
	ID         uint64
	EventID    string
	ObjectType string
	ObjectKey  string
	Payload    string
}

// ClaimedRegulatoryReport 是已领取到本进程的报送，LeaseToken 是归还租约的凭据。
type ClaimedRegulatoryReport struct {
	ID         uint64
	EventID    string
	ObjectType string
	ObjectKey  string
	Data       json.RawMessage
	LeaseToken string
}

// leaseDuration 是一次报送发送的租约时长。超时未归还的记录会被下一个进程重新领取。
const leaseDuration = 2 * time.Minute

// ClaimRegulatoryReports 领取最多 limit 条到期待发的报送，给每条打上新的租约令牌。
// 选取与打标记在同一个事务里完成，避免两个进程领到同一条。
func (s WebhookDispatchStore) ClaimRegulatoryReports(tx *gorm.DB, now time.Time, limit int) ([]ClaimedRegulatoryReport, error) {
	var pending []PendingRegulatoryRow
	if err := tx.Raw(`SELECT id,event_id,object_type,object_key,CAST(payload_json AS CHAR) AS payload
		FROM regulatory_report WHERE status IN ('queued','processing') AND next_attempt_at <= ?
		AND (lease_until IS NULL OR lease_until < ?) ORDER BY id LIMIT ? FOR UPDATE`,
		now, now, limit).Scan(&pending).Error; err != nil {
		return nil, err
	}
	claimed := make([]ClaimedRegulatoryReport, 0, len(pending))
	for _, row := range pending {
		token := uuid.NewString()
		if err := tx.Exec(`UPDATE regulatory_report SET status='processing',lease_token=?,lease_until=? WHERE id=?`,
			token, now.Add(leaseDuration), row.ID).Error; err != nil {
			return nil, err
		}
		claimed = append(claimed, ClaimedRegulatoryReport{
			ID: row.ID, EventID: row.EventID, ObjectType: row.ObjectType,
			ObjectKey: row.ObjectKey, Data: json.RawMessage(row.Payload), LeaseToken: token,
		})
	}
	return claimed, nil
}

// RegulatoryFinish 是一笔报送的发送结果。租约令牌不匹配的记录不会被更新。
type RegulatoryFinish struct {
	ID         uint64
	LeaseToken string
	Delivered  bool
	Mode       string
	Error      string
}

// FinishRegulatoryReports 凭租约令牌推进报送状态。成功置 delivered；
// 失败回到 queued 并按有界指数退避重排：2 的 attempts 次幂秒，封顶 2^11。
//
// 返回推进成功与租约已失效的笔数——后者说明结果作废，本次发送白做了。
func (s WebhookDispatchStore) FinishRegulatoryReports(tx *gorm.DB, now time.Time, items []RegulatoryFinish) (finished, lost int, err error) {
	for _, item := range items {
		var res *gorm.DB
		if item.Delivered {
			res = tx.Exec(`UPDATE regulatory_report SET status='delivered',attempts=attempts+1,
				delivered_at=?,delivered_mode=?,last_error=NULL,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
				now, item.Mode, item.ID, item.LeaseToken)
		} else {
			// MySQL 按赋值从左到右求值：attempts 先 +1（等于旧值 +1），
			// 退避用 2 的 attempts 次幂秒。
			res = tx.Exec(`UPDATE regulatory_report SET status='queued',attempts=attempts+1,
				next_attempt_at=DATE_ADD(?, INTERVAL POW(2, LEAST(attempts,11)) SECOND),last_error=?,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
				now, TruncateRegulatoryError(item.Error), item.ID, item.LeaseToken)
		}
		if res.Error != nil {
			return finished, lost, res.Error
		}
		if res.RowsAffected == 1 {
			finished++
		} else {
			lost++
		}
	}
	return finished, lost, nil
}

// TruncateRegulatoryError 按字节上限截断报送错误，与该列的长度约束一致。
func TruncateRegulatoryError(value string) string {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
