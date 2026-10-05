// Package pricing 保存和读取完整计费规则，算法及协议计量判断由 domain/charging 提供。
package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	devicepkg "github.com/ChargePilot2026/charge-pilot/internal/device"
	"math"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
)

// Repo 读取 pricing 所属规则及目标能力，写方法接收用例管理的事务。
type Repo struct {
	DB *gorm.DB /* central 规则连接，由进程管理 */
}

// activeRuleQuery 在 ctx 下为 stationID／deviceID 构造当前生效规则的只读查询。
// 返回尚未执行的 DB；设备覆盖优先于站点默认，按版本和 ID 稳定选择，不自行冻结交易价格。
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

// ActiveStationRule 在 ctx 下返回 stationID 当前整站规则或不可用／格式／存储错误，不执行计价或写入。
func (s Repo) ActiveStationRule(ctx context.Context, stationID uint64) (charging.Rule, error) {
	return s.ActiveDeviceRule(ctx, stationID, "")
}

// ActiveDeviceRule 在 ctx 下读取 stationID／deviceID 的当前完整规则，设备覆盖优先于站点默认。
// 无连接、非法站点或无生效行返回 ErrRuleUnavailable，方案非法返回 ErrInvalidPricing，其余错误原样返回。
// 结果不包含当前价格的持久冻结；下单／刷卡用例必须在原交易中保存完整快照。
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
	// 保留完整方案；首个套餐提供基础执行视图，实际选购套餐仍在同一规则快照内选择。
	spec = spec.Scheme.SpecFor(spec.Scheme.Packages[0])
	device := row.DeviceID.String
	return charging.Rule{ID: row.ID, StationID: stationID, DeviceID: device, Version: row.Version, Spec: spec, Channel: row.Channel}, nil
}

// pricingRuleRow 是当前发布规则的读取投影，数据库映射不进入领域模型。
type pricingRuleRow struct {
	ID        uint64           `gorm:"column:id"`         // 原规则主键
	StationID uint64           `gorm:"column:station_id"` // 所属站点
	DeviceID  sql.NullString   `gorm:"column:device_id"`  // 空为整站默认，非空为设备覆盖
	SpecJSON  []byte           `gorm:"column:spec_json"`  // 完整计费方案与套餐 JSON
	Channel   charging.Channel `gorm:"column:channel"`    // 计费渠道标签，沿用原发布值
	Version   uint32           `gorm:"column:version"`    // 发布版本，非模板版本
}

// ActiveOffers 在 ctx 下按 station／device 当前生效完整规则返回可选套餐或规则读取错误，不修改价格。
func (s Repo) ActiveOffers(ctx context.Context, station uint64, device string) ([]charging.Offer, error) {
	rule, err := s.ActiveDeviceRule(ctx, station, device)
	if err != nil {
		return nil, err
	}
	return rule.Spec.Scheme.Offers(rule), nil
}

// ActiveOffer 在 ctx 下查找 station／device 当前套餐 id；无此套餐返回 ErrOfferUnavailable，其他错误原样返回。
// 结果仍是报价事实，调用方负责首次交易冻结，不据此重算历史订单。
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

// CheckDeviceScheme 在 ctx 下检查 station／device 选定适配器是否兼容 scheme，返回能力或读取错误。
// 仅用于新交易准入，不修改已付订单的冻结方案，无资金或设备指令副作用。
func (s Repo) CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme charging.Scheme) error {
	_, cap, err := (devicepkg.OperationRepo{DB: s.DB}).Capabilities(ctx, station, device)
	if err != nil {
		return err
	}
	return scheme.ValidateCapabilities(cap)
}
