package pricing

import (
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/device"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
)

// 设备导入前的计费方式校验。判定"某个协议能否执行某种计费方式"的规则本身
// 属于领域层（charging.CheckModeSupport），这里只负责取数、归并与报错。

// deviceCapability 是设备计量能力在 device_meta 上的落库形态。
// 是否支持电量/分段功率由所选协议决定，不落库，取数后由 charging.CheckModeSupport 判定。
type deviceCapability struct {
	ProtocolAdapter string `gorm:"column:protocol_adapter"`
	DeviceID        string `gorm:"column:device_id"` // 全局唯一设备号
}

func unmarshalSpec(raw []byte, spec *charging.Spec) error {
	if len(raw) == 0 {
		return errors.New("计费口径为空")
	}
	return json.Unmarshal(raw, spec)
}

func stationModeOf(tx *gorm.DB, stationID uint64) (charging.ChargeMode, bool, error) {
	// row 只取 spec_json 一列；口径为空时下面 unmarshalSpec 会返回错误。
	row := struct{ SpecJSON []byte }{}
	err := tx.Table("pricing_rule").
		Select("spec_json").
		Where("station_id=? AND device_id IS NULL AND status='active' AND deleted_at IS NULL", stationID).
		Order("version DESC").Limit(1).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var spec charging.Spec
	if unmarshalSpec(row.SpecJSON, &spec) != nil {
		return "", false, nil
	}
	return spec.Mode, true, nil
}

func CheckImportAgainstStation(tx *gorm.DB, devices []device.ImportDevice) error {
	cache := map[uint64]charging.ChargeMode{}
	for _, d := range devices {
		mode, ok := cache[d.StationID]
		if !ok {
			found, exists, err := stationModeOf(tx, d.StationID)
			if err != nil {
				return err
			}
			mode, ok = found, exists
			cache[d.StationID] = found
		}
		if !ok {
			continue
		}
		cap := deviceCapability{DeviceID: d.DeviceID, ProtocolAdapter: d.ProtocolAdapter}
		if reason := charging.CheckModeSupport(mode, cap.ProtocolAdapter); reason != "" {
			return &ErrMeteringBlocked{device: d.DeviceID, station: d.StationID, mode: mode, reason: reason}
		}
	}
	return nil
}

type ErrMeteringBlocked struct {
	device  string              // 进不去的设备号
	station uint64              // 目标站点 id
	mode    charging.ChargeMode // 目标站点默认的计费方式
	reason  string              // 缺哪项能力的中文说明
}

func (e *ErrMeteringBlocked) Error() string {
	return "设备 " + e.device + " 无法执行该站点的计费方式：" + e.reason
}

func NewDevicesOnly(tx *gorm.DB, devices []device.ImportDevice) ([]device.ImportDevice, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.DeviceID)
	}
	var known []string
	if err := tx.Table("device_meta").Where("device_id IN ? AND deleted_at IS NULL", ids).
		Pluck("device_id", &known).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(known))
	for _, id := range known {
		seen[id] = true
	}
	arriving := make([]device.ImportDevice, 0, len(devices))
	for _, d := range devices {
		if !seen[d.DeviceID] {
			arriving = append(arriving, d)
		}
	}
	return arriving, nil
}
