package charging

// Rule 保存已发布且关联站点的 Spec；预估与结算复用同一份规则及 Cost 算法。
type Rule struct {
	ID        uint64 `json:"rule_id"`
	StationID uint64 `json:"station_id"`
	// DeviceID 为空表示这条是整站默认规则。
	DeviceID string `json:"device_id,omitempty"`
	Version  uint32 `json:"version"`
	Spec     Spec   `json:"spec"`
	// Channel 是规则默认的充电启动入口，用于选择费率倍率。
	Channel Channel `json:"channel,omitempty"`
}

// Estimate 保存报价结果；预计电量用 kWh 文本表示，金额单位为分。
type Estimate struct {
	EstimatedKWh     string     `json:"estimated_kwh"`
	EstimatedMinutes uint16     `json:"estimated_minutes"`
	Mode             ChargeMode `json:"mode"`
	// Basis 在设备计费模式下为空，该模式不使用服务端费率估算。
	Basis         ServerBasis `json:"basis,omitempty"`
	ElectricCents int64       `json:"electric_cents"`
	ServiceCents  int64       `json:"service_cents"`
	TotalCents    int64       `json:"total_cents"`
	// PrepaidCents 为设备计费已收的预付款，单位为分，不由服务端费率推算。
	PrepaidCents   int64  `json:"prepaid_cents,omitempty"`
	ChargeMode     uint8  `json:"charge_mode"`
	ChargeQuantity uint16 `json:"charge_quantity"`
}
