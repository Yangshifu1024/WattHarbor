package central

const (
	StartAuthorizationRoute = "/api/v1/internal/charge-orders/:order_no/start-authorization"
	StopAuthorizationRoute  = "/api/v1/internal/charge-orders/:order_no/stop-authorization"
)

// StartAuthorizationReply 保留内部整数身份及已有设备执行单位；不暴露存储模型。
type StartAuthorizationReply struct {
	ConsumerType     uint8  `json:"consumer_type"`      // 原设备消费方式编码
	CardNumber       uint32 `json:"card_number"`        // 原设备协议卡号
	CardBalanceUnits uint16 `json:"card_balance_units"` // 原卡余额，协议单位0.1元
	ChargeOrderID    uint64 `json:"charge_order_id"`    // 原中央充电订单主键
	PaymentOrderID   uint64 `json:"payment_order_id"`   // 原支付单主键
	OrderNo          string `json:"order_no"`           // 原业务订单号
	UserID           uint64 `json:"user_id"`            // 原所属用户主键
	DeviceID         string `json:"device_id"`          // 原设备业务身份
	PortNo           uint8  `json:"port_no"`            // 原物理端口号，从1开始
	ChargeMode       uint8  `json:"charge_mode"`        // 原设备充电模式编码
	ChargeQuantity   uint16 `json:"charge_quantity"`    // 按原模式解释的协议充电量，不修改单位
}

// StopAuthorizationReply 保留原停止授权身份和启动命令，不提供新的停止凭据。
type StopAuthorizationReply struct {
	ChargeOrderID  uint64 `json:"charge_order_id"`  // 原中央充电订单主键
	OrderNo        string `json:"order_no"`         // 原业务订单号
	UserID         uint64 `json:"user_id"`          // 原所属用户主键
	DeviceID       string `json:"device_id"`        // 原设备业务身份
	PortNo         uint8  `json:"port_no"`          // 原物理端口号，从1开始
	PortID         uint64 `json:"port_id"`          // 原开通端口主键；可空字段表示未知
	StartCommandID string `json:"start_command_id"` // 原启动命令UUID
}
