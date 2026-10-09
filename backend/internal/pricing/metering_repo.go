package pricing

import (
	"chargepilot/internal/device"
	"encoding/json"
	"errors"

	"chargepilot/internal/domain/charging"
	"gorm.io/gorm"
)

// 设备导入前的计费方式校验。判定"某个协议能否执行某种计费方式"的规则本身
// 属于领域层（charging.CheckModeSupport），这里只负责取数、归并与报错。

// unmarshalSpec 将 raw 计费 JSON 解入非 nil spec，返回空值或 JSON 错误；无 IO，不默认补全规则。
func unmarshalSpec(raw []byte, spec *charging.Spec) error {
	if len(raw) == 0 {
		return errors.New("计费口径为空")
	}
	return json.Unmarshal(raw, spec)
}

// stationSpecOf 在 tx 下读取 stationID 最新 active 整站计费口径，返回口径、存在标志及读取错误。
// 缺行或 JSON 非法沿用原行为返回不存在；不修改站点规则，不对 malformed 口径宣称兼容。
func stationSpecOf(tx *gorm.DB, stationID uint64) (charging.Spec, bool, error) {
	// row 只取 spec_json 一列；口径为空时下面 unmarshalSpec 会返回错误。
	row := struct {
		SpecJSON []byte /* 最新 active 整站原计费口径 JSON */
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

// CheckImportAgainstStation 在调用方 tx 下批量核对 devices 所属站点口径，逐站缓存存在标志及完整口径。
// 完整方案检查全部套餐及算法，旧模式口径沿用模式检查；无规则不能因缓存命中误判为存在空模式。
// 返回不兼容 ErrMeteringBlocked 或查询错误；未配置有效整站模式时跳过，不写入导入数据。
func (s Repo) CheckImportAgainstStation(tx *gorm.DB, devices []device.ImportDevice) error {
	// cachedSpec 在单次导入检查内缓存站点口径及实际存在标志，避免把未配置或不可解析的规则当成空模式；不形成跨请求缓存。
	type cachedSpec struct {
		spec   charging.Spec // 原规则口径，含完整方案或旧模式
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

// ErrMeteringBlocked 标识导入目标计费与选定协议不兼容，保留原中文提示。
type ErrMeteringBlocked struct {
	device  string              // 进不去的设备号
	station uint64              // 目标站点 id
	mode    charging.ChargeMode // 目标站点默认的计费方式
	reason  string              // 缺哪项能力的中文说明
}

// Error 返回原设备与不兼容原因提示；无 IO，不包含原数据库错误。
func (e *ErrMeteringBlocked) Error() string {
	return "设备 " + e.device + " 无法执行该站点的计费方式：" + e.reason
}
