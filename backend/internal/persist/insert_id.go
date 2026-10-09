package persist

import "gorm.io/gorm"

// LastInsertID 读取当前连接最近一次 INSERT 生成的自增主键。
// 必须使用插入时的同一事务和连接；换连接可能读到别人的主键。
// GORM 用 map 插入时不会回填主键，可用本方法取回。
func LastInsertID(tx *gorm.DB) (uint64, error) {
	var id uint64
	err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error
	return id, err
}
