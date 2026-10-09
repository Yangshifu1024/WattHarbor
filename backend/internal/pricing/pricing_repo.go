// Package pricing 保存和读取完整计费规则，算法及协议计量判断由 domain/charging 提供。
package pricing

import (
	devicepkg "chargepilot/internal/device"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"chargepilot/internal/domain/charging"
	"gorm.io/gorm"
)

// Repo 查询计费规则和设备能力，写入使用用例管理的事务。
type Repo struct {
	DB *gorm.DB /* central 规则连接，由进程管理 */
}

// activeRuleQuery 构造当前生效规则查询，设备规则优先于站点默认，再按版本和 ID 选择。
func (s Repo) activeRuleQuery(ctx context.Context, stationID uint64, deviceID string) *gorm.DB {
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

// ActiveStationRule 读取当前整站计费规则。
func (s Repo) ActiveStationRule(ctx context.Context, stationID uint64) (charging.Rule, error) {
	return s.ActiveDeviceRule(ctx, stationID, "")
}

// ActiveDeviceRule 读取完整规则，设备专属优先于站点默认。
// 无连接、非法站点或无生效规则返回 ErrRuleUnavailable，非法方案返回 ErrInvalidPricing。
// 下单和刷卡仍须在交易中保存完整价格快照。
func (s Repo) ActiveDeviceRule(ctx context.Context, stationID uint64, deviceID string) (charging.Rule, error) {
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
	// 保留完整方案；首个套餐提供基础参数，实际套餐从同一规则快照选择。
	spec = spec.Scheme.SpecFor(spec.Scheme.Packages[0])
	device := row.DeviceID.String
	return charging.Rule{ID: row.ID, StationID: stationID, DeviceID: device, Version: row.Version, Spec: spec, Channel: row.Channel}, nil
}

// pricingRuleRow 保存当前规则查询字段，数据库映射不进入领域模型。
type pricingRuleRow struct {
	ID        uint64           `gorm:"column:id"`         // 既定规则主键
	StationID uint64           `gorm:"column:station_id"` // 所属站点
	DeviceID  sql.NullString   `gorm:"column:device_id"`  // 空为整站默认，非空为设备覆盖
	SpecJSON  []byte           `gorm:"column:spec_json"`  // 完整计费方案与套餐 JSON
	Channel   charging.Channel `gorm:"column:channel"`    // 计费渠道标签，沿用首次发布值
	Version   uint32           `gorm:"column:version"`    // 发布版本，非模板版本
}

// ActiveOffers 读取当前生效规则中的可选套餐。
func (s Repo) ActiveOffers(ctx context.Context, station uint64, device string) ([]charging.Offer, error) {
	rule, err := s.ActiveDeviceRule(ctx, station, device)
	if err != nil {
		return nil, err
	}
	return rule.Spec.Scheme.Offers(rule), nil
}

// ActiveOffer 查找当前规则中指定套餐，无匹配返回 ErrOfferUnavailable。
// 报价由调用方在首次交易保存，不用于重算历史订单。
func (s Repo) ActiveOffer(ctx context.Context, station uint64, device string, id uint64) (charging.Offer, error) {
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

// CheckDeviceScheme 检查设备协议是否支持新交易方案，并返回支持的功能；已付订单快照不变。
func (s Repo) CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme charging.Scheme) error {
	_, cap, err := (devicepkg.OperationRepo{DB: s.DB}).Capabilities(ctx, station, device)
	if err != nil {
		return err
	}
	return scheme.ValidateCapabilities(cap)
}
