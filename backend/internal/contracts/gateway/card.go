package gateway

import "chargepilot/internal/protocol"

const (
	CardEventsPath  = "/api/v1/internal/card-events"               // 待处理的卡事件列表
	CardFreezePath  = "/api/v1/internal/card-events/decide-finish" // 保存首次处理结果
	CardAdvancePath = "/api/v1/internal/card-events/advance"       // 安排重试或标记完成
	CardReplyPath   = "/api/v1/internal/cards/reply"               // 向设备发送回复
	CardPortPath    = "/api/v1/internal/ports/resolve"             // 查询设备端口业务编码
)

// CardEvent 包含刷卡事件和首次保存的回复，重复处理不得按当前余额重算回复。
type CardEvent struct {
	EventKey string `json:"event_key"` // 事件 UUID
	Payload  string `json:"payload"`   // 设备事件原始 JSON
	Response string `json:"response"`  // 首次保存的回复 JSON，尚未处理时为空
}

// CardEventsReply 返回最多 100 条待处理卡事件。
type CardEventsReply struct {
	Items []CardEvent `json:"items"` /* 事件列表，无记录时为 [] */
}

// CardReplyRequest 将首次保存的结果回复给设备。
// Accepted=true 表示 central 已接受卡操作，worker 会跳过拒绝或余额回复。
type CardReplyRequest struct {
	Accepted     bool                 `json:"accepted"`      // central 是否已接受卡操作，不代表设备确认
	DeviceID     string               `json:"device_id"`     // 设备编号
	Session      string               `json:"session"`       // 六字节设备会话号，使用十六进制文本
	Kind         protocol.CommandKind `json:"kind"`          // card_denied 或 card_balance 命令
	CardNumber   uint32               `json:"card_number"`   // 协议中的整数卡号
	BalanceUnits uint16               `json:"balance_units"` // 余额单位为 0.1 元，由协议规则转换并限制最大值
	Invalid      bool                 `json:"invalid"`       // 是否为无效卡或拒绝操作
}

// CardFreezeRequest 请求按事件号保存首次处理结果的完整 JSON。
type CardFreezeRequest struct {
	EventKey     string `json:"event_key" binding:"required,max=64"`        // 事件 UUID
	ResponseJSON string `json:"response_json" binding:"required,max=65535"` // 首次保存的回复 JSON，不重新计算
}

// CardFreezeReply 返回是否首次保存及首次保存的回复。
type CardFreezeReply struct {
	Stored       bool   `json:"stored"`        // true 表示本次首次保存，false 表示此前已保存
	ResponseJSON string `json:"response_json"` // 首次保存的回复；其他请求先保存时必须使用其结果
}

// CardAdvanceOutcome 是 worker 推进刷卡投递结果时的外部契约输入取值（retry/done）。
//
// 与数据库 card_event_delivery.status（pending/done）不是同一集合：
// 前者表达调用方对本次处理的判断，后者是库内投递进度；
// 不得合并成一个类型，也不得用本类型表达或复用 DB 投递状态。
type CardAdvanceOutcome string

const (
	// CardAdvanceOutcomeRetry 表示本次处理未完成，安排重试。
	CardAdvanceOutcomeRetry CardAdvanceOutcome = "retry"
	// CardAdvanceOutcomeDone 表示本次处理完成，不再重试。
	CardAdvanceOutcomeDone CardAdvanceOutcome = "done"
)

// Known 判断是否属于本端点的合法取值集合（两值）。
// 只用于识别输入取值，不改变 binding 校验行为；未知值仍由 oneof 标签拒绝。
func (o CardAdvanceOutcome) Known() bool {
	return o == CardAdvanceOutcomeRetry || o == CardAdvanceOutcomeDone
}

// CardAdvanceRequest 按事件编号标记完成或安排重试，并记录重试原因。
type CardAdvanceRequest struct {
	EventKey string             `json:"event_key" binding:"required,max=64"`         // 事件 UUID
	Outcome  CardAdvanceOutcome `json:"outcome" binding:"required,oneof=retry done"` // retry 安排重试，done 标记完成
	Error    string             `json:"error" binding:"omitempty,max=255"`           // 重试原因，最多 255 字符，不得包含凭据
}

// CardAdvanceReply 返回本次是否更新了投递状态。
type CardAdvanceReply struct {
	Advanced bool `json:"advanced"` /* true 表示更新了一行 */
}

// CardDeliveryReply 确认回复已写入设备连接；不代表设备已收到或执行。
type CardDeliveryReply struct {
	Accepted bool `json:"accepted"` /* true 表示已写入设备连接 */
}

// CardPortReply 返回设备端口是否开通及其二维码编码。
type CardPortReply struct {
	Found    bool   `json:"found"`               // false 表示未开通
	PortCode string `json:"port_code,omitempty"` // 业务码，未开通时省略
}
