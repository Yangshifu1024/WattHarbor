package identity

import "gorm.io/gorm"

// RequireActiveUserInTx 在调用方 tx 内确认 userID 仍为未删除的启用用户，返回缺失或存储错误。
// 为卡人工绑定提供所属用户事实；不创建账户、加业务锁或提交事务，不写卡及资金表。
func RequireActiveUserInTx(tx *gorm.DB, userID uint64) error {
	var row struct {
		Status string // 原用户启用状态
	}
	return tx.Table("user").Where("id=? AND status='active' AND deleted_at IS NULL", userID).Take(&row).Error
}
