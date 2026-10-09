package pricing

import (
	"chargepilot/internal/device"
	"encoding/json"
	"errors"

	"chargepilot/internal/domain/charging"
	"gorm.io/gorm"
)

// 导入前读取并检查站点计费规则，协议能否支持计费方式由 charging.CheckModeSupport 判断。

// unmarshalSpec 解析计费 JSON，目标指针须非 nil，缺规则时不自动补全。
func unmarshalSpec(raw []byte, spec *charging.Spec) error {
	if len(raw) == 0 {
		return errors.New("计费口径为空")
	}
	return json.Unmarshal(raw, spec)
}

// stationSpecOf 读取站点最新 active 整站计费规则；缺记录或 JSON 不合法时返回不存在。
func stationSpecOf(tx *gorm.DB, stationID uint64) (charging.Spec, bool, error) {
	// row 只读取 spec_json；内容为空时解析失败。
	row := struct {
		SpecJSON []byte /* 最新 active 整站计费规则 JSON */
	}{}
	err := tx.Table("pricing_rule").
		Select("spec_json").
		Where("station_id=? AND device_id IS NULL AND status='active' AND deleted_at IS NULL", stationID).
		Order("version DESC").Limit(1).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return charging.Spec{}, false, nil
	}
	if err != nil {
		return charging.Spec{}, false, err
	}
	var spec charging.Spec
	if unmarshalSpec(row.SpecJSON, &spec) != nil {
		return charging.Spec{}, false, nil
	}
	return spec, true, nil
}

// CheckImportAgainstStation 按站点缓存规则和是否存在，再检查待导入设备协议。
// 完整方案须检查全部套餐和算法，旧格式检查计费模式；没有有效规则时跳过。
// 不兼容返回 ErrMeteringBlocked，无规则不能因缓存命中被当成空模式。
func (s Repo) CheckImportAgainstStation(tx *gorm.DB, devices []device.ImportDevice) error {
	// cachedSpec 仅在本次导入缓存站点规则及是否存在，未配置或解析失败不能当作空模式。
	type cachedSpec struct {
		spec   charging.Spec // 计费规则，包含完整方案或旧模式
		exists bool          // 是否实际存在可解析的整站规则
	}
	cache := map[uint64]cachedSpec{}
	for _, d := range devices {
		entry, cached := cache[d.StationID]
		if !cached {
			found, exists, err := stationSpecOf(tx, d.StationID)
			if err != nil {
				return err
			}
			entry = cachedSpec{spec: found, exists: exists}
			cache[d.StationID] = entry
		}
		if !entry.exists {
			continue
		}
		reason := ""
		if entry.spec.Scheme != nil {
			cap, err := charging.ProtocolCapabilities(d.ProtocolAdapter)
			if err == nil {
				err = entry.spec.Scheme.ValidateCapabilities(cap)
			}
			if err != nil {
				reason = err.Error()
			}
		} else {
			reason = charging.CheckModeSupport(entry.spec.Mode, d.ProtocolAdapter)
		}
		if reason != "" {
			return &ErrMeteringBlocked{device: d.DeviceID, station: d.StationID, mode: entry.spec.Mode, reason: reason}
		}
	}
	return nil
}

// ErrMeteringBlocked 表示站点计费方式与设备所选协议不兼容。
type ErrMeteringBlocked struct {
	device  string              // 计费不兼容的设备号
	station uint64              // 目标站点 id
	mode    charging.ChargeMode // 目标站点默认的计费方式
	reason  string              // 不支持功能的中文说明
}

// Error 返回设备号和不兼容原因，不含数据库错误。
func (e *ErrMeteringBlocked) Error() string {
	return "设备 " + e.device + " 无法执行该站点的计费方式：" + e.reason
}
