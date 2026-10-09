package charging

import (
	"github.com/shopspring/decimal"
	"time"
)

// FeeFragment 保存区间用量、单价和取整前费用，用于解释 Cost 的合计结果。
type FeeFragment struct {
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	PowerW        uint32    `json:"power_w"`
	Quantity      string    `json:"quantity"`
	Unit          string    `json:"unit"`
	ElectricRate  int64     `json:"electric_rate"`
	ServiceRate   int64     `json:"service_rate"`
	ElectricCents string    `json:"electric_cents"`
	ServiceCents  string    `json:"service_cents"`
}

func fragment(start, end time.Time, power uint32, q decimal.Decimal, unit string, e, s int64) FeeFragment {
	return FeeFragment{StartedAt: start, EndedAt: end, PowerW: power, Quantity: q.StringFixed(6), Unit: unit, ElectricRate: e, ServiceRate: s, ElectricCents: q.Mul(decimal.NewFromInt(e)).StringFixed(6), ServiceCents: q.Mul(decimal.NewFromInt(s)).StringFixed(6)}
}
