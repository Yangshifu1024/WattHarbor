package charging

import (
	"errors"
)

// Offer 来自一份完整方案，套餐价格与计费规则必须一起发布。
type Offer struct {
	ServerDuration  bool   `json:"server_duration,omitempty"`
	PackageID       uint64 `json:"package_id,omitempty"`
	PurchaseCount   uint16 `json:"purchase_count,omitempty"`
	EnergyWh        uint32 `json:"energy_wh,omitempty"`
	MaxMinutes      uint16 `json:"max_minutes,omitempty"`
	ID              uint64 `json:"id"`
	StationID       uint64 `json:"station_id"`
	DeviceID        string `json:"device_id,omitempty"`
	Name            string `json:"name"`
	Mode            string `json:"mode"`
	PriceCents      int64  `json:"price_cents"`
	DurationMinutes uint16 `json:"duration_minutes"`
}

// ErrOfferUnavailable 表示所选套餐不存在或当前不可购买。
var ErrOfferUnavailable = errors.New("charging offer unavailable")

// Valid 校验套餐标识、金额和额度；未填 PurchaseCount 按一次购买校验。
func (o Offer) Valid() bool {
	count := int64(o.PurchaseCount)
	if count == 0 {
		count = 1
	}
	return (!o.ServerDuration || o.Mode == "duration") && o.ID > 0 && o.StationID > 0 && o.Name != "" && o.PriceCents > 0 && o.PriceCents <= maxRateCents*count &&
		(o.Mode == "amount" && o.DurationMinutes == 0 && o.EnergyWh == 0 || o.Mode == "duration" && o.DurationMinutes > 0 && o.DurationMinutes <= 4320 && o.EnergyWh == 0 || o.Mode == "energy" && o.DurationMinutes == 0 && o.EnergyWh >= 1000 && o.EnergyWh <= 65000 && o.EnergyWh%1000 == 0)
}
