package database

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

// SettlementPartyRow 是一笔分账中的参与方及其应得金额。名称与比例都是结算
// 当时的快照，之后改分账模板不会回溯这张表。
type SettlementPartyRow struct {
	PartyID     uint64 `gorm:"column:party_id" json:"party_id"`             // 参与方 ID
	PartyCode   string `gorm:"column:party_code" json:"party_code"`         // 参与方编码
	PartyName   string `gorm:"column:party_name" json:"party_name"`         // 参与方名称
	RatioBP     uint32 `gorm:"column:ratio_bp" json:"ratio_bp"`             // 分成比例,单位万分比(bp),10000 表示 100%
	Electric    int64  `gorm:"column:electric_cents" json:"electric_cents"` // 该参与方分到的电费金额,单位分
	Service     int64  `gorm:"column:service_cents" json:"service_cents"`   // 该参与方分到的服务费金额,单位分
	AmountCents int64  `gorm:"column:amount_cents" json:"amount_cents"`     // 该参与方分成合计,单位分
	Status      string `gorm:"column:status" json:"status"`                 // 这笔分成的状态:pending 待付 / paid 已付 / failed 失败
}

// WebhookEventTypesRow 读回订阅的事件类型。库中以 JSON 存的是数组字节，
// 查询时 CAST 为 CHAR 才能扫描进 []byte。
type WebhookEventTypesRow struct {
	ID         uint64 `gorm:"column:id"`
	EventTypes []byte `gorm:"column:event_types"`
}

// SettlementListRow 是分账列表的一行（settlement）。
type SettlementListRow struct {
	ID               uint64  `gorm:"column:id" json:"id"`                                                                 // 分账汇总主键 ID
	SettlementNo     string  `gorm:"column:settlement_no" json:"settlement_no"`                                           // 分账单号
	OrderNo          string  `gorm:"column:order_no" json:"order_no"`                                                     // 来源充电订单号
	Mode             string  `gorm:"column:mode" json:"mode"`                                                             // 分账模式:mode_a 电费与服务费都进分账池 / mode_b 只有服务费进分账池
	TemplateCode     *string `gorm:"column:split_template_code" json:"split_template_code"`                               // 分账模板编码快照(可空),早期分账只记模板 ID
	Status           string  `gorm:"column:status" json:"status"`                                                         // 分账单状态:pending 待确认 / confirmed 已确认 / paid 已付出 / failed 失败
	TotalCents       int64   `gorm:"column:total_cents" json:"total_cents"`                                               // 订单总金额,单位分
	ElectricCents    int64   `gorm:"column:electric_cents" json:"electric_cents"`                                         // 订单电费金额,单位分
	ServiceCents     int64   `gorm:"column:service_cents" json:"service_cents"`                                           // 订单服务费金额,单位分
	SplitPoolCents   int64   `gorm:"column:split_pool_cents" json:"split_pool_cents"`                                     // 进入分账池的金额,单位分
	ExcludedElectric int64   `gorm:"column:split_pool_excluded_electric_cents" json:"split_pool_excluded_electric_cents"` // mode_b 下被排除、不进分账池的电费金额,单位分
	CreatedAt        string  `gorm:"column:created_at" json:"created_at"`                                                 // 分账单创建时间
}

// ReconcileDiffsRow 读回对账记录的逐条差异明细。库中以 JSON 存，
// 查询时 CAST 为 CHAR 才能扫描进 []byte。
type ReconcileDiffsRow struct {
	ID    uint64 `gorm:"column:id"`
	Diffs []byte `gorm:"column:diffs_json"`
}

// WebhookDeliveryRow 是一条待投递的 webhook 记录（webhook_delivery_log）。
type WebhookDeliveryRow struct {
	ID         uint64
	EventID    string
	ObjectType string
	ObjectKey  string
	Payload    string `gorm:"column:payload"` // 投递载荷
}
