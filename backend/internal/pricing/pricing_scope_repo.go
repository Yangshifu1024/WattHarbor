package pricing

import (
	"gorm.io/gorm"
)

// PricingTemplateRow 是计费模板查询记录，发布前须核对当前状态和版本。
type PricingTemplateRow struct {
	ID          uint64 `gorm:"column:id"`           // 模板主键
	Name        string `gorm:"column:name"`         // 模板名称
	Remark      string `gorm:"column:remark"`       // 模板备注
	SpecJSON    []byte `gorm:"column:spec_json"`    // 计费规则 JSON，对应 charging.Spec；发布前须解析并校验完整方案
	DisplayJSON []byte `gorm:"column:display_json"` // 用户端展示开关 JSON，对应 charging.Display
	Status      string `gorm:"column:status"`       // active 可用于发布；disabled 可编辑和查看，不能用于新发布
	Version     uint32 `gorm:"column:version"`      // 模板版本号，每次修改或停用都 +1，用作 expected_version 乐观锁
}

// RuleScope 按站点和设备构造规则条件，空设备匹配 SQL NULL。
func RuleScope(stationID uint64, deviceID string) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB {
		if deviceID == "" {
			return q.Where("station_id=? AND device_id IS NULL", stationID)
		}
		return q.Where("station_id=? AND device_id=?", stationID, deviceID)
	}
}

// NullableDevice 将空设备号转为 SQL NULL，其他设备号保持不变。
func NullableDevice(deviceID string) any {
	if deviceID == "" {
		return nil
	}
	return deviceID
}
