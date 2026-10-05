package pricing

import (
	"fmt"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EffectiveRuleSQL 是计费规则当前生效的条件：启用、未删除，且落在生效时间窗内。
// 方案变更时要排除设备已有的自有规则，统计与校验都必须用同一份定义。
const EffectiveRuleSQL = "status='active' AND deleted_at IS NULL AND (effective_from IS NULL OR effective_from<=NOW(3)) AND (effective_to IS NULL OR effective_to>NOW(3))"

// CheckSchemeTargets 校验一次方案变更不会让目标站点的任何设备落到协议不支持的计费方式上。
// 设备端具体能力由 charging.Scheme.ValidateCapabilities 判定，这里只负责取数与逐台套用。
// tx 属于发布事务，station／device 定义目标；共享锁读取目标设备，返回缺行、兼容或存储错误。
// 整站默认排除具有当前自有规则的设备，不修改设备或独立提交。
func CheckSchemeTargets(tx *gorm.DB, station uint64, device string, scheme charging.Scheme) error {
	var rows []struct {
		DeviceID        string // 目标设备原全局身份
		ProtocolAdapter string // 创建时明确选定的适配器，领域能力的来源
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
