package central

import (
	"errors"
	"time"
)

const (
	CardSwipePath   = "/api/v1/internal/cards/swipe"   // 原刷卡扣款决策端点
	CardBalancePath = "/api/v1/internal/cards/balance" // 在线卡可用余额接口
)

// CardSwipeRequest 提交刷卡事件编号和接收时间，扣款金额由服务端决定。
type CardSwipeRequest struct {
	CardNo     string    `json:"card_no"`     // 十进制卡号
	EventID    string    `json:"event_id"`    // 事件 UUID，重试沿用
	PortID     string    `json:"port_id"`     // 实际端口业务编码
	OccurredAt time.Time `json:"occurred_at"` // 网关接收事件的时间
}

// CardOperationReply 返回已保存的扣款结果，不代表设备已经启动。
type CardOperationReply struct {
	OperationID   string `json:"operation_id"`    // 操作 UUID
	DeviceID      string `json:"device_id"`       // 设备编号
	EventID       string `json:"event_id"`        // 刷卡事件
	ChargeOrderID uint64 `json:"charge_order_id"` // 原充电订单主键
	CardID        uint64 `json:"card_id"`         // 在线卡 ID
	Kind          string `json:"kind"`            // start 首购或 extend 续购
	PriceCents    int64  `json:"price_cents"`     // 本次实付金额，分
	Minutes       uint16 `json:"minutes"`         // 本次增加时长，分钟
	Status        string `json:"status"`          // 操作状态：confirming、confirmed 或 failed
}

// CardBalanceReply 返回卡有效性和可用余额；无效卡的余额为零。
type CardBalanceReply struct {
	Valid        bool  `json:"valid"`         // 卡、用户与钱包均有效
	BalanceCents int64 `json:"balance_cents"` // 可花费金额，分，非负
}

// ErrCardDeclined 表示收到 HTTP 409 且业务码为 2009 的明确拒绝；网络结果不确定时不返回此错误。
var ErrCardDeclined = errors.New("card operation declined")

// CardDeclineReply 保留拒绝时的余额不足语义，其他拒绝不得当成可补余额后重试。
type CardDeclineReply struct {
	InsufficientBalance bool /* 余额不足标志 */
}

// Error 返回公共拒绝文本。
func (e *CardDeclineReply) Error() string { return ErrCardDeclined.Error() }

// Unwrap 返回 ErrCardDeclined，供 errors.Is 判断是否明确拒绝。
func (e *CardDeclineReply) Unwrap() error { return ErrCardDeclined }
