package central

const (
	StartAuthorizationRoute = "/api/v1/internal/charge-orders/:order_no/start-authorization"
	StopAuthorizationRoute  = "/api/v1/internal/charge-orders/:order_no/stop-authorization"
)

// StartAuthorizationReply 包含允许启动订单所需的订单编号和设备执行参数，数值单位保持协议规定。
type StartAuthorizationReply struct {
	ConsumerType     uint8  `json:"consumer_type"`      // 设备消费方式编码
	CardNumber       uint32 `json:"card_number"`        // 设备协议卡号
	CardBalanceUnits uint16 `json:"card_balance_units"` // 卡余额，协议单位为 0.1 元
	ChargeOrderID    uint64 `json:"charge_order_id"`    // 充电订单 ID
	PaymentOrderID   uint64 `json:"payment_order_id"`   // 支付单 ID
	OrderNo          string `json:"order_no"`           // 业务订单号
	UserID           uint64 `json:"user_id"`            // 用户 ID
	DeviceID         string `json:"device_id"`          // 设备编号
	PortNo           uint8  `json:"port_no"`            // 物理端口号，从 1 开始
	ChargeMode       uint8  `json:"charge_mode"`        // 设备充电模式编码
	ChargeQuantity   uint16 `json:"charge_quantity"`    // 充电量，单位由 ChargeMode 决定
}

// StopAuthorizationReply 包含允许停止的订单、用户、端口和启动命令编号。
type StopAuthorizationReply struct {
	ChargeOrderID  uint64 `json:"charge_order_id"`  // 充电订单 ID
	OrderNo        string `json:"order_no"`         // 业务订单号
	UserID         uint64 `json:"user_id"`          // 用户 ID
	DeviceID       string `json:"device_id"`        // 设备编号
	PortNo         uint8  `json:"port_no"`          // 物理端口号，从 1 开始
	PortID         uint64 `json:"port_id"`          // 已开通端口的 ID
	StartCommandID string `json:"start_command_id"` // 启动命令 UUID
}
