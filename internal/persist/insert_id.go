package persist

import "gorm.io/gorm"

// LastInsertID 读回当前连接上最近一次 INSERT 生成的自增主键。
//
// 必须在同一个 *gorm.DB（同一事务与同一条连接）上调用：主键是连接级状态，
// 换连接读到的可能是别的事务刚写入的值。用 map 建行时 gorm 不会回填主键，
// 只能这样取回。
func LastInsertID(tx *gorm.DB) (uint64, error) {
	var id uint64
	err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error
	return id, err
}
