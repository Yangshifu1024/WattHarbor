package ops

import "time"

// 后台若干列表与详情接口的查询投影。都是为某一次查询专门定义的扫描目标，
// 放在持久化层，处理器只负责组装参数与渲染响应。

// OrderTimelineRow 是订单时间线的一行（charge_event_log）。
type OrderTimelineRow struct {
	EventID string    `json:"event_id"`
	At      time.Time `json:"at" gorm:"column:occurred_at"`
	Event   string    `json:"event"`
	Actor   string    `json:"actor"`
	Detail  string    `json:"detail"`
}

// WebhookDeliveryRow 是一条待投递的 webhook 记录（webhook_delivery_log）。
type WebhookDeliveryRow struct {
	ID         uint64
	EventID    string
	ObjectType string
	ObjectKey  string
	Payload    string `gorm:"column:payload"` // 投递载荷
}
