package charging

import "time"

// 实时费用使用最后一次上报的用量和订单冻结价格；断线期间不继续累计。

// LiveMeterView 是某个订单此刻的实时计量与费用。
type LiveMeterView struct {
	At             time.Time `json:"at"`
	Stale          bool      `json:"stale"`
	KWh            float64   `json:"kwh"`
	Seconds        uint32    `json:"seconds"`
	Fee            *LiveFee  `json:"fee,omitempty"`
	FeeUnavailable string    `json:"fee_unavailable,omitempty"`
}

// LiveFee 是实时读数对应的费用拆分，单位分。
type LiveFee struct {
	ElectricCents int64 `json:"electric_cents"`
	ServiceCents  int64 `json:"service_cents"`
	TotalCents    int64 `json:"total_cents"`
}
