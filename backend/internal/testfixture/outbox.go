//go:build integration

package testfixture

import "gorm.io/gorm"

// DeleteOutbox 先不加锁查询匹配的测试主键，再按全部主键字段和归属条件逐行删除。
// 不提交调用方事务，避免无索引的批量删除锁住其他测试的数据。
func DeleteOutbox(db *gorm.DB, filter string, args ...any) error {
	var ids []uint64
	if err := db.Table("event_outbox").Where(filter, args...).Order("id").Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := db.Table("event_outbox").Where("id=?", id).Where(filter, args...).Delete(map[string]any{}).Error; err != nil {
			return err
		}
	}
	return nil
}
