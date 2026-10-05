//go:build integration

package testfixture

import "gorm.io/gorm"

// DeleteOutbox 在调用连接上非锁枚举符合原 filter 的夹具主键，再按完整主键及原归属条件逐行清理。
// 返回查询或首个删除错误，不提交调用方事务；避免无索引载荷范围删除锁住其他包的夹具。
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
