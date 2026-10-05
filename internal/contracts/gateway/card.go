package gateway

import "github.com/ChargePilot2026/charge-pilot/internal/protocol"

const (
	CardEventsPath  = "/api/v1/internal/card-events"               // 原待投递卡事件清单
	CardFreezePath  = "/api/v1/internal/card-events/decide-finish" // 原首写冻结决定
	CardAdvancePath = "/api/v1/internal/card-events/advance"       // 原重排或完成投递
	CardReplyPath   = "/api/v1/internal/cards/reply"               // 原向设备连接回复
	CardPortPath    = "/api/v1/internal/ports/resolve"             // 原设备端口业务码读取
)

// CardEvent 保留原事件 JSON 和已冻结回复，不能用当前钱包或新决定替换冻结值。
type CardEvent struct {
	EventKey string `json:"event_key"` // 原事件 UUID
	Payload  string `json:"payload"`   // 原设备事件 JSON
	Response string `json:"response"`  // 原冻结回复 JSON，未决定为空
}

// CardEvents 是原最多100条待投递清单。
type CardEvents struct {
	Items []CardEvent `json:"items"` /* 原事件集合；空为 [] */
}

// CardReply 保留原冻结决定；Accepted 表示已创建卡操作，无需另发拒绝／余额回复。
type CardReply struct {
	Accepted     bool                 `json:"accepted"`      // central 原操作已受理，不表示设备回执
	DeviceID     string               `json:"device_id"`     // 原设备身份
	Session      string               `json:"session"`       // 原6字节会话的十六进制文本
	Kind         protocol.CommandKind `json:"kind"`          // 原 card_denied 或 card_balance 命令
	CardNumber   uint32               `json:"card_number"`   // 原协议卡号，非字符串资金身份
	BalanceUnits uint16               `json:"balance_units"` // 原协议0.1元单位，转换及饱和由协议规则完成
	Invalid      bool                 `json:"invalid"`       // 原无效卡／拒绝标志
}

// CardFreeze 请求按原事件号首写冻结完整 JSON。
type CardFreeze struct {
	EventKey     string `json:"event_key" binding:"required,max=64"`        // 原事件 UUID
	ResponseJSON string `json:"response_json" binding:"required,max=65535"` // 原回复 JSON，不重复决策
}

// CardFrozen 返回此次首写标志及权威原回复。
type CardFrozen struct {
	Stored       bool   `json:"stored"`        // true 为本请求首写，false 为已有冻结值
	ResponseJSON string `json:"response_json"` // 最终原回复，竞争失败必须使用它
}

// CardAdvance 保留原事件身份和重排原因。
type CardAdvance struct {
	EventKey string `json:"event_key" binding:"required,max=64"`         // 原事件 UUID
	Outcome  string `json:"outcome" binding:"required,oneof=retry done"` // 原重排或完成状态
	Error    string `json:"error" binding:"omitempty,max=255"`           // 原有界错误，非凭据
}

// CardAdvanced 表达原投递状态行是否推进。
type CardAdvanced struct {
	Advanced bool `json:"advanced"` /* true 为实际推进一行 */
}

// CardReplyAccepted 只确认本次连接发送完成，不承诺设备消费确认。
type CardReplyAccepted struct {
	Accepted bool `json:"accepted"` /* true 为网关已发送原回复 */
}

// CardPort 返回原设备端口是否已开通及其二维码编码。
type CardPort struct {
	Found    bool   `json:"found"`               // false 表示未开通
	PortCode string `json:"port_code,omitempty"` // 原业务码，未开通时省略
}
