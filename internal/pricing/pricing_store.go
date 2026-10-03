// Package database 承载 central_db 的记录类型与查询入口。
package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
)

type PricingStore struct{ DB *gorm.DB }

// activeRuleQuery 是唯一决定一次充电按哪条已发布规则计价的地方，
// 于是可选套餐、报价与最终账单不可能对「当时生效的是哪份电价表」产生分歧。
func (s PricingStore) activeRuleQuery(ctx context.Context, stationID uint64, deviceID string) *gorm.DB {
	query := s.DB.WithContext(ctx).Table("pricing_rule AS r").
		Select(`r.id, r.station_id, r.device_id, r.spec_json, r.channel, r.version`).
		Joins("JOIN station AS s ON s.id = r.station_id").
		Where(`r.station_id = ? AND s.status = 'active' AND s.deleted_at IS NULL
			AND r.status = 'active' AND r.deleted_at IS NULL
			AND (r.effective_from IS NULL OR r.effective_from <= NOW(3))
			AND (r.effective_to IS NULL OR r.effective_to > NOW(3))`, stationID)
	if deviceID != "" {
		// 设备专属规则优先于站点默认规则；无专属规则时使用站点规则。
		query = query.Where(`r.device_id = ? OR r.device_id IS NULL`, deviceID)
	} else {
		// 仅 device_id 为空的规则代表站点默认，避免将设备覆盖规则误用为全站规则。
		query = query.Where(`r.device_id IS NULL`)
	}
	return query.Order("r.device_id IS NULL ASC, r.version DESC, r.id DESC")
}

// ActiveStationRule 按整站电价表给一次充电计价。
func (s PricingStore) ActiveStationRule(ctx context.Context, stationID uint64) (charging.Rule, error) {
	return s.ActiveDeviceRule(ctx, stationID, "")
}

// ActiveDeviceRule 优先读取设备覆盖规则，未配置时使用站点默认规则。
func (s PricingStore) ActiveDeviceRule(ctx context.Context, stationID uint64, deviceID string) (charging.Rule, error) {
	if s.DB == nil || stationID == 0 || stationID > math.MaxInt64 {
		return charging.Rule{}, charging.ErrRuleUnavailable
	}
	var row pricingRuleRow
	result := s.activeRuleQuery(ctx, stationID, deviceID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return charging.Rule{}, charging.ErrRuleUnavailable
	}
	if result.Error != nil {
		return charging.Rule{}, result.Error
	}
	var spec charging.Spec
	if json.Unmarshal(row.SpecJSON, &spec) != nil || spec.Scheme == nil || spec.Scheme.Validate() != nil {
		return charging.Rule{}, charging.ErrInvalidPricing
	}
	// The rule keeps the full scheme. The selected package supplies the execution
	// view later, within the same frozen rule read.
	spec = spec.Scheme.SpecFor(spec.Scheme.Packages[0])
	device := row.DeviceID.String
	return charging.Rule{ID: row.ID, StationID: stationID, DeviceID: device, Version: row.Version, Spec: spec, Channel: row.Channel}, nil
}

type pricingRuleRow struct {
	ID        uint64           `gorm:"column:id"`
	StationID uint64           `gorm:"column:station_id"`
	DeviceID  sql.NullString   `gorm:"column:device_id"`
	SpecJSON  []byte           `gorm:"column:spec_json"`
	Channel   charging.Channel `gorm:"column:channel"`
	Version   uint32           `gorm:"column:version"`
}

func (s PricingStore) ActiveOffers(ctx context.Context, station uint64, device string) ([]charging.Offer, error) {
	rule, err := s.ActiveDeviceRule(ctx, station, device)
	if err != nil {
		return nil, err
	}
	return rule.Spec.Scheme.Offers(rule), nil
}
func (s PricingStore) ActiveOffer(ctx context.Context, station uint64, device string, id uint64) (charging.Offer, error) {
	rows, err := s.ActiveOffers(ctx, station, device)
	if err != nil {
		return charging.Offer{}, err
	}
	for _, o := range rows {
		if o.ID == id {
			return o, nil
		}
	}
	return charging.Offer{}, charging.ErrOfferUnavailable
}

func (s PricingStore) DeviceCapabilities(ctx context.Context, station uint64, device string) (string, charging.Capabilities, error) {
	var d struct{ ProtocolAdapter string }
	if err := s.DB.WithContext(ctx).Table("device_meta").Select("protocol_adapter").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, device).Take(&d).Error; err != nil {
		return "", charging.Capabilities{}, err
	}
	cap, err := charging.ProtocolCapabilities(d.ProtocolAdapter)
	return d.ProtocolAdapter, cap, err
}

// Frozen paid orders retain their rates; new orders use the selected protocol.
func (s PricingStore) CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme charging.Scheme) error {
	_, cap, err := s.DeviceCapabilities(ctx, station, device)
	if err != nil {
		return err
	}
	return scheme.ValidateCapabilities(cap)
}
