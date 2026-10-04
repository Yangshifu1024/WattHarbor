package central

const (
	StartAuthorizationRoute = "/api/v1/internal/charge-orders/:order_no/start-authorization"
	StopAuthorizationRoute  = "/api/v1/internal/charge-orders/:order_no/stop-authorization"
)

// StartAuthorization 保留内部整数身份及已有设备执行单位；不暴露存储模型。
type StartAuthorization struct {
	ConsumerType     uint8  `json:"consumer_type"`
	CardNumber       uint32 `json:"card_number"`
	CardBalanceUnits uint16 `json:"card_balance_units"`
	ChargeOrderID    uint64 `json:"charge_order_id"`
	PaymentOrderID   uint64 `json:"payment_order_id"`
	OrderNo          string `json:"order_no"`
	UserID           uint64 `json:"user_id"`
	DeviceID         string `json:"device_id"`
	PortNo           uint8  `json:"port_no"`
	ChargeMode       uint8  `json:"charge_mode"`
	ChargeQuantity   uint16 `json:"charge_quantity"`
}

type StopAuthorization struct {
	ChargeOrderID  uint64 `json:"charge_order_id"`
	OrderNo        string `json:"order_no"`
	UserID         uint64 `json:"user_id"`
	DeviceID       string `json:"device_id"`
	PortNo         uint8  `json:"port_no"`
	PortID         uint64 `json:"port_id"`
	StartCommandID string `json:"start_command_id"`
}
