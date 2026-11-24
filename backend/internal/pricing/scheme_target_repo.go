package pricing

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/domain/charging"
	"wattharbor/internal/protocol"
)

// EffectiveRuleSQL 限定规则启用、未删除且当前时间有效；统计和校验共用，整站变更排除有自有规则的设备。
const EffectiveRuleSQL = "status='active' AND deleted_at IS NULL AND (effective_from IS NULL OR effective_from<=NOW(3)) AND (effective_to IS NULL OR effective_to>NOW(3))"

// CheckSchemeTargets 共享锁读目标设备，逐台检查协议是否支持方案，由 charging.Scheme.ValidateCapabilities 判定。
// 整站变更跳过已有生效自有规则的设备，使用调用方发布事务。
func CheckSchemeTargets(tx *gorm.DB, station uint64, device string, scheme charging.Scheme) error {
	var rows []struct {
		DeviceID        string             // 目标设备编号
		ProtocolAdapter protocol.AdapterID // 创建设备时选定的协议适配器，用于查询支持的功能
	}
	q := tx.Table("device_meta").Where("station_id=? AND deleted_at IS NULL", station)
	if device != "" {
		q = q.Where("device_id=?", device)
	} else {
		q = q.Where("device_id NOT IN (SELECT device_id FROM pricing_rule WHERE station_id=? AND device_id IS NOT NULL AND "+EffectiveRuleSQL+")", station)
	}
	if err := q.Clauses(clause.Locking{Strength: "SHARE"}).Find(&rows).Error; err != nil {
		return err
	}
	if device != "" && len(rows) != 1 {
		return gorm.ErrRecordNotFound
	}
	for _, d := range rows {
		cap, err := charging.ProtocolCapabilities(d.ProtocolAdapter)
		if err != nil {
			return fmt.Errorf("设备%s：%s", d.DeviceID, err)
		}
		if err := scheme.ValidateCapabilities(cap); err != nil {
			return fmt.Errorf("设备%s：%s", d.DeviceID, err)
		}
	}
	return nil
}
