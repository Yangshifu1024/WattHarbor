package database

import (
	"context"
	"encoding/json"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台的财务查询：分账、提现、对账与 Webhook 投递日志。
// 写入类操作（新建提现、审核、打款、跑对账）本来就归属 settlement 与
// reconcile 家族，这里只承接读接口和对账的处理标记。

// AdminFinanceStore 持有财务查询所需的连接：分账与提现在结算库，
// 对账记录与投递日志在 central_db。
type AdminFinanceStore struct {
	AdminDB, BillingDB *gorm.DB
}

// SettlementListItem 是分账列表的一行，附各参与方的分成明细。
// 明细是结算当时的快照，之后改分账模板不会回溯这张表。
type SettlementListItem struct {
	SettlementListRow
	Parties []SettlementPartyRow `json:"parties" gorm:"-"`
}

// ListSettlements 分页返回分账单并带上参与方明细，可按状态与单号/订单号关键字过滤。
func (s AdminFinanceStore) ListSettlements(ctx context.Context, q PageQuery) (Page[SettlementListItem], error) {
	out := Page[SettlementListItem]{Items: []SettlementListItem{}, Page: q.Page, PageSize: q.PageSize}
	query := s.BillingDB.WithContext(ctx).Table("settlement")
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		pattern := dbutil.LikePattern(q.Keyword)
		query = query.Where("(settlement_no LIKE ? ESCAPE '!' OR order_no LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	if err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error; err != nil {
		return out, err
	}
	for i := range out.Items {
		out.Items[i].Parties = []SettlementPartyRow{}
		if err := s.BillingDB.WithContext(ctx).Table("settlement_party_amount").
			Where("settlement_id = ?", out.Items[i].ID).Order("party_code").
			Find(&out.Items[i].Parties).Error; err != nil {
			return out, err
		}
	}
	return out, nil
}

// ListWithdraws 分页返回提现单，可按状态与单号/参与方编码关键字过滤。
func (s AdminFinanceStore) ListWithdraws(ctx context.Context, q PageQuery) (Page[WithdrawRow], error) {
	out := Page[WithdrawRow]{Items: []WithdrawRow{}, Page: q.Page, PageSize: q.PageSize}
	query := s.BillingDB.WithContext(ctx).Table("withdraw_request")
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		pattern := dbutil.LikePattern(q.Keyword)
		query = query.Where("(withdraw_no LIKE ? ESCAPE '!' OR party_code LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// ListReconciles 分页返回对账记录。reconcileType 为空表示不限类型；
// resolved 为 nil 表示不限处理状态。逐条差异从 diffs_json 解出后填回 Diffs，
// 解码失败的那条按空明细返回，不让一行坏数据让整个列表失败。
func (s AdminFinanceStore) ListReconciles(ctx context.Context, reconcileType string, resolved *bool, q PageQuery) (Page[ReconcileRow], error) {
	out := Page[ReconcileRow]{Items: []ReconcileRow{}, Page: q.Page, PageSize: q.PageSize}
	query := s.AdminDB.WithContext(ctx).Table("finance_reconcile_log")
	if reconcileType != "" {
		query = query.Where("reconcile_type = ?", reconcileType)
	}
	if resolved != nil {
		query = query.Where("resolved = ?", *resolved)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	if err := query.Order("reconcile_date DESC, id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error; err != nil {
		return out, err
	}
	ids := make([]uint64, 0, len(out.Items))
	for _, row := range out.Items {
		ids = append(ids, row.ID)
	}
	diffs := map[uint64][]byte{}
	if len(ids) > 0 {
		var stored []ReconcileDiffsRow
		if err := s.AdminDB.WithContext(ctx).Table("finance_reconcile_log").
			Select("id, CAST(diffs_json AS CHAR) AS diffs_json").Where("id IN ?", ids).Find(&stored).Error; err == nil {
			for _, row := range stored {
				diffs[row.ID] = row.Diffs
			}
		}
	}
	for i := range out.Items {
		out.Items[i].Diffs = []ReconcileDiff{}
		if raw := diffs[out.Items[i].ID]; len(raw) > 0 {
			_ = json.Unmarshal(raw, &out.Items[i].Diffs)
		}
	}
	return out, nil
}

// SetReconcileResolved 标记一条对账记录是否已处理。先在行锁下确认记录存在，
// 再按 ID 更新，重复调用结果一致。db 由调用方传入，以便与审计同事务提交。
func (s AdminFinanceStore) SetReconcileResolved(db *gorm.DB, id uint64, resolved bool) error {
	var existing struct{ ID uint64 }
	if err := db.Table("finance_reconcile_log").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).Take(&existing).Error; err != nil {
		return err
	}
	return db.Table("finance_reconcile_log").Where("id = ?", id).Updates(map[string]any{"resolved": resolved}).Error
}

// AdminWebhookDeliveryRow 是运营在后台看到的投递日志投影。
// 订阅方地址与签名密钥不在这里：投递由 worker 用订阅自己的密钥完成。
type AdminWebhookDeliveryRow struct {
	ID             uint64  `json:"id"`              // 投递日志主键 ID
	SubscriptionID uint64  `json:"subscription_id"` // 所属 Webhook 订阅 ID
	EventID        string  `json:"event_id"`        // 事件 ID,同一订阅内唯一,重发按它定位原始事件
	EventType      string  `json:"event_type"`      // 事件类型
	RequestBody    string  `json:"request_body"`    // 实际发出的请求体 JSON
	ResponseStatus *int    `json:"response_status"` // 订阅方 HTTP 状态码；未收到响应时为 NULL。
	ResponseBody   *string `json:"response_body"`   // 订阅方返回体(可空)
	ErrorMsg       *string `json:"error_msg"`       // 投递失败原因(可空),成功时为空
	AttemptCount   uint32  `json:"attempt_count"`   // 累计投递尝试次数
	DurationMs     *uint64 `json:"duration_ms"`     // 单次请求耗时毫秒(可空)
	DeliveredAt    string  `json:"delivered_at"`    // 投递时间
}

// ListWebhookDeliveries 分页返回某个订阅的投递日志，可按事件 ID 或事件类型关键字过滤。
func (s AdminFinanceStore) ListWebhookDeliveries(ctx context.Context, subscriptionID uint64, q PageQuery) (Page[AdminWebhookDeliveryRow], error) {
	out := Page[AdminWebhookDeliveryRow]{Items: []AdminWebhookDeliveryRow{}, Page: q.Page, PageSize: q.PageSize}
	query := s.AdminDB.WithContext(ctx).Table("webhook_delivery_log").Where("subscription_id = ?", subscriptionID)
	if q.Keyword != "" {
		pattern := dbutil.LikePattern(q.Keyword)
		query = query.Where("(event_id LIKE ? ESCAPE '!' OR event_type LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// WebhookSubscription 是重发前要确认的订阅状态。
type WebhookSubscription struct {
	ID         uint64
	URL        string   // 订阅方回调地址
	EventTypes []string // 订阅的事件类型列表
	Enabled    bool     // 订阅是否启用,停用时不允许重发
}

// FindWebhookSubscription 取一条未删除的订阅，取不到按未找到处理。
func (s AdminFinanceStore) FindWebhookSubscription(ctx context.Context, id uint64) (WebhookSubscription, error) {
	var row struct {
		ID         uint64
		URL        string
		EventTypes []byte
		Enabled    bool
	}
	err := s.AdminDB.WithContext(ctx).Table("webhook_subscription").
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	if err != nil {
		return WebhookSubscription{}, err
	}
	sub := WebhookSubscription{ID: row.ID, URL: row.URL, Enabled: row.Enabled}
	if len(row.EventTypes) > 0 {
		_ = json.Unmarshal(row.EventTypes, &sub.EventTypes)
	}
	return sub, nil
}

// WebhookOriginalDelivery 是手动重发时取回的原始投递内容。
type WebhookOriginalDelivery struct {
	EventType   string // 原事件类型
	RequestBody string // 原事件体,重发时作为 data 原样带回
}

// FindWebhookDelivery 按订阅与事件 ID 取回一次投递，重发的数据源就是它。
func (s AdminFinanceStore) FindWebhookDelivery(ctx context.Context, subscriptionID uint64, eventID string) (WebhookOriginalDelivery, error) {
	var row WebhookOriginalDelivery
	err := s.AdminDB.WithContext(ctx).Table("webhook_delivery_log").
		Select("event_type, request_body").
		Where("subscription_id = ? AND event_id = ?", subscriptionID, eventID).Take(&row).Error
	return row, err
}

// EnqueueAdminEvent 往后台事件队列表里投一个待投递事件，签名、SSRF 检查与投递日志
// 都留在正式投递链路上。db 由调用方传入，让入队与审计同事务提交。
func (s AdminFinanceStore) EnqueueAdminEvent(db *gorm.DB, stream, eventID string, envelope []byte) error {
	return db.Table(AdminEventOutboxTable).Create(&EventOutboxRecord{
		EventID: eventID, Stream: stream, EnvelopeJSON: envelope,
	}).Error
}
