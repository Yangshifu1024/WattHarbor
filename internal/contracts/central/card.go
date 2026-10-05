package central

import (
	"errors"
	"time"
)

const (
	CardSwipePath   = "/api/v1/internal/cards/swipe"   // 原刷卡扣款决策端点
	CardBalancePath = "/api/v1/internal/cards/balance" // 原在线卡可花费余额端点
)

// CardSwipe 保留原网关事件身份和时效，不允许客户端传扣款金额。
type CardSwipe struct {
	CardNo     string    `json:"card_no"`     // 原十进制卡号
	EventID    string    `json:"event_id"`    // 原事件 UUID，重试沿用
	PortID     string    `json:"port_id"`     // 原真实端口业务编码
	OccurredAt time.Time `json:"occurred_at"` // 网关接收原事件的时刻
}

// CardOperation 是原扣款事务的持久结果，收到结果不等于设备已起充。
type CardOperation struct {
	OperationID   string `json:"operation_id"`    // 原操作 UUID
	DeviceID      string `json:"device_id"`       // 原设备身份
	EventID       string `json:"event_id"`        // 原刷卡事件
	ChargeOrderID uint64 `json:"charge_order_id"` // 原充电订单主键
	CardID        uint64 `json:"card_id"`         // 原在线卡主键
	Kind          string `json:"kind"`            // start 首购或 extend 续购
	PriceCents    int64  `json:"price_cents"`     // 本次实付金额，分
	Minutes       uint16 `json:"minutes"`         // 本次增加时长，分钟
	Status        string `json:"status"`          // confirming/confirmed/failed 原操作状态
}

// CardBalance 是原余额查询结果，非有效卡金额为零。
type CardBalance struct {
	Valid        bool  `json:"valid"`         // 卡、用户与钱包均有效
	BalanceCents int64 `json:"balance_cents"` // 可花费金额，分，非负
}

// ErrCardDeclined 表示明确收到原 HTTP 409／2009 业务拒绝，网络未知结果不属于拒绝。
var ErrCardDeclined = errors.New("card operation declined")

// CardDecline 保留拒绝时的余额不足语义，其他拒绝不得当成可补余额后重试。
type CardDecline struct {
	InsufficientBalance bool /* 原不足余额标志 */
}

// Error 返回公共拒绝文本，无 IO。
func (e *CardDecline) Error() string { return ErrCardDeclined.Error() }

// Unwrap 返回拒绝哨兵，供消费者分类，不推进投递或资金状态。
func (e *CardDecline) Unwrap() error { return ErrCardDeclined }
